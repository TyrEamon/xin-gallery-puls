package backup

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pixiv-tg-gallery/internal/database"
	"pixiv-tg-gallery/internal/telegram"
)

type Config struct {
	Enabled            bool
	WebDAVURL          string
	WebDAVUsername     string
	WebDAVPassword     string
	BasePath           string
	Workers            int
	RetryMax           int
	PollSeconds        int
	TaskTimeoutSeconds int
}

type HealthSnapshot struct {
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	Status    string `json:"status"`
	LastError string `json:"last_error,omitempty"`
	CheckedAt int64  `json:"checked_at"`
	BasePath  string `json:"base_path,omitempty"`
	WebDAVURL string `json:"webdav_url,omitempty"`
}

type StatsSnapshot struct {
	Enabled    bool  `json:"enabled"`
	Running    bool  `json:"running"`
	Pending    int64 `json:"pending"`
	Processing int64 `json:"processing"`
	Synced     int64 `json:"synced"`
	Failed     int64 `json:"failed"`
	Total      int64 `json:"total"`
	InFlight   int64 `json:"in_flight"`
	RetryMax   int   `json:"retry_max"`
	UpdatedAt  int64 `json:"updated_at"`
}

type Service struct {
	cfg Config
	db  *database.Client
	tg  *telegram.Client
	dav *webdavClient

	taskCh chan database.BackupTask

	inFlightMu sync.Mutex
	inFlight   map[string]struct{}

	healthMu sync.RWMutex
	health   HealthSnapshot
}

func New(cfg Config, db *database.Client, tg *telegram.Client) *Service {
	cfg = normalizeConfig(cfg)
	s := &Service{
		cfg:      cfg,
		db:       db,
		tg:       tg,
		taskCh:   make(chan database.BackupTask, cfg.Workers*3),
		inFlight: make(map[string]struct{}),
		health: HealthSnapshot{
			Enabled:   cfg.Enabled,
			Running:   false,
			Status:    "disabled",
			CheckedAt: time.Now().Unix(),
			BasePath:  cfg.BasePath,
			WebDAVURL: cfg.WebDAVURL,
		},
	}

	if !cfg.Enabled {
		return s
	}

	if cfg.WebDAVURL == "" || cfg.WebDAVUsername == "" || cfg.WebDAVPassword == "" {
		s.setHealth(false, "degraded", "backup webdav config is incomplete")
		return s
	}

	dav, err := newWebDAVClient(cfg.WebDAVURL, cfg.WebDAVUsername, cfg.WebDAVPassword)
	if err != nil {
		s.setHealth(false, "degraded", fmt.Sprintf("backup webdav init failed: %v", err))
		return s
	}
	s.dav = dav
	s.setHealth(false, "init", "")
	return s
}

func normalizeConfig(cfg Config) Config {
	if cfg.BasePath == "" {
		cfg.BasePath = "/MyPixiv"
	}
	cfg.BasePath = "/" + strings.Trim(strings.TrimSpace(cfg.BasePath), "/")
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = 5
	}
	if cfg.PollSeconds <= 0 {
		cfg.PollSeconds = 8
	}
	if cfg.TaskTimeoutSeconds <= 0 {
		cfg.TaskTimeoutSeconds = 120
	}
	return cfg
}

func (s *Service) Enabled() bool {
	return s.cfg.Enabled
}

func (s *Service) CanRun() bool {
	return s.cfg.Enabled && s.dav != nil
}

func (s *Service) Start(ctx context.Context) {
	if !s.CanRun() {
		return
	}

	if err := s.checkHealth(ctx); err != nil {
		log.Printf("[BACKUP] health check failed on start: %v", err)
	} else {
		log.Printf("[BACKUP] health check ok")
	}
	s.setHealth(true, s.healthStatus(), s.healthError())

	go s.dispatch(ctx)
	for i := 0; i < s.cfg.Workers; i++ {
		go s.worker(ctx, i+1)
	}

	log.Printf("[BACKUP] worker started (workers=%d, retry_max=%d, base_path=%s)", s.cfg.Workers, s.cfg.RetryMax, s.cfg.BasePath)
}

