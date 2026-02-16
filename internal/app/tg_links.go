package app

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"path"
	"pixiv-tg-gallery/internal/telegram"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxTGLinksPerMessage    = 3
	defaultTwitterAPIDomain = "fxtwitter.com"
	maxPixivAlbumGroup      = 10
)

var (
	urlPattern        = regexp.MustCompile(`https?://[^\s]+`)
	pixivIDPattern    = regexp.MustCompile(`^\d+$`)
	yandeIDPattern    = regexp.MustCompile(`^\d+$`)
	twitterIDPattern  = regexp.MustCompile(`^\d+$`)
	hashtagPattern    = regexp.MustCompile(`#([\p{L}\p{N}_][\p{L}\p{N}_\p{M}]*)`)
	pixivBRTagPattern = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlTagPattern    = regexp.MustCompile(`(?s)<[^>]+>`)
	punctuationTrim   = ".,;:!?)]}>'\"\uFF0C\u3002\uFF01\uFF1F\u3001\uFF09\u3011\u300B"
)

type linkType string

const (
	linkPixiv   linkType = "pixiv"
	linkYande   linkType = "yande"
	linkTwitter linkType = "twitter"
)

type supportedLink struct {
	Type linkType
	ID   string
	URL  string
}

type ingestStats struct {
	FirstID    string
	Title      string
	Downloaded int
	Skipped    int
	Failed     int
}

func extractSupportedLinks(parts ...string) []supportedLink {
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		return nil
	}

	raw := urlPattern.FindAllString(text, -1)
	if len(raw) == 0 {
		return nil
	}

	links := make([]supportedLink, 0, len(raw))
	seen := map[string]struct{}{}

	for _, token := range raw {
		clean := strings.TrimRight(token, punctuationTrim)
		u, err := neturl.Parse(clean)
		if err != nil {
			continue
		}

		host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
		pathVal := strings.Trim(u.EscapedPath(), "/")
		segments := strings.Split(pathVal, "/")

		if host == "pixiv.net" {
			for i := 0; i+1 < len(segments); i++ {
				if segments[i] == "artworks" && pixivIDPattern.MatchString(segments[i+1]) {
					id := segments[i+1]
					key := string(linkPixiv) + ":" + id
					if _, ok := seen[key]; ok {
						break
					}
					seen[key] = struct{}{}
					links = append(links, supportedLink{Type: linkPixiv, ID: id, URL: clean})
					break
				}
			}
		}

		if host == "yande.re" && len(segments) >= 3 && segments[0] == "post" && segments[1] == "show" && yandeIDPattern.MatchString(segments[2]) {
			id := segments[2]
			key := string(linkYande) + ":" + id
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			links = append(links, supportedLink{Type: linkYande, ID: id, URL: clean})
		}

		if isTwitterHost(host) {
			username, id, ok := parseTwitterPath(segments)
			if !ok {
				continue
			}
			key := string(linkTwitter) + ":" + id
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			links = append(links, supportedLink{Type: linkTwitter, ID: id, URL: canonicalTwitterURL(username, id)})
		}
	}

	return links
}

func isTwitterHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	return host == "x.com" || host == "twitter.com" || host == "mobile.twitter.com"
}

func parseTwitterPath(parts []string) (username, id string, ok bool) {
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] != "status" || !twitterIDPattern.MatchString(parts[i+1]) {
			continue
		}
		id = parts[i+1]
		if i > 0 {
			username = normalizeTwitterUsername(parts[i-1])
		}
		return username, id, true
	}
	return "", "", false
}

func canonicalTwitterURL(username, tweetID string) string {
	username = normalizeTwitterUsername(username)
	if username == "" {
		return fmt.Sprintf("https://x.com/i/status/%s", tweetID)
	}
	return fmt.Sprintf("https://x.com/%s/status/%s", username, tweetID)
}

func normalizeTwitterUsername(username string) string {
	username = strings.TrimSpace(username)
	username = strings.TrimPrefix(username, "@")
	return username
}

