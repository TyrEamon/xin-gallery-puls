package app

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	neturl "net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const twitterAuthorStatePrefix = "twitter_author_last_"

type twitterRSSFeed struct {
	Channel struct {
		Items []twitterRSSItem `xml:"item"`
	} `xml:"channel"`
}

type twitterRSSItem struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
	GUID  string `xml:"guid"`
}

type twitterAuthorCandidate struct {
	Link   supportedLink
	ID     int64
	Source string
}

func (a *App) StartTwitterAuthorCrawler(ctx context.Context) {
	if !a.Cfg.TwitterAuthorEnabled {
		log.Println("Twitter author crawler disabled")
		return
	}

	if len(a.Cfg.TwitterAuthorUsers) == 0 || len(a.Cfg.TwitterRSSSources) == 0 {
		log.Println("Twitter author crawler disabled (missing TWITTER_AUTHOR_USERS or TWITTER_RSS_SOURCES)")
		return
	}

	go func() {
		a.crawlTwitterAuthorsOnce(ctx)

		interval := time.Duration(a.Cfg.TwitterAuthorIntervalMin) * time.Minute
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.crawlTwitterAuthorsOnce(ctx)
			}
		}
	}()
}

func (a *App) crawlTwitterAuthorsOnce(ctx context.Context) {
	log.Printf("Twitter author crawl started (users=%d, sources=%d, interval_min=%d, fetch_limit=%d)", len(a.Cfg.TwitterAuthorUsers), len(a.Cfg.TwitterRSSSources), a.Cfg.TwitterAuthorIntervalMin, a.Cfg.TwitterAuthorFetchLimit)

	for _, rawUser := range a.Cfg.TwitterAuthorUsers {
		if ctx.Err() != nil {
			return
		}

		user := normalizeTwitterUsername(rawUser)
		if user == "" {
			continue
		}

		if err := a.crawlTwitterAuthorUser(ctx, user); err != nil {
			log.Printf("Twitter author crawl failed user=%s err=%v", user, err)
		}
		time.Sleep(1500 * time.Millisecond)
	}

	log.Println("Twitter author crawl finished")
}

func (a *App) crawlTwitterAuthorUser(ctx context.Context, user string) error {
	stateKey := twitterAuthorStatePrefix + strings.ToLower(user)
	lastValue, ok, err := a.DB.GetCrawlerState(ctx, stateKey)
	if err != nil {
		return fmt.Errorf("get crawler state: %w", err)
	}
	lastID, _ := strconv.ParseInt(strings.TrimSpace(lastValue), 10, 64)
	if !ok {
		lastID = 0
	}

	links, rssURL, err := a.fetchTwitterAuthorLinks(ctx, user)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		log.Printf("Twitter author fetched user=%s source=%s links=0", user, rssURL)
		return nil
	}

	candidates := make([]twitterAuthorCandidate, 0, len(links))
	for _, link := range links {
		idNum, parseErr := strconv.ParseInt(link.ID, 10, 64)
		if parseErr != nil || idNum <= 0 {
			continue
		}
		if lastID > 0 && idNum <= lastID {
			continue
		}
		candidates = append(candidates, twitterAuthorCandidate{Link: link, ID: idNum, Source: rssURL})
	}

	if len(candidates) == 0 {
		log.Printf("Twitter author user=%s no new tweets (last=%d, fetched=%d)", user, lastID, len(links))
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ID < candidates[j].ID
	})

	if limit := a.Cfg.TwitterAuthorFetchLimit; limit > 0 && len(candidates) > limit {
		candidates = candidates[len(candidates)-limit:]
	}

	processed := 0
	success := 0
	highestSuccessID := lastID
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return nil
		}

		processed++
		if _, err := a.ingestTwitterFromLink(ctx, candidate.Link); err != nil {
			log.Printf("Twitter author ingest failed user=%s tweet=%s err=%v", user, candidate.Link.ID, err)
			continue
		}

		success++
		if candidate.ID > highestSuccessID {
			highestSuccessID = candidate.ID
		}
		time.Sleep(1200 * time.Millisecond)
	}

	if highestSuccessID > lastID {
		if err := a.DB.SetCrawlerState(ctx, stateKey, strconv.FormatInt(highestSuccessID, 10)); err != nil {
			log.Printf("Twitter author state update failed user=%s key=%s err=%v", user, stateKey, err)
		} else {
			log.Printf("Twitter author state updated user=%s key=%s value=%d", user, stateKey, highestSuccessID)
		}
	}

	log.Printf("Twitter author user=%s done source=%s fetched=%d new=%d processed=%d success=%d", user, rssURL, len(links), len(candidates), processed, success)
	return nil
}

func (a *App) fetchTwitterAuthorLinks(ctx context.Context, user string) ([]supportedLink, string, error) {
	var errs []string
	for _, source := range a.Cfg.TwitterRSSSources {
		feedURL := buildTwitterRSSURL(source, user)
		items, err := fetchTwitterRSSItems(ctx, feedURL)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", feedURL, err))
			continue
		}

		links := extractTwitterLinksFromRSSItems(items)
		if len(links) == 0 {
			errs = append(errs, fmt.Sprintf("%s: no twitter links", feedURL))
			continue
		}

		return links, feedURL, nil
	}

	if len(errs) == 0 {
		return nil, "", fmt.Errorf("no rss sources configured")
	}
	return nil, "", fmt.Errorf("all rss sources failed: %s", strings.Join(errs, " | "))
}

func buildTwitterRSSURL(template, user string) string {
	t := strings.TrimSpace(template)
	u := neturl.PathEscape(normalizeTwitterUsername(user))
	if strings.Contains(t, "{user}") {
		return strings.ReplaceAll(t, "{user}", u)
	}
	if strings.Contains(t, "%s") {
		return fmt.Sprintf(t, u)
	}
	return strings.TrimRight(t, "/") + "/" + u
}

func fetchTwitterRSSItems(ctx context.Context, feedURL string) ([]twitterRSSItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "xin-gallery-puls/1.0")

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var feed twitterRSSFeed
	if err := xml.Unmarshal(data, &feed); err != nil {
		return nil, err
	}
	return feed.Channel.Items, nil
}

func extractTwitterLinksFromRSSItems(items []twitterRSSItem) []supportedLink {
	out := make([]supportedLink, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		combined := strings.TrimSpace(strings.Join([]string{item.Link, item.GUID}, "\n"))
		if combined == "" {
			continue
		}
		links := extractSupportedLinks(combined)
		for _, link := range links {
			if link.Type != linkTwitter {
				continue
			}
			if _, ok := seen[link.ID]; ok {
				continue
			}
			seen[link.ID] = struct{}{}
			out = append(out, link)
		}
	}
	return out
}
