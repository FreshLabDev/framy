// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/storage"
)

// Results made on the site come to the person's DM (docs/design.md, DM).
const (
	// siteEvery is how often the site's new results are read.
	siteEvery = 5 * time.Second
	// siteWithin: a result that ended longer ago is not sent. One held back
	// by an outage is old news by then, and the site has it.
	siteWithin = time.Hour
	// siteBatch is how many results one round takes.
	siteBatch = 20
	// albumItems is the most items Telegram puts in one album.
	albumItems = 10
)

// runSite sends the site's results to the DMs until ctx ends.
func (f *Flow) runSite(ctx context.Context) {
	tick := time.NewTicker(siteEvery)
	defer tick.Stop()
	for {
		f.sendSite(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// sendSite sends every result the site made since the last round.
func (f *Flow) sendSite(ctx context.Context) {
	results, err := f.cfg.Store.SiteResults(ctx, f.now().Add(-siteWithin), siteBatch)
	if err != nil {
		if ctx.Err() == nil {
			f.log.Error("site results unread", "error", err)
		}
		return
	}
	var wg sync.WaitGroup
	slot := make(chan struct{}, following)
	for _, r := range results {
		wg.Go(func() {
			slot <- struct{}{}
			defer func() { <-slot }()
			f.sendSiteResult(ctx, r)
		})
	}
	wg.Wait()
}

// sendSiteResult sends one result to its person's DM, at most once. A chat
// action goes first: it says framy is sending, and it tells, before any
// upload, whether the bot may write there at all. It may when the person
// allowed it at sign-in (telegram:bot_access) or pressed /start.
func (f *Flow) sendSiteResult(ctx context.Context, r db.SiteResult) {
	claimed, err := f.cfg.Store.ClaimSiteResult(ctx, r.JobID)
	if err != nil || !claimed {
		if err != nil && ctx.Err() == nil {
			f.log.Warn("site result not claimed", "job", r.JobID, "error", err)
		}
		return
	}
	// Nothing went out yet: a later round tries again.
	release := func(why string, err error) {
		if ctx.Err() == nil {
			f.log.Warn(why, "job", r.JobID, "error", err)
		}
		if err := f.cfg.Store.ReleaseSiteResult(context.WithoutCancel(ctx), r.JobID); err != nil {
			f.log.Warn("site result not released", "job", r.JobID, "error", err)
		}
	}
	end := func(state string) {
		if err := f.cfg.Store.EndSiteResult(context.WithoutCancel(ctx), r.JobID, state); err != nil {
			f.log.Warn("site result not ended", "job", r.JobID, "state", state, "error", err)
		}
	}
	assets, err := f.cfg.Files.Results(ctx, r.JobID)
	if err != nil {
		release("site results unread", err)
		return
	}
	if len(assets) == 0 {
		f.log.Warn("site result has no file", "job", r.JobID)
		end(db.DeliveryFailed)
		return
	}
	video := strings.HasPrefix(assets[0].ContentType, "video/")
	action := "upload_photo"
	if video {
		action = "upload_video"
	}
	if err := f.cfg.Telegram.SendChatAction(ctx, r.UserID, 0, action); err != nil {
		if mayNotWrite(err) {
			f.log.Info("site result not sent: the bot may not write to the person", "job", r.JobID)
			end(db.DeliveryClosed)
			return
		}
		release("site result held back", err)
		return
	}
	lang, err := f.cfg.Store.UserLang(ctx, r.UserID)
	if err != nil {
		f.log.Warn("person language unread", "error", err)
	}
	if lang == "" {
		lang = i18n.DefaultLang
	}
	opts := tg.CaptionOptions{Caption: resultCaption(r.Prompt, r.Model)}
	var msg tg.Message
	switch {
	case video:
		msg, err = f.postVideo(ctx, r.UserID, assets[0], opts)
	case len(assets) == 1:
		opts.Markup = resultKeyboard(lang, r.JobID)
		msg, err = f.postImage(ctx, r.UserID, assets[0], opts)
	default:
		msg, err = f.postAlbum(ctx, r.UserID, assets, opts.Caption)
	}
	if err != nil {
		if ctx.Err() == nil {
			f.log.Error("site result not sent", "job", r.JobID, "error", err)
			end(db.DeliveryFailed)
		}
		return
	}
	run := db.ChatRun{JobID: r.JobID, ChatID: r.UserID, Lang: lang}
	res := resultOf(msg)
	if err := f.cfg.Store.SentSiteResult(context.WithoutCancel(ctx), run, res); err != nil {
		f.log.Error("site result not recorded as sent", "job", r.JobID, "error", err)
	}
}

// mayNotWrite tells a refusal that says the bot may not write to the
// person: a 403 (never started, blocked, deactivated) or a 400 (the chat
// is unknown to the bot). Nothing sent there would arrive.
func mayNotWrite(err error) bool {
	var apiErr *tg.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode == 403 || apiErr.ErrorCode == 400)
}

// postAlbum sends the pictures of a result as one album, the caption on the
// first, and returns its first message: as photos, or all as documents when
// one would not go as a photo, since Telegram does not mix the two. An album
// takes no keyboard, so it has no Animate.
func (f *Flow) postAlbum(ctx context.Context, chatID int64, assets []storage.Asset, caption string) (tg.Message, error) {
	assets = assets[:min(len(assets), albumItems)]
	photos := true
	for _, a := range assets {
		photos = photos && photoFits(a)
	}
	items := make([]tg.InputMedia, 0, len(assets))
	for i, a := range assets {
		body, err := f.cfg.Files.Open(ctx, a)
		if err != nil {
			return tg.Message{}, err
		}
		defer body.Close()
		file := tg.InputFileUpload{Filename: "framy" + extOf(a.ContentType), Data: body}
		var text string
		if i == 0 {
			text = caption
		}
		if photos {
			items = append(items, tg.InputMediaPhoto{Media: file, Caption: text})
		} else {
			items = append(items, tg.InputMediaDocument{Media: file, Caption: text})
		}
	}
	msgs, err := f.cfg.Telegram.SendMediaGroup(ctx, chatID, items, 0)
	if err != nil {
		return tg.Message{}, err
	}
	if len(msgs) == 0 {
		return tg.Message{}, errors.New("sendMediaGroup returned no message")
	}
	return msgs[0], nil
}