func (s *Service) dispatch(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(s.cfg.PollSeconds) * time.Second)
	defer ticker.Stop()

	s.fillTasks(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.fillTasks(ctx)
		}
	}
}

func (s *Service) fillTasks(ctx context.Context) {
	capacity := cap(s.taskCh) - len(s.taskCh)
	if capacity <= 0 {
		return
	}

	tasks, err := s.db.ListBackupTasks(ctx, capacity, s.cfg.RetryMax)
	if err != nil {
		log.Printf("[BACKUP] list tasks failed: %v", err)
		return
	}
	if len(tasks) == 0 {
		return
	}

	for _, task := range tasks {
		if task.ImageID == "" {
			continue
		}
		if !s.markInFlight(task.ImageID) {
			continue
		}
		select {
		case s.taskCh <- task:
		case <-ctx.Done():
			s.unmarkInFlight(task.ImageID)
			return
		}
	}
}

func (s *Service) worker(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-s.taskCh:
			s.handleTask(ctx, workerID, task)
		}
	}
}

func (s *Service) handleTask(parent context.Context, workerID int, task database.BackupTask) {
	defer s.unmarkInFlight(task.ImageID)

	ctx, cancel := context.WithTimeout(parent, time.Duration(s.cfg.TaskTimeoutSeconds)*time.Second)
	defer cancel()

	previewPath, originPath, err := s.backupTask(ctx, task)
	if err != nil {
		log.Printf("[BACKUP] worker=%d image=%s failed: %v", workerID, task.ImageID, err)
		_ = s.db.MarkBackupFailed(parent, task.ImageID, trimErr(err.Error(), 500))
		return
	}

	if err := s.db.MarkBackupSynced(parent, task.ImageID, previewPath, originPath); err != nil {
		log.Printf("[BACKUP] worker=%d image=%s mark synced failed: %v", workerID, task.ImageID, err)
		_ = s.db.MarkBackupFailed(parent, task.ImageID, trimErr(err.Error(), 500))
		return
	}
	log.Printf("[BACKUP] worker=%d image=%s synced", workerID, task.ImageID)
}

func (s *Service) backupTask(ctx context.Context, task database.BackupTask) (string, string, error) {
	if strings.TrimSpace(task.PreviewID) == "" && strings.TrimSpace(task.OriginID) == "" {
		return "", "", fmt.Errorf("missing preview_id and origin_id")
	}

	previewPath := ""
	originPath := ""

	if strings.TrimSpace(task.PreviewID) != "" {
		p, err := s.uploadFromTG(ctx, task, "preview", task.PreviewID)
		if err != nil {
			return previewPath, originPath, err
		}
		previewPath = p
	}

	if strings.TrimSpace(task.OriginID) != "" {
		p, err := s.uploadFromTG(ctx, task, "origin", task.OriginID)
		if err != nil {
			return previewPath, originPath, err
		}
		originPath = p
	}

	return previewPath, originPath, nil
}

func (s *Service) uploadFromTG(ctx context.Context, task database.BackupTask, kind, fileID string) (string, error) {
	data, filePath, err := s.tg.DownloadFile(ctx, fileID)
	if err != nil {
		return "", fmt.Errorf("download telegram file %s: %w", kind, err)
	}

	ext := normalizeExt(filepath.Ext(filePath), defaultExt(kind))
	remotePath := s.buildRemotePath(task, kind, ext)
	contentType := detectContentType(data, kind)
	if err := s.dav.UploadBytes(ctx, remotePath, data, contentType); err != nil {
		return "", fmt.Errorf("upload webdav %s: %w", kind, err)
	}

	return remotePath, nil
}

func (s *Service) buildRemotePath(task database.BackupTask, kind, ext string) string {
	t := time.Now().UTC()
	if task.CreatedAt > 0 {
		t = time.Unix(task.CreatedAt, 0).UTC()
	}
	source := sanitizeSegment(task.Source)
	fileName := fmt.Sprintf("%s_%s%s", sanitizeSegment(task.ImageID), kind, ext)
	return path.Join(s.cfg.BasePath, kind, source, fmt.Sprintf("%04d", t.Year()), fmt.Sprintf("%02d", int(t.Month())), fileName)
}

