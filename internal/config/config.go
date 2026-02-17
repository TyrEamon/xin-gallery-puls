package config

import (
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BotToken                 string
	ChannelID                int64
	PublishChannelID         int64
	StorageChannelID         int64
	DiscussionGroupID        int64
	AdminPassword            string
	BotUsername              string
	OriginLinkSecret         string
	OriginLinkTTLSeconds     int
	TGAllowedUserIDs         map[int64]struct{}
	PreviewHasSpoiler        bool
	BackupEnabled            bool
	BackupWebDAVURL          string
	BackupWebDAVUsername     string
	BackupWebDAVPassword     string
	BackupBasePath           string
	BackupWorkers            int
	BackupRetryMax           int
	BackupPollSeconds        int
	BackupTaskTimeoutSeconds int
	TwitterAPIDomain         string
	TwitterAuthorEnabled     bool
	TwitterAuthorUsers       []string
	TwitterRSSSources        []string
	TwitterAuthorIntervalMin int
	TwitterAuthorFetchLimit  int
	UmamiBaseURL             string
	UmamiWebsiteIDFrontend   string
	UmamiUsername            string
	UmamiPassword            string
	UmamiAPIToken            string
	UmamiLookbackDays        int
	PixivPHPSESSID           string
	PixivUserID              string
	PixivTag                 string
	PixivRest                string
	PixivCrawlOrder          string
	PixivLimit               int
	PixivMaxPages            int
	PixivBootstrapMaxPages   int
	PixivIncrementalMaxPages int
	PixivIntervalMinutes     int
	D1AccountID              string
	D1ApiToken               string
	D1DatabaseID             string
	ListenAddr               string
}