func (a *App) handleTGLinks(ctx context.Context, links []supportedLink) (*TGIngestResult, error) {
	if len(links) > maxTGLinksPerMessage {
		links = links[:maxTGLinksPerMessage]
	}

	var (
		firstID, firstTitle, firstURL string
		successMsgs                   []string
		errorMsgs                     []string
	)

	for _, item := range links {
		switch item.Type {
		case linkPixiv:
			res, err := a.ingestPixivFromLink(ctx, item)
			if err != nil {
				errorMsgs = append(errorMsgs, fmt.Sprintf("Pixiv %s failed: %v", item.ID, err))
				continue
			}
			if firstID == "" && res.ID != "" {
				firstID, firstTitle, firstURL = res.ID, res.Title, res.SourceURL
			}
			successMsgs = append(successMsgs, res.Summary)
		case linkYande:
			res, err := a.ingestYandeFromLink(ctx, item)
			if err != nil {
				errorMsgs = append(errorMsgs, fmt.Sprintf("Yande %s failed: %v", item.ID, err))
				continue
			}
			if firstID == "" && res.ID != "" {
				firstID, firstTitle, firstURL = res.ID, res.Title, res.SourceURL
			}
			successMsgs = append(successMsgs, res.Summary)
		case linkTwitter:
			res, err := a.ingestTwitterFromLink(ctx, item)
			if err != nil {
				errorMsgs = append(errorMsgs, fmt.Sprintf("Twitter %s failed: %v", item.ID, err))
				continue
			}
			if firstID == "" && res.ID != "" {
				firstID, firstTitle, firstURL = res.ID, res.Title, res.SourceURL
			}
			successMsgs = append(successMsgs, res.Summary)
		}
	}

	if len(successMsgs) == 0 {
		if len(errorMsgs) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("all links failed: %s", strings.Join(errorMsgs, "; "))
	}

	summary := strings.Join(successMsgs, "\n")
	if len(errorMsgs) > 0 {
		summary += "\nPartial failures: " + strings.Join(errorMsgs, "; ")
	}

	return &TGIngestResult{
		ID:        firstID,
		Title:     firstTitle,
		SourceURL: firstURL,
		Summary:   summary,
	}, nil
}

func (a *App) ingestPixivFromLink(ctx context.Context, item supportedLink) (*TGIngestResult, error) {
	if a.Pixiv == nil || a.Cfg.PixivPHPSESSID == "" {
		return nil, fmt.Errorf("pixiv config is missing")
	}

	stats, err := a.ingestPixivArtwork(ctx, item.ID, item.URL)
	if err != nil {
		return nil, err
	}

	msg := fmt.Sprintf("Pixiv %s done: +%d, skipped %d, failed %d", item.ID, stats.Downloaded, stats.Skipped, stats.Failed)
	return &TGIngestResult{
		ID:        stats.FirstID,
		Title:     stats.Title,
		SourceURL: item.URL,
		Summary:   msg,
	}, nil
}

func (a *App) ingestYandeFromLink(ctx context.Context, item supportedLink) (*TGIngestResult, error) {
	posts, err := fetchYandeFamilyPosts(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, fmt.Errorf("yande post not found")
	}

	stats, err := a.ingestYandePosts(ctx, item, posts)
	if err != nil {
		return nil, err
	}

	msg := fmt.Sprintf("Yande %s done: +%d, skipped %d, failed %d", item.ID, stats.Downloaded, stats.Skipped, stats.Failed)
	return &TGIngestResult{
		ID:        stats.FirstID,
		Title:     stats.Title,
		SourceURL: item.URL,
		Summary:   msg,
	}, nil
}

type yandePreparedPost struct {
	Post         yandePost
	PID          string
	Data         []byte
	OriginID     string
	StorageMsgID int
	Width        int
	Height       int
}

