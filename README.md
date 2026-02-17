# Xin Gallery Puls - Pixiv + Telegram + D1 Gallery

Xin Gallery Puls is a Go-based ACG gallery backend.

It supports ingest from:
- Pixiv bookmarks crawler
- Telegram direct photo/document
- Telegram links (Pixiv / yande.re / X/Twitter)

It stores:
- Preview post in **Publish Channel (A)**
- Origin file in **Storage Channel (B)**
- Metadata in **Cloudflare D1**
- Optional backup to WebDAV/OpenList

---

## v2 Core Model (A/B + discussion)

- `PUBLISH_CHANNEL_ID` (A): gallery preview posts
- `STORAGE_CHANNEL_ID` (B): origin file storage (document)
- `DISCUSSION_GROUP_ID` (optional): linked discussion group for A

Flow per image:
1. Send preview photo to A (caption)
2. Send origin document to B
3. If discussion group configured, post comment in A's thread:
   - origin link (to B message)
   - source link
4. Write record into D1

Twitter link ingest caption style:
- Header: `title(source_url) / artist`
- Blockquote 1: tweet text
- Blockquote 2: hashtags (if any)

---

## Project Layout

- `cmd/server/main.go`: startup, HTTP, Telegram bot
- `internal/config/config.go`: env loading
- `internal/app/`: ingest/crawler logic
- `internal/telegram/telegram.go`: Telegram send/download
- `internal/database/d1.go`: D1 schema + queries
- `internal/web/server.go`: HTTP pages and APIs
- `schema.sql`: canonical D1 schema
- `web/`: frontend static pages

---

## Environment Variables

### Required

- `BOT_TOKEN`
- `PUBLISH_CHANNEL_ID`
- `STORAGE_CHANNEL_ID`
- `CLOUDFLARE_ACCOUNT_ID`
- `CLOUDFLARE_API_TOKEN`
- `D1_DATABASE_ID`
- `ADMIN_PASSWORD`

### Optional / compatibility

- `CHANNEL_ID`
  - backward compatibility fallback
  - if `PUBLISH_CHANNEL_ID` or `STORAGE_CHANNEL_ID` is empty, fallback to `CHANNEL_ID`
- `DISCUSSION_GROUP_ID`
  - linked group for channel comments

### Pixiv

- `PIXIV_PHPSESSID`
- `PIXIV_USER_ID`
- `PIXIV_TAG` (optional, empty = all)
- `PIXIV_REST` (`show` or `hide`, default `show`)
- `PIXIV_CRAWL_ORDER` (`desc` or `asc`, default `desc`)
- `PIXIV_LIMIT` (default `40`)
- `PIXIV_MAX_PAGES` (legacy fallback, default `0` = unlimited)
- `PIXIV_BOOTSTRAP_MAX_PAGES` (default `-1` -> fallback to `PIXIV_MAX_PAGES`)
- `PIXIV_INCREMENTAL_MAX_PAGES` (default `2`)
- `PIXIV_INTERVAL_MINUTES` (default `120`)

### Other integrations

- `TWITTER_API_DOMAIN` (default `fxtwitter.com`)
- `TWITTER_AUTHOR_ENABLED` (`true/false`, default `false`)
- `TWITTER_AUTHOR_USERS` (comma-separated usernames, e.g. `LIMU838,kasuga_iz`)
- `TWITTER_RSS_SOURCES` (semicolon-separated templates, supports `{user}`)
  - example: `https://rsshubi.zeabur.app/twitter/user/{user}/includeRts=0&includeReplies=0&count=20`
- `TWITTER_AUTHOR_INTERVAL_MINUTES` (default `60`)
- `TWITTER_AUTHOR_FETCH_LIMIT` (default `20`)
- `UMAMI_BASE_URL`
- `UMAMI_WEBSITE_ID_FRONTEND`
- `UMAMI_USERNAME`
- `UMAMI_PASSWORD`
- `UMAMI_API_TOKEN`
- `UMAMI_LOOKBACK_DAYS` (default `7`)

