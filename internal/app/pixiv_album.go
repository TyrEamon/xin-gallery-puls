package app

import (
	"context"
	"fmt"
	"log"
	"time"

	"pixiv-tg-gallery/internal/telegram"
)

type pixivPageCandidate struct {
	PageIndex int
	PID       string
	URL       string
	Width     int
	Height    int
}

type pixivPreparedPage struct {
	Candidate    pixivPageCandidate
	Data         []byte
	OriginID     string
	StorageMsgID int
}

func chunkPixivCandidates(items []pixivPageCandidate, size int) [][]pixivPageCandidate {
	if size <= 0 {
		size = maxPixivAlbumGroup
	}
	if len(items) == 0 {
		return nil
	}
	out := make([][]pixivPageCandidate, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		chunk := make([]pixivPageCandidate, end-start)
		copy(chunk, items[start:end])
		out = append(out, chunk)
	}
	return out
}

func (a *App) ingestPixivAlbumCandidates(ctx context.Context, artworkID string, candidates []pixivPageCandidate, baseMeta imagePublishMeta, stats *ingestStats) error {
	groups := chunkPixivCandidates(candidates, maxPixivAlbumGroup)
	for groupIdx, group := range groups {
		prepared := make([]pixivPreparedPage, 0, len(group))
		for _, c := range group {
			imgData, err := a.Pixiv.Download(c.URL)
			if err != nil {
				stats.Failed++
				log.Printf("Pixiv download failed pid=%s err=%v", c.PID, err)
				continue
			}

			originID, storageMsgID, err := a.TG.SendOriginDocument(ctx, imgData, "Original")
			if err != nil {
				stats.Failed++
				log.Printf("Pixiv origin send failed pid=%s err=%v", c.PID, err)
				continue
			}

			prepared = append(prepared, pixivPreparedPage{
				Candidate:    c,
				Data:         imgData,
				OriginID:     originID,
				StorageMsgID: storageMsgID,
			})
			time.Sleep(1200 * time.Millisecond)
		}
		if len(prepared) == 0 {
			continue
		}

		groupCaption := ""
		isLastGroup := groupIdx == len(groups)-1
		if isLastGroup {
			groupCaption = buildPreviewCaption(normalizePublishMeta(baseMeta))
		}

		previewItems := make([]telegram.PreviewMedia, 0, len(prepared))
		for _, p := range prepared {
			previewItems = append(previewItems, telegram.PreviewMedia{
				Data:     p.Data,
				Filename: fmt.Sprintf("%s_preview.jpg", p.Candidate.PID),
				Width:    p.Candidate.Width,
				Height:   p.Candidate.Height,
			})
		}

		previewResults, err := a.TG.SendPreviewMediaGroup(ctx, previewItems, groupCaption)
		if err != nil {
			log.Printf("Pixiv media group failed artwork=%s group=%d err=%v, fallback=single_preview", artworkID, groupIdx+1, err)
			fallbackPrepared := make([]pixivPreparedPage, 0, len(prepared))
			fallbackPreview := make([]telegram.PreviewSendResult, 0, len(prepared))
			for i, p := range prepared {
				caption := ""
				if i == 0 {
					caption = groupCaption
				}
				res, sendErr := a.TG.SendPreviewPhoto(ctx, p.Data, caption)
				if sendErr != nil {
					stats.Failed++
					log.Printf("Pixiv fallback preview failed pid=%s err=%v", p.Candidate.PID, sendErr)
					continue
				}
				fallbackPrepared = append(fallbackPrepared, p)
				fallbackPreview = append(fallbackPreview, res)
			}
			prepared = fallbackPrepared
			previewResults = fallbackPreview
		}

		if len(prepared) == 0 || len(previewResults) == 0 {
			continue
		}
		if len(prepared) != len(previewResults) {
			limit := len(prepared)
			if len(previewResults) < limit {
				limit = len(previewResults)
			}
			prepared = prepared[:limit]
			previewResults = previewResults[:limit]
		}

		discussionMsgID := 0
		if isLastGroup {
			anchorMeta := baseMeta
			anchorMeta.ID = prepared[0].Candidate.PID
			originLinks := make([]discussionOriginLink, 0, len(prepared))
			for i, page := range prepared {
				originLinks = append(originLinks, discussionOriginLink{
					ImageID:      page.Candidate.PID,
					OriginID:     page.OriginID,
					StorageMsgID: page.StorageMsgID,
					Label:        fmt.Sprintf("\u539f\u56fe%d", i+1),
				})
			}
			discussionMsgID = a.sendDiscussionCommentWithOrigins(ctx, normalizePublishMeta(anchorMeta), previewResults[0].PublishMsgID, originLinks)
		}

		for i, p := range prepared {
			meta := baseMeta
			meta.ID = p.Candidate.PID
			meta.CreatedAt = time.Now().Unix()

			width := previewResults[i].Width
			height := previewResults[i].Height
			if width <= 0 {
				width = p.Candidate.Width
			}
			if height <= 0 {
				height = p.Candidate.Height
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
				log.Printf("Pixiv persist failed pid=%s err=%v", p.Candidate.PID, persistErr)
				return fmt.Errorf("%w: persist image %s: %v", errPixivCrawlStop, p.Candidate.PID, persistErr)
			}

			stats.Downloaded++
			if stats.FirstID == "" {
				stats.FirstID = img.ID
			}
			log.Printf("Pixiv stored pid=%s size=%dx%d", p.Candidate.PID, img.Width, img.Height)
		}

		time.Sleep(2 * time.Second)
	}
	return nil
}