func (a *App) ingestYandePosts(ctx context.Context, item supportedLink, posts []yandePost) (*ingestStats, error) {
	stats := &ingestStats{Title: "Yandex"}
	prepared := make([]yandePreparedPost, 0, len(posts))

	for _, post := range posts {
		pid := fmt.Sprintf("yande_%d", post.ID)
		if blocked, err := a.DB.IsBlocked(ctx, pid); err == nil && blocked {
			stats.Skipped++
			continue
		}
		if exists, _ := a.DB.Exists(ctx, pid); exists {
			stats.Skipped++
			continue
		}

		imgURL := post.bestImageURL()
		if imgURL == "" {
			stats.Failed++
			log.Printf("Yande image URL missing pid=%s", pid)
			continue
		}

		data, err := downloadWithHeaders(ctx, imgURL, "https://yande.re/")
		if err != nil {
			stats.Failed++
			log.Printf("Yande download failed pid=%s err=%v", pid, err)
			continue
		}

		originID, storageMsgID, err := a.TG.SendOriginDocumentWithFilename(ctx, data, buildYandeOriginFilename(post), "Original")
		if err != nil {
			stats.Failed++
			log.Printf("Yande origin send failed pid=%s err=%v", pid, err)
			continue
		}

		width, height := detectImageSize(data)
		prepared = append(prepared, yandePreparedPost{
			Post:         post,
			PID:          pid,
			Data:         data,
			OriginID:     originID,
			StorageMsgID: storageMsgID,
			Width:        width,
			Height:       height,
		})
		time.Sleep(1200 * time.Millisecond)
	}

	if len(prepared) == 0 {
		return stats, nil
	}

	groups := chunkYandePrepared(prepared, maxPixivAlbumGroup)
	for groupIdx, group := range groups {
		isLastGroup := groupIdx == len(groups)-1
		baseMeta := normalizePublishMeta(imagePublishMeta{
			Title:      "Yandex",
			ArtistName: "Arts",
			ArtistID:   "none",
			SourceURL:  item.URL,
			Source:     "yande",
			Tags:       strings.TrimSpace(group[0].Post.Tags),
			CreatedAt:  time.Now().Unix(),
		})

		groupCaption := ""
		if isLastGroup {
			groupCaption = buildPreviewCaption(baseMeta)
		}

		previewItems := make([]telegram.PreviewMedia, 0, len(group))
		for _, p := range group {
			previewItems = append(previewItems, telegram.PreviewMedia{
				Data:     p.Data,
				Filename: fmt.Sprintf("%s_preview.jpg", p.PID),
				Width:    p.Width,
				Height:   p.Height,
			})
		}

		previewResults, err := a.TG.SendPreviewMediaGroup(ctx, previewItems, groupCaption)
		if err != nil {
			log.Printf("Yande media group failed group=%d err=%v fallback=single_preview", groupIdx+1, err)
			fallbackGroup := make([]yandePreparedPost, 0, len(group))
			fallbackPreview := make([]telegram.PreviewSendResult, 0, len(group))
			for i, p := range group {
				caption := ""
				if i == 0 {
					caption = groupCaption
				}
				res, sendErr := a.TG.SendPreviewPhoto(ctx, p.Data, caption)
				if sendErr != nil {
					stats.Failed++
					log.Printf("Yande fallback preview failed pid=%s err=%v", p.PID, sendErr)
					continue
				}
				fallbackGroup = append(fallbackGroup, p)
				fallbackPreview = append(fallbackPreview, res)
			}
			group = fallbackGroup
			previewResults = fallbackPreview
		}

		if len(group) == 0 || len(previewResults) == 0 {
			continue
		}
		if len(group) != len(previewResults) {
			limit := len(group)
			if len(previewResults) < limit {
				limit = len(previewResults)
			}
			group = group[:limit]
			previewResults = previewResults[:limit]
		}

		discussionMsgID := 0
		if isLastGroup {
			anchorMeta := baseMeta
			anchorMeta.ID = group[0].PID
			originLinks := make([]discussionOriginLink, 0, len(group))
			for i, page := range group {
				originLinks = append(originLinks, discussionOriginLink{
					ImageID:      page.PID,
					OriginID:     page.OriginID,
					StorageMsgID: page.StorageMsgID,
					Label:        fmt.Sprintf("\u539f\u56fe%d", i+1),
				})
			}
			discussionMsgID = a.sendDiscussionCommentWithOrigins(ctx, anchorMeta, previewResults[0].PublishMsgID, originLinks)
		}

		for i, p := range group {
			meta := baseMeta
			meta.ID = p.PID
			meta.Tags = strings.TrimSpace(p.Post.Tags)
			meta.CreatedAt = time.Now().Unix()

			width := previewResults[i].Width
			height := previewResults[i].Height
			if width <= 0 {
				width = p.Width
			}
			if height <= 0 {
				height = p.Height
			}

			result := telegram.SendResult{
				PreviewID:    previewResults[i].PreviewID,
				OriginID:     p.OriginID,
				PublishMsgID: previewResults[i].PublishMsgID,
				StorageMsgID: p.StorageMsgID,
				Width:        width,
				Height:       height,
			}

			img, persistErr := a.persistPublishedImage(ctx, normalizePublishMeta(meta), result, discussionMsgID)
			if persistErr != nil {
				stats.Failed++
				log.Printf("Yande persist failed pid=%s err=%v", p.PID, persistErr)
				continue
			}
			stats.Downloaded++
			if stats.FirstID == "" {
				stats.FirstID = img.ID
			}
		}

		time.Sleep(1500 * time.Millisecond)
	}

	return stats, nil
}