func sanitizeSegment(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return "unknown"
	}
	return out
}

func defaultExt(kind string) string {
	if kind == "preview" {
		return ".jpg"
	}
	return ".bin"
}

func normalizeExt(ext, fallback string) string {
	ext = strings.TrimSpace(strings.ToLower(ext))
	if ext == "" {
		return fallback
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if len(ext) > 8 {
		return fallback
	}
	return ext
}

func detectContentType(data []byte, kind string) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	sample := data
	if len(sample) > 512 {
		sample = sample[:512]
	}
	ct := http.DetectContentType(sample)
	if ct == "application/octet-stream" && kind == "preview" {
		return "image/jpeg"
	}
	return ct
}

func trimErr(msg string, max int) string {
	msg = strings.TrimSpace(msg)
	if max <= 0 || len(msg) <= max {
		return msg
	}
	return msg[:max]
}

func (s *Service) checkHealth(ctx context.Context) error {
	if !s.CanRun() {
		s.setHealth(false, "degraded", "backup is disabled or not initialized")
		return fmt.Errorf("backup service unavailable")
	}

	healthDir := path.Join(s.cfg.BasePath, "_health")
	if err := s.dav.HealthCheck(ctx, healthDir); err != nil {
		s.setHealth(true, "degraded", err.Error())
		return err
	}
	s.setHealth(true, "ok", "")
	return nil
}

func (s *Service) Health(ctx context.Context, probe bool) HealthSnapshot {
	if probe {
		_ = s.checkHealth(ctx)
	}
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()
	return s.health
}

func (s *Service) Stats(ctx context.Context) (StatsSnapshot, error) {
	stats, err := s.db.GetBackupStats(ctx)
	if err != nil {
		return StatsSnapshot{}, err
	}
	health := s.Health(ctx, false)
	return StatsSnapshot{
		Enabled:    s.cfg.Enabled,
		Running:    health.Running,
		Pending:    stats.Pending,
		Processing: stats.Processing,
		Synced:     stats.Synced,
		Failed:     stats.Failed,
		Total:      stats.Total,
		InFlight:   int64(s.inFlightCount()),
		RetryMax:   s.cfg.RetryMax,
		UpdatedAt:  time.Now().Unix(),
	}, nil
}

func (s *Service) Backfill(ctx context.Context, limit int) (int64, error) {
	if !s.cfg.Enabled {
		return 0, fmt.Errorf("backup disabled")
	}
	return s.db.BackfillBackupTasks(ctx, limit)
}

func (s *Service) RetryFailed(ctx context.Context, limit int) (int64, error) {
	if !s.cfg.Enabled {
		return 0, fmt.Errorf("backup disabled")
	}
	return s.db.RetryFailedBackupTasks(ctx, limit)
}

func (s *Service) markInFlight(imageID string) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	if _, exists := s.inFlight[imageID]; exists {
		return false
	}
	s.inFlight[imageID] = struct{}{}
	return true
}

func (s *Service) unmarkInFlight(imageID string) {
	s.inFlightMu.Lock()
	delete(s.inFlight, imageID)
	s.inFlightMu.Unlock()
}

func (s *Service) inFlightCount() int {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	return len(s.inFlight)
}

func (s *Service) setHealth(running bool, status, lastErr string) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.health.Enabled = s.cfg.Enabled
	s.health.Running = running
	s.health.Status = status
	s.health.LastError = trimErr(lastErr, 600)
	s.health.CheckedAt = time.Now().Unix()
	s.health.BasePath = s.cfg.BasePath
	s.health.WebDAVURL = s.cfg.WebDAVURL
}

func (s *Service) healthStatus() string {
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()
	return s.health.Status
}

func (s *Service) healthError() string {
	s.healthMu.RLock()
	defer s.healthMu.RUnlock()
	return s.health.LastError
}
