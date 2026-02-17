package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"pixiv-tg-gallery/internal/app"
	"pixiv-tg-gallery/internal/backup"
	"pixiv-tg-gallery/internal/config"
	"pixiv-tg-gallery/internal/database"
	"pixiv-tg-gallery/internal/pixiv"
	"pixiv-tg-gallery/internal/telegram"
	"pixiv-tg-gallery/internal/umami"
	"pixiv-tg-gallery/internal/web"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func main() {
	cfg := config.Load()
	if cfg.BotToken == "" || cfg.PublishChannelID == 0 || cfg.StorageChannelID == 0 {
		log.Fatal("BOT_TOKEN or channel config missing (PUBLISH_CHANNEL_ID/STORAGE_CHANNEL_ID or CHANNEL_ID)")
	}
	if cfg.D1AccountID == "" || cfg.D1ApiToken == "" || cfg.D1DatabaseID == "" {
		log.Fatal("D1 credentials missing")
	}
	if cfg.AdminPassword == "" {
		log.Println("warning: ADMIN_PASSWORD is empty, /admin will be blocked")
	}

	db := database.New(cfg.D1AccountID, cfg.D1ApiToken, cfg.D1DatabaseID)
	if err := db.EnsureSchema(context.Background()); err != nil {
		log.Fatalf("ensure schema error: %v", err)
	}
	tg, err := telegram.New(cfg.BotToken, cfg.PublishChannelID, cfg.StorageChannelID, cfg.DiscussionGroupID)
	if err != nil {
		log.Fatal(err)
	}
	tg.SetPreviewHasSpoiler(cfg.PreviewHasSpoiler)

	pv := pixiv.New(cfg.PixivPHPSESSID, cfg.PixivUserID, cfg.PixivRest)
	application := app.New(cfg, db, tg, pv)
	application.InitRuntimeFlags(context.Background())
	backupSvc := backup.New(backup.Config{
		Enabled:            cfg.BackupEnabled,
		WebDAVURL:          cfg.BackupWebDAVURL,
		WebDAVUsername:     cfg.BackupWebDAVUsername,
		WebDAVPassword:     cfg.BackupWebDAVPassword,
		BasePath:           cfg.BackupBasePath,
		Workers:            cfg.BackupWorkers,
		RetryMax:           cfg.BackupRetryMax,
		PollSeconds:        cfg.BackupPollSeconds,
		TaskTimeoutSeconds: cfg.BackupTaskTimeoutSeconds,
	}, db, tg)
	if cfg.BackupEnabled && !backupSvc.CanRun() {
		log.Println("backup service enabled but not ready; check BACKUP_WEBDAV_* config")
	}
	um := umami.New(umami.Config{
		BaseURL:      cfg.UmamiBaseURL,
		WebsiteID:    cfg.UmamiWebsiteIDFrontend,
		Username:     cfg.UmamiUsername,
		Password:     cfg.UmamiPassword,
		APIToken:     cfg.UmamiAPIToken,
		LookbackDays: cfg.UmamiLookbackDays,
	})

	tg.Bot.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		if update.Message == nil {
			return false
		}
		return application.CanHandleTGMessage(update.Message)
	}, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		result, err := application.HandleTGMessage(ctx, update.Message)
		if err != nil {
			log.Printf("tg handle error: %v", err)
			if update.Message != nil && update.Message.Chat.ID != cfg.PublishChannelID && update.Message.Chat.ID != cfg.StorageChannelID && update.Message.Chat.ID != cfg.DiscussionGroupID {
				_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.Message.Chat.ID,
					Text:   fmt.Sprintf("\u8fd9\u6b21\u662f\u7f51\u7edc\u95f9\u813e\u6c14\u4e86\u55b5~\n\u9519\u8bef\uff1a%v", err),
				})
			}
			return
		}
		if result != nil && update.Message != nil && update.Message.Chat.ID != cfg.ChannelID {
			replyText := strings.TrimSpace(result.Summary)
			if replyText == "" {
				replyText = fmt.Sprintf("\u54fc\uff0c\u7ed9\u4f60\u5904\u7406\u597d\u4e86\u55b5~\n\u6807\u9898\uff1a%s\nID\uff1a%s", result.Title, result.ID)
			}
			_, _ = b.SendMessage(ctx, &bot.SendMessageParams{
				ChatID: update.Message.Chat.ID,
				Text:   replyText,
			})
		}
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application.StartPixivCrawler(ctx)
	application.StartTwitterAuthorCrawler(ctx)
	backupSvc.Start(ctx)

	mux := http.NewServeMux()
	server := web.New(cfg, db, tg, application, um, backupSvc)
	server.Register(mux)

	httpSrv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}

	go func() {
		log.Printf("HTTP server listening on %s", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	go tg.Start(ctx)

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	tg.Stop()
	log.Println("shutdown complete")
}