func chunkYandePrepared(items []yandePreparedPost, size int) [][]yandePreparedPost {
	if len(items) == 0 {
		return nil
	}
	if size <= 0 {
		size = maxPixivAlbumGroup
	}
	out := make([][]yandePreparedPost, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		chunk := make([]yandePreparedPost, end-start)
		copy(chunk, items[start:end])
		out = append(out, chunk)
	}
	return out
}

func buildYandeOriginFilename(post yandePost) string {
	ext := ".jpg"
	raw := strings.TrimSpace(post.bestImageURL())
	if raw != "" {
		if u, err := neturl.Parse(raw); err == nil {
			candidate := strings.ToLower(path.Ext(u.Path))
			switch candidate {
			case ".jpg", ".jpeg", ".png", ".webp", ".gif":
				ext = candidate
			}
		}
	}
	return fmt.Sprintf("yande_%d%s", post.ID, ext)
}

func (a *App) ingestTwitterFromLink(ctx context.Context, item supportedLink) (*TGIngestResult, error) {
	stats, err := a.ingestTwitterTweet(ctx, item.ID, item.URL)
	if err != nil {
		return nil, err
	}

	msg := fmt.Sprintf("Twitter %s done: +%d, skipped %d, failed %d", item.ID, stats.Downloaded, stats.Skipped, stats.Failed)
	return &TGIngestResult{
		ID:        stats.FirstID,
		Title:     stats.Title,
		SourceURL: item.URL,
		Summary:   msg,
	}, nil
}