func Load() *Config {
	cfg := &Config{
		BotToken:                 os.Getenv("BOT_TOKEN"),
		AdminPassword:            os.Getenv("ADMIN_PASSWORD"),
		BotUsername:              strings.TrimPrefix(getEnvString("BOT_USERNAME", ""), "@"),
		OriginLinkSecret:         os.Getenv("ORIGIN_LINK_SECRET"),
		OriginLinkTTLSeconds:     getEnvInt("ORIGIN_LINK_TTL_SECONDS", 604800),
		TGAllowedUserIDs:         parseIDSet(os.Getenv("TG_ALLOWED_USER_IDS")),
		PreviewHasSpoiler:        getEnvBool("TG_PREVIEW_HAS_SPOILER", false),
		BackupEnabled:            getEnvBool("BACKUP_ENABLED", false),
		BackupWebDAVURL:          getEnvString("BACKUP_WEBDAV_URL", ""),
		BackupWebDAVUsername:     os.Getenv("BACKUP_WEBDAV_USERNAME"),
		BackupWebDAVPassword:     os.Getenv("BACKUP_WEBDAV_PASSWORD"),
		BackupBasePath:           getEnvString("BACKUP_BASE_PATH", "/MyPixiv"),
		BackupWorkers:            getEnvInt("BACKUP_WORKERS", 1),
		BackupRetryMax:           getEnvInt("BACKUP_RETRY_MAX", 5),
		BackupPollSeconds:        getEnvInt("BACKUP_POLL_SECONDS", 8),
		BackupTaskTimeoutSeconds: getEnvInt("BACKUP_TASK_TIMEOUT_SECONDS", 120),
		TwitterAPIDomain:         getEnvString("TWITTER_API_DOMAIN", "fxtwitter.com"),
		TwitterAuthorEnabled:     getEnvBool("TWITTER_AUTHOR_ENABLED", false),
		TwitterAuthorUsers:       parseStringList(os.Getenv("TWITTER_AUTHOR_USERS"), ","),
		TwitterRSSSources:        parseStringList(os.Getenv("TWITTER_RSS_SOURCES"), ";"),
		TwitterAuthorIntervalMin: getEnvInt("TWITTER_AUTHOR_INTERVAL_MINUTES", 60),
		TwitterAuthorFetchLimit:  getEnvInt("TWITTER_AUTHOR_FETCH_LIMIT", 20),
		UmamiBaseURL:             getEnvString("UMAMI_BASE_URL", ""),
		UmamiWebsiteIDFrontend:   os.Getenv("UMAMI_WEBSITE_ID_FRONTEND"),
		UmamiUsername:            os.Getenv("UMAMI_USERNAME"),
		UmamiPassword:            os.Getenv("UMAMI_PASSWORD"),
		UmamiAPIToken:            os.Getenv("UMAMI_API_TOKEN"),
		UmamiLookbackDays:        getEnvInt("UMAMI_LOOKBACK_DAYS", 7),
		PixivPHPSESSID:           os.Getenv("PIXIV_PHPSESSID"),
		PixivUserID:              os.Getenv("PIXIV_USER_ID"),
		PixivTag:                 os.Getenv("PIXIV_TAG"),
		PixivRest:                getEnvString("PIXIV_REST", "show"),
		PixivCrawlOrder:          getEnvString("PIXIV_CRAWL_ORDER", "desc"),
		PixivLimit:               getEnvInt("PIXIV_LIMIT", 40),
		PixivMaxPages:            getEnvInt("PIXIV_MAX_PAGES", 0),
		PixivBootstrapMaxPages:   getEnvInt("PIXIV_BOOTSTRAP_MAX_PAGES", -1),
		PixivIncrementalMaxPages: getEnvInt("PIXIV_INCREMENTAL_MAX_PAGES", 2),
		PixivIntervalMinutes:     getEnvInt("PIXIV_INTERVAL_MINUTES", 120),
		D1AccountID:              os.Getenv("CLOUDFLARE_ACCOUNT_ID"),
		D1ApiToken:               os.Getenv("CLOUDFLARE_API_TOKEN"),
		D1DatabaseID:             os.Getenv("D1_DATABASE_ID"),
		ListenAddr:               getEnvString("LISTEN_ADDR", ":8080"),
	}

	channelStr := os.Getenv("CHANNEL_ID")
	if channelStr != "" {
		if id, err := strconv.ParseInt(channelStr, 10, 64); err == nil {
			cfg.ChannelID = id
		} else {
			log.Printf("invalid CHANNEL_ID: %v", err)
		}
	}

	publishStr := os.Getenv("PUBLISH_CHANNEL_ID")
	if publishStr != "" {
		if id, err := strconv.ParseInt(publishStr, 10, 64); err == nil {
			cfg.PublishChannelID = id
		} else {
			log.Printf("invalid PUBLISH_CHANNEL_ID: %v", err)
		}
	}

	storageStr := os.Getenv("STORAGE_CHANNEL_ID")
	if storageStr != "" {
		if id, err := strconv.ParseInt(storageStr, 10, 64); err == nil {
			cfg.StorageChannelID = id
		} else {
			log.Printf("invalid STORAGE_CHANNEL_ID: %v", err)
		}
	}

	groupStr := os.Getenv("DISCUSSION_GROUP_ID")
	if groupStr != "" {
		if id, err := strconv.ParseInt(groupStr, 10, 64); err == nil {
			cfg.DiscussionGroupID = id
		} else {
			log.Printf("invalid DISCUSSION_GROUP_ID: %v", err)
		}
	}

	// Backward compatibility: if new split-channel vars are not set, keep old single-channel behavior.
	if cfg.PublishChannelID == 0 {
		cfg.PublishChannelID = cfg.ChannelID
	}
	if cfg.StorageChannelID == 0 {
		cfg.StorageChannelID = cfg.ChannelID
	}
	if cfg.ChannelID == 0 {
		cfg.ChannelID = cfg.PublishChannelID
	}

	if cfg.OriginLinkTTLSeconds < 0 {
		cfg.OriginLinkTTLSeconds = 604800
	}

	if cfg.TwitterAuthorIntervalMin <= 0 {
		cfg.TwitterAuthorIntervalMin = 60
	}
	if cfg.TwitterAuthorFetchLimit <= 0 {
		cfg.TwitterAuthorFetchLimit = 20
	}
	if len(cfg.TwitterAuthorUsers) == 0 || len(cfg.TwitterRSSSources) == 0 {
		cfg.TwitterAuthorEnabled = false
	}

	return cfg
}

func (c *Config) IsTGUserAllowed(userID int64) bool {
	if len(c.TGAllowedUserIDs) == 0 {
		return true
	}
	_, ok := c.TGAllowedUserIDs[userID]
	return ok
}

func parseIDSet(raw string) map[int64]struct{} {
	out := make(map[int64]struct{})
	for _, part := range strings.Split(raw, ",") {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			log.Printf("invalid TG_ALLOWED_USER_IDS item %q: %v", v, err)
			continue
		}
		out[id] = struct{}{}
	}
	return out
}

func parseStringList(raw, sep string) []string {
	parts := strings.Split(raw, sep)
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	return out
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