### Backup (optional)

- `BACKUP_ENABLED` (`true/false`, default `false`)
- `BACKUP_WEBDAV_URL`
- `BACKUP_WEBDAV_USERNAME`
- `BACKUP_WEBDAV_PASSWORD`
- `BACKUP_BASE_PATH` (default `/MyPixiv`)
- `BACKUP_WORKERS` (default `1`)
- `BACKUP_RETRY_MAX` (default `5`)
- `BACKUP_POLL_SECONDS` (default `8`)
- `BACKUP_TASK_TIMEOUT_SECONDS` (default `120`)

### Server

- `LISTEN_ADDR` (default `:8080`)

---

## D1 Schema

Use `schema.sql`.

`images` now includes extra v2 fields:
- `source_text`
- `publish_channel_id`, `publish_message_id`
- `storage_channel_id`, `storage_message_id`
- `discussion_group_id`, `discussion_message_id`

Backend `EnsureSchema` can auto-add missing columns for upgrade.

---

## Fresh Deploy (from zero)

## 1) Telegram setup

1. Create channel A (publish).
2. Create channel B (storage).
3. (Optional) Create/choose discussion group and link to channel A.
4. Add bot as admin in A and B.
5. If using discussion comments, ensure bot can send messages in the linked group.

## 2) D1 setup

1. Create a new D1 database.
2. Run `schema.sql` (or let backend auto-create on first start).

## 3) Zeabur env setup

At minimum set:

- `BOT_TOKEN`
- `PUBLISH_CHANNEL_ID`
- `STORAGE_CHANNEL_ID`
- `DISCUSSION_GROUP_ID` (optional)
- `CLOUDFLARE_ACCOUNT_ID`
- `CLOUDFLARE_API_TOKEN`
- `D1_DATABASE_ID`
- `ADMIN_PASSWORD`

## 4) Build and deploy

- Build from Dockerfile and deploy to Zeabur.
- Confirm logs contain `HTTP server listening on :8080`.

## 5) Quick verification

1. Send a test image to bot:
   - A gets preview
   - B gets origin
   - D1 has one row
2. Send a Twitter link to bot:
   - caption includes title/source/artist + tweet quote + hashtag quote
   - discussion comment contains origin link + source link
3. Open:
   - `/gallery`
   - `/admin/upload` (Basic Auth)

---

## HTTP Routes

Pages:
- `GET /` -> redirect `/gallery`
- `GET /gallery`
- `GET /favorites`
- `GET /admin/upload` (Basic Auth)

Public APIs:
- `GET /api/posts?type=all|h|v&offset=0&limit=20`
- `GET /api/favorites?type=all|h|v&offset=0&limit=20`
- `GET /api/random`
- `GET /api/random?type=h`
- `GET /api/random?type=v`
- `GET /api/random?format=url`
- `GET /api/random?format=redirect`
- `GET /image/{file_id}`

Admin APIs:
- `GET /admin/api/images`
- `GET /admin/api/images/count`
- `POST /admin/api/images/hide`
- `POST /admin/api/images/favorite`
- `GET /admin/api/umami/summary`
- `GET /admin/api/backup/health`
- `GET /admin/api/backup/stats`
- `POST /admin/api/backup/backfill`
- `POST /admin/api/backup/retry-failed`
- `GET /admin/api/backup/failed`
- `POST /admin/api/backup/resolve`

---

## Notes

- Random image API returns preview only by design.
- `/image/{file_id}` is long-cache friendly (`max-age=31536000, immutable`).
- Keep only one bot instance to avoid Telegram `getUpdates 409 conflict`.
- Twitter author crawler uses RSS source + existing Twitter single-link ingest flow; state key format: `twitter_author_last_<username>`.
- If token/password leaked, rotate immediately.