func (a *App) ingestTwitterTweet(ctx context.Context, tweetID, sourceURL string) (*ingestStats, error) {
	firstPageID := fmt.Sprintf("twitter_%s_p0", tweetID)
	if blocked, err := a.DB.IsBlocked(ctx, firstPageID); err == nil && blocked {
		return &ingestStats{Title: tweetID, Skipped: 1}, nil
	}
	if exists, _ := a.DB.Exists(ctx, firstPageID); exists {
		return &ingestStats{Title: tweetID, Skipped: 1}, nil
	}

	tweet, err := fetchTwitterTweet(ctx, a.Cfg.TwitterAPIDomain, tweetID)
	if err != nil {
		return nil, fmt.Errorf("twitter status: %w", err)
	}

	title := buildTwitterTitle(tweet.Text, tweetID, tweet.Author.Username)
	artistName := strings.TrimSpace(tweet.Author.Name)
	if artistName == "" {
		artistName = strings.TrimSpace(tweet.Author.Username)
	}
	if artistName == "" {
		artistName = "Arts"
	}

	artistID := normalizeTwitterUsername(tweet.Author.Username)
	if artistID == "" {
		artistID = "none"
	}

	if strings.TrimSpace(sourceURL) == "" {
		sourceURL = canonicalTwitterURL(artistID, tweetID)
	}

	photos := tweet.photoURLs()
	motions := tweet.motionMedia()
	if len(photos) == 0 && len(motions) == 0 {
		return nil, fmt.Errorf("tweet has no media")
	}
	log.Printf("Twitter tweet fetched id=%s photos=%d motions=%d", tweetID, len(photos), len(motions))

	tags := strings.Join(extractHashTags(tweet.Text), " ")
	baseMeta := imagePublishMeta{
		Title:      title,
		ArtistName: artistName,
		ArtistID:   artistID,
		SourceURL:  sourceURL,
		SourceText: tweet.Text,
		Source:     "twitter",
		Tags:       tags,
		CreatedAt:  time.Now().Unix(),
	}
	stats := &ingestStats{Title: title}

	for i, rawURL := range photos {
		pid := fmt.Sprintf("twitter_%s_p%d", tweetID, i)
		if blocked, err := a.DB.IsBlocked(ctx, pid); err == nil && blocked {
			stats.Skipped++
			log.Printf("Twitter skip page pid=%s reason=blocked", pid)
			continue
		}
		if exists, _ := a.DB.Exists(ctx, pid); exists {
			stats.Skipped++
			log.Printf("Twitter skip page pid=%s reason=already_exists", pid)
			continue
		}

		imgURL := buildTwitterImageURL(rawURL)
		imgData, err := downloadWithHeaders(ctx, imgURL, "https://x.com/")
		if err != nil {
			stats.Failed++
			log.Printf("Twitter download failed pid=%s err=%v", pid, err)
			continue
		}

		meta := baseMeta
		meta.ID = pid
		meta.CreatedAt = time.Now().Unix()

		img, err := a.publishImage(ctx, imgData, meta)
		if err != nil {
			stats.Failed++
			log.Printf("Twitter publish failed pid=%s err=%v", pid, err)
		} else {
			stats.Downloaded++
			if stats.FirstID == "" {
				stats.FirstID = pid
			}
			log.Printf("Twitter stored pid=%s size=%dx%d", pid, img.Width, img.Height)
		}

		time.Sleep(1500 * time.Millisecond)
	}

	for i, item := range motions {
		pid := fmt.Sprintf("twitter_%s_v%d", tweetID, i)
		mediaURL := strings.TrimSpace(item.URL)
		if mediaURL == "" {
			stats.Failed++
			log.Printf("Twitter media skip pid=%s reason=empty_url", pid)
			continue
		}

		mediaData, err := downloadWithHeaders(ctx, mediaURL, "https://x.com/")
		if err != nil {
			stats.Failed++
			log.Printf("Twitter media download failed pid=%s err=%v", pid, err)
			continue
		}

		filename := buildTwitterMediaFilename(tweetID, i, item)
		meta := baseMeta
		meta.ID = ""
		meta.CreatedAt = time.Now().Unix()
		if err := a.publishMotionNoDB(ctx, mediaData, filename, item.isAnimation(), meta); err != nil {
			stats.Failed++
			log.Printf("Twitter media publish failed pid=%s err=%v", pid, err)
		} else {
			stats.Downloaded++
			log.Printf("Twitter media published pid=%s animation=%t", pid, item.isAnimation())
		}

		time.Sleep(1500 * time.Millisecond)
	}

	return stats, nil
}

func (a *App) ingestPixivArtwork(ctx context.Context, id string, sourceURL string) (*ingestStats, error) {
	firstPageID := fmt.Sprintf("pixiv_%s_p0", id)
	if blocked, err := a.DB.IsBlocked(ctx, firstPageID); err == nil && blocked {
		return &ingestStats{Title: id, Skipped: 1}, nil
	}
	if exists, _ := a.DB.Exists(ctx, firstPageID); exists {
		return &ingestStats{Title: id, Skipped: 1}, nil
	}

	detail, err := a.Pixiv.FetchDetail(id)
	if err != nil {
		return nil, fmt.Errorf("pixiv detail: %w", err)
	}
	if detail.Body.IllustType == 2 {
		return nil, fmt.Errorf("ugoira is not supported")
	}

	tags := make([]string, 0, len(detail.Body.Tags.Tags))
	for _, t := range detail.Body.Tags.Tags {
		tags = append(tags, t.Tag)
	}

	pages, err := a.Pixiv.FetchPages(id)
	if err != nil {
		return nil, fmt.Errorf("pixiv pages: %w", err)
	}

	if sourceURL == "" {
		sourceURL = fmt.Sprintf("https://www.pixiv.net/artworks/%s", id)
	}

	baseMeta := imagePublishMeta{
		Title:      detail.Body.Title,
		ArtistName: detail.Body.UserName,
		ArtistID:   detail.Body.UserID,
		SourceURL:  sourceURL,
		SourceText: pixivDescriptionToText(detail.Body.Description),
		Source:     "pixiv",
		Tags:       strings.Join(tags, " "),
	}

	stats := &ingestStats{Title: detail.Body.Title}
	candidates := make([]pixivPageCandidate, 0, len(pages))
	for i, p := range pages {
		pid := fmt.Sprintf("pixiv_%s_p%d", id, i)
		if blocked, err := a.DB.IsBlocked(ctx, pid); err == nil && blocked {
			stats.Skipped++
			log.Printf("Pixiv skip page pid=%s reason=blocked", pid)
			continue
		}
		if exists, _ := a.DB.Exists(ctx, pid); exists {
			stats.Skipped++
			log.Printf("Pixiv skip page pid=%s reason=already_exists", pid)
			continue
		}
		candidates = append(candidates, pixivPageCandidate{
			PageIndex: i,
			PID:       pid,
			URL:       p.URL,
			Width:     p.Width,
			Height:    p.Height,
		})
	}

	if len(candidates) == 0 {
		return stats, nil
	}

	if len(candidates) > 1 {
		a.ingestPixivAlbumCandidates(ctx, id, candidates, baseMeta, stats)
		return stats, nil
	}

	for _, c := range candidates {
		imgData, err := a.Pixiv.Download(c.URL)
		if err != nil {
			stats.Failed++
			log.Printf("Pixiv download failed pid=%s err=%v", c.PID, err)
			continue
		}

		meta := baseMeta
		meta.ID = c.PID
		meta.CreatedAt = time.Now().Unix()

		img, err := a.publishImage(ctx, imgData, meta)
		if err != nil {
			stats.Failed++
			log.Printf("Pixiv publish failed pid=%s err=%v", c.PID, err)
		} else {
			stats.Downloaded++
			if stats.FirstID == "" {
				stats.FirstID = img.ID
			}
			log.Printf("Pixiv stored pid=%s size=%dx%d", c.PID, img.Width, img.Height)
		}

		time.Sleep(2 * time.Second)
	}

	return stats, nil
}

type yandePost struct {
	ID          int    `json:"id"`
	ParentID    *int   `json:"parent_id"`
	HasChildren bool   `json:"has_children"`
	FileURL     string `json:"file_url"`
	JPEGURL     string `json:"jpeg_url"`
	PNGURL      string `json:"png_url"`
	SampleURL   string `json:"sample_url"`
	Tags        string `json:"tags"`
}

func (p yandePost) bestImageURL() string {
	candidates := []string{p.FileURL, p.JPEGURL, p.PNGURL, p.SampleURL}
	for _, u := range candidates {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if strings.HasPrefix(u, "//") {
			return "https:" + u
		}
		return u
	}
	return ""
}

type twitterStatusResp struct {
	Tweet   *twitterTweet `json:"tweet"`
	Message string        `json:"message"`
	Code    int           `json:"code"`
}

type twitterTweet struct {
	ID     string        `json:"id"`
	Text   string        `json:"text"`
	Author twitterAuthor `json:"author"`
	Media  *twitterMedia `json:"media"`
}

type twitterAuthor struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"screen_name"`
}

type twitterMedia struct {
	Photos []twitterMediaItem `json:"photos"`
	Videos []twitterMediaItem `json:"videos"`
	All    []twitterMediaItem `json:"all"`
}

type twitterMediaItem struct {
	Type         string `json:"type"`
	URL          string `json:"url"`
	Format       string `json:"format,omitempty"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
}

func (t *twitterTweet) photoURLs() []string {
	if t == nil || t.Media == nil {
		return nil
	}
	items := make([]twitterMediaItem, 0, len(t.Media.Photos)+len(t.Media.All))
	items = append(items, t.Media.Photos...)
	items = append(items, t.Media.All...)

	return collectTwitterMediaURLs(items, func(item twitterMediaItem) bool {
		mediaType := strings.ToLower(strings.TrimSpace(item.Type))
		return mediaType == "" || mediaType == "photo"
	})
}

func (t *twitterTweet) motionMedia() []twitterMediaItem {
	if t == nil || t.Media == nil {
		return nil
	}
	items := make([]twitterMediaItem, 0, len(t.Media.Videos)+len(t.Media.All))
	items = append(items, t.Media.Videos...)
	items = append(items, t.Media.All...)

	out := make([]twitterMediaItem, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if !isTwitterMotionType(item.Type, item.Format, item.URL) {
			continue
		}
		u := strings.TrimSpace(item.URL)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		item.URL = u
		out = append(out, item)
	}
	return out
}

func (m twitterMediaItem) isAnimation() bool {
	mediaType := strings.ToLower(strings.TrimSpace(m.Type))
	if mediaType == "gif" || mediaType == "animated_gif" {
		return true
	}
	format := strings.ToLower(strings.TrimSpace(m.Format))
	return strings.Contains(format, "gif")
}

func collectTwitterMediaURLs(items []twitterMediaItem, allow func(twitterMediaItem) bool) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if allow != nil && !allow(item) {
			continue
		}
		u := strings.TrimSpace(item.URL)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func isTwitterMotionType(mediaType, format, rawURL string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	switch mediaType {
	case "video", "gif", "animated_gif":
		return true
	case "photo":
		return false
	}

	format = strings.ToLower(strings.TrimSpace(format))
	if strings.Contains(format, "video") || strings.Contains(format, "gif") || strings.Contains(format, "mp4") || strings.Contains(format, "webm") {
		return true
	}

	u, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".mp4", ".mov", ".m4v", ".webm", ".gif":
		return true
	default:
		return false
	}
}

func buildTwitterMediaFilename(tweetID string, index int, item twitterMediaItem) string {
	ext := ""
	raw := strings.TrimSpace(item.URL)
	if raw != "" {
		if u, err := neturl.Parse(raw); err == nil {
			candidate := strings.ToLower(path.Ext(u.Path))
			if isSupportedTwitterMediaExt(candidate) {
				ext = candidate
			}
		}
	}
	if ext == "" {
		format := strings.ToLower(strings.TrimSpace(item.Format))
		switch {
		case strings.Contains(format, "webm"):
			ext = ".webm"
		case strings.Contains(format, "mov"):
			ext = ".mov"
		case strings.Contains(format, "gif"):
			ext = ".mp4"
		default:
			ext = ".mp4"
		}
	}
	return fmt.Sprintf("twitter_%s_v%d%s", tweetID, index, ext)
}

func isSupportedTwitterMediaExt(ext string) bool {
	switch ext {
	case ".mp4", ".mov", ".m4v", ".webm", ".gif":
		return true
	default:
		return false
	}
}

func fetchTwitterTweet(ctx context.Context, domain, tweetID string) (*twitterTweet, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = defaultTwitterAPIDomain
	}

	endpoint := fmt.Sprintf("https://api.%s/_/status/%s", domain, tweetID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("twitter status %d", resp.StatusCode)
	}

	var payload twitterStatusResp
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Code != 0 && payload.Code != 200 {
		msg := strings.TrimSpace(payload.Message)
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("twitter api code %d: %s", payload.Code, msg)
	}
	if payload.Tweet == nil {
		return nil, fmt.Errorf("tweet not found")
	}
	return payload.Tweet, nil
}

func buildTwitterTitle(text, tweetID, username string) string {
	text = strings.TrimSpace(text)
	if text != "" {
		first := strings.TrimSpace(strings.Split(text, "\n")[0])
		if first != "" {
			return truncateRunes(first, 120)
		}
	}
	username = normalizeTwitterUsername(username)
	if username != "" {
		return fmt.Sprintf("%s/%s", username, tweetID)
	}
	return "Twitter/" + tweetID
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	r := []rune(strings.TrimSpace(s))
	if len(r) <= limit {
		return string(r)
	}
	return string(r[:limit])
}

func pixivDescriptionToText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	text := pixivBRTagPattern.ReplaceAllString(raw, "\n")
	text = htmlTagPattern.ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if blank {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}

	return strings.TrimSpace(strings.Join(out, "\n"))
}

func extractHashTags(text string) []string {
	text = strings.ReplaceAll(text, "\uFF03", "#")
	matches := hashtagPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		tag := strings.TrimSpace(m[1])
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func buildTwitterImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	u, err := neturl.Parse(raw)
	if err != nil {
		return raw
	}
	if !strings.Contains(strings.ToLower(u.Hostname()), "twimg.com") {
		return raw
	}

	q := u.Query()
	q.Set("name", "orig")
	if q.Get("format") == "" {
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(u.Path)), ".")
		if ext != "" {
			q.Set("format", ext)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func fetchYandePosts(ctx context.Context, tags string) ([]yandePost, error) {
	tags = strings.TrimSpace(tags)
	if tags == "" {
		return nil, fmt.Errorf("yande tags is empty")
	}

	endpoint := fmt.Sprintf("https://yande.re/post.json?tags=%s", neturl.QueryEscape(tags))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://yande.re/")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("yande status %d", resp.StatusCode)
	}

	var arr []yandePost
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		return nil, err
	}
	return arr, nil
}

func fetchYandePost(ctx context.Context, id string) (*yandePost, error) {
	arr, err := fetchYandePosts(ctx, fmt.Sprintf("id:%s", strings.TrimSpace(id)))
	if err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("yande post not found")
	}
	return &arr[0], nil
}

func fetchYandeFamilyPosts(ctx context.Context, id string) ([]yandePost, error) {
	seed, err := fetchYandePost(ctx, id)
	if err != nil {
		return nil, err
	}

	rootID := seed.ID
	if seed.ParentID != nil && *seed.ParentID > 0 {
		rootID = *seed.ParentID
	}

	family, err := fetchYandePosts(ctx, fmt.Sprintf("parent:%d", rootID))
	if err != nil {
		return []yandePost{*seed}, nil
	}
	if len(family) == 0 {
		return []yandePost{*seed}, nil
	}

	merged := make(map[int]yandePost, len(family)+1)
	for _, post := range family {
		merged[post.ID] = post
	}
	if _, ok := merged[seed.ID]; !ok {
		merged[seed.ID] = *seed
	}

	out := make([]yandePost, 0, len(merged))
	for _, post := range merged {
		out = append(out, post)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func downloadWithHeaders(ctx context.Context, sourceURL, referer string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
