// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"cmp"
	"context"
	"errors"
	"html"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/media"
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

// sendSite sends every result the site made since the last round, a press
// of several frames as one album.
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
	for _, press := range pressesOf(results, len(results) == siteBatch) {
		wg.Go(func() {
			slot <- struct{}{}
			defer func() { <-slot }()
			f.sendSitePress(ctx, press)
		})
	}
	wg.Wait()
}

// pressesOf groups the results by the press that made them, in their
// order; a job made alone is a press of its own. When the round was full,
// the press of its last result may go on in the next one, and waits for it.
func pressesOf(results []db.SiteResult, full bool) [][]db.SiteResult {
	var out [][]db.SiteResult
	at := map[string]int{}
	for _, r := range results {
		if i, ok := at[r.Batch]; ok && r.Batch != "" {
			out[i] = append(out[i], r)
			continue
		}
		at[r.Batch] = len(out)
		out = append(out, []db.SiteResult{r})
	}
	if last := len(results) - 1; full && last >= 0 && results[last].Batch != "" {
		cut := at[results[last].Batch]
		out = append(out[:cut], out[cut+1:]...)
	}
	return out
}

// sendSitePress claims the results of one press and sends them, at most
// once: one alone as any result, several as one album.
func (f *Flow) sendSitePress(ctx context.Context, press []db.SiteResult) {
	var claimed []db.SiteResult
	for _, r := range press {
		ok, err := f.cfg.Store.ClaimSiteResult(ctx, r.JobID)
		if err != nil {
			// The press goes whole in a later round, not in pieces.
			f.releaseSite(ctx, "site result not claimed", err, claimed...)
			return
		}
		if ok {
			claimed = append(claimed, r)
		}
	}
	switch len(claimed) {
	case 0:
	case 1:
		f.sendSiteResult(ctx, claimed[0])
	default:
		f.sendSiteAlbum(ctx, claimed)
	}
}

// releaseSite gives back the claims on results nothing of which went out,
// so a later round tries again.
func (f *Flow) releaseSite(ctx context.Context, why string, err error, rs ...db.SiteResult) {
	if ctx.Err() == nil {
		f.log.Warn(why, "jobs", jobIDs(rs), "error", err)
	}
	for _, r := range rs {
		if err := f.cfg.Store.ReleaseSiteResult(context.WithoutCancel(ctx), r.JobID); err != nil {
			f.log.Warn("site result not released", "job", r.JobID, "error", err)
		}
	}
}

// endSite records that the claimed results will not be sent.
func (f *Flow) endSite(ctx context.Context, state string, rs ...db.SiteResult) {
	for _, r := range rs {
		if err := f.cfg.Store.EndSiteResult(context.WithoutCancel(ctx), r.JobID, state); err != nil {
			f.log.Warn("site result not ended", "job", r.JobID, "state", state, "error", err)
		}
	}
}

func jobIDs(rs []db.SiteResult) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.JobID
	}
	return out
}

// mayWrite sends the chat action that says framy is sending, which tells,
// before any upload, whether the bot may write to the person at all. It
// may when the person allowed it at sign-in (telegram:bot_access) or
// pressed /start. When it may not, or the action failed, the results are
// ended or given back, and mayWrite is false.
func (f *Flow) mayWrite(ctx context.Context, video bool, rs ...db.SiteResult) bool {
	action := "upload_photo"
	if video {
		action = "upload_video"
	}
	err := f.cfg.Telegram.SendChatAction(ctx, rs[0].UserID, 0, action)
	switch {
	case err == nil:
		return true
	case mayNotWrite(err):
		f.log.Info("site result not sent: the bot may not write to the person", "jobs", jobIDs(rs))
		f.endSite(ctx, db.DeliveryClosed, rs...)
	default:
		f.releaseSite(ctx, "site result held back", err, rs...)
	}
	return false
}

// siteLang is the person's language in core, the default when it is unread.
func (f *Flow) siteLang(ctx context.Context, userID int64) string {
	lang, err := f.cfg.Store.UserLang(ctx, userID)
	if err != nil {
		f.log.Warn("person language unread", "error", err)
	}
	if lang == "" {
		lang = i18n.DefaultLang
	}
	return lang
}

// sendSiteResult sends one claimed result to its person's DM.
func (f *Flow) sendSiteResult(ctx context.Context, r db.SiteResult) {
	if !r.Deliver {
		// The person turned the DM off; the result stays on the site, and is
		// not sent when they turn it on again.
		f.endSite(ctx, db.DeliveryOff, r)
		return
	}
	assets, err := f.cfg.Files.Results(ctx, r.JobID)
	if err != nil {
		f.releaseSite(ctx, "site results unread", err, r)
		return
	}
	if len(assets) == 0 {
		f.log.Warn("site result has no file", "job", r.JobID)
		f.endSite(ctx, db.DeliveryFailed, r)
		return
	}
	// A run Continue joined to its clip goes as the whole clip, as the site
	// shows it; the run's own video stays on the site.
	joined, ok, err := f.cfg.Files.Joined(ctx, r.JobID)
	if err != nil {
		f.releaseSite(ctx, "site results unread", err, r)
		return
	}
	var assetID int64
	if ok {
		assets, assetID = []storage.Asset{joined}, joined.ID
	}
	video := strings.HasPrefix(assets[0].ContentType, "video/")
	if !f.mayWrite(ctx, video, r) {
		return
	}
	lang := f.siteLang(ctx, r.UserID)
	opts := tg.CaptionOptions{Caption: resultCaption(r.Prompt, r.Model)}
	var msg tg.Message
	switch {
	case video:
		msg, err = f.postVideo(ctx, r.UserID, assets[0], opts)
	case len(assets) == 1:
		opts.Markup = resultKeyboard(lang, r.JobID, assets[0], false, true)
		msg, err = f.postImage(ctx, r.UserID, assets[0], opts)
	default:
		var msgs []tg.Message
		if msgs, err = f.postAlbum(ctx, r.UserID, assets, opts.Caption); err == nil {
			msg = msgs[0]
		}
	}
	if err != nil {
		if ctx.Err() == nil {
			f.log.Error("site result not sent", "job", r.JobID, "error", err)
			f.endSite(ctx, db.DeliveryFailed, r)
		}
		return
	}
	res := resultOf(msg)
	res.AssetID = assetID
	f.sentSite(ctx, r, lang, res)
}

// sentSite records the result as sent in res.
func (f *Flow) sentSite(ctx context.Context, r db.SiteResult, lang string, res db.ChatResult) {
	run := db.ChatRun{JobID: r.JobID, ChatID: r.UserID, Lang: lang}
	if err := f.cfg.Store.SentSiteResult(context.WithoutCancel(ctx), run, res); err != nil {
		f.log.Error("site result not recorded as sent", "job", r.JobID, "error", err)
	}
}

// frame is one picture or video of a press's album.
type frame struct {
	r     db.SiteResult
	asset storage.Asset
	// at is the frame's place in the press; joined is the clip Continue
	// joined the run to, which the run keeps as its message's file.
	at     int
	joined int64
}

// sendSiteAlbum sends the claimed results of one press to the DM as one
// album, in the order of its frames, with one caption on the first: the
// frames are often the same prompt in several models, to compare. Each job
// gets a chat run whose message is its first frame, so a reply to it works
// as on any result; an album takes no keyboard, so there is no Animate.
func (f *Flow) sendSiteAlbum(ctx context.Context, press []db.SiteResult) {
	if !press[0].Deliver {
		f.endSite(ctx, db.DeliveryOff, press...)
		return
	}
	var frames []frame
	for _, r := range press {
		assets, err := f.cfg.Files.Results(ctx, r.JobID)
		if err != nil {
			f.releaseSite(ctx, "site results unread", err, press...)
			return
		}
		joined, ok, err := f.cfg.Files.Joined(ctx, r.JobID)
		if err != nil {
			f.releaseSite(ctx, "site results unread", err, press...)
			return
		}
		var joinedID int64
		if ok {
			assets, joinedID = []storage.Asset{joined}, joined.ID
		}
		for i, a := range assets {
			at := math.MaxInt32
			if i < len(r.Frames) {
				at = int(r.Frames[i])
			}
			frames = append(frames, frame{r: r, asset: a, at: at, joined: joinedID})
		}
	}
	slices.SortStableFunc(frames, func(a, b frame) int { return cmp.Compare(a.at, b.at) })
	frames = frames[:min(len(frames), albumItems)]
	// first is each job's first frame in the album; a job with none is not
	// sent, and stays on the site.
	first := map[int64]int{}
	for i, fr := range frames {
		if _, ok := first[fr.r.JobID]; !ok {
			first[fr.r.JobID] = i
		}
	}
	var sending []db.SiteResult
	for _, r := range press {
		if _, ok := first[r.JobID]; ok {
			sending = append(sending, r)
			continue
		}
		f.log.Warn("site result has no file", "job", r.JobID)
		f.endSite(ctx, db.DeliveryFailed, r)
	}
	switch len(sending) {
	case 0:
		return
	case 1:
		f.sendSiteResult(ctx, sending[0])
		return
	}
	assets := make([]storage.Asset, len(frames))
	video := true
	for i, fr := range frames {
		assets[i] = fr.asset
		video = video && strings.HasPrefix(fr.asset.ContentType, "video/")
	}
	if !f.mayWrite(ctx, video, sending...) {
		return
	}
	lang := f.siteLang(ctx, sending[0].UserID)
	caption := albumCaption(frames)
	var msgs []tg.Message
	var err error
	if video {
		msgs, err = f.postVideoAlbum(ctx, sending[0].UserID, assets, caption)
	} else {
		msgs, err = f.postAlbum(ctx, sending[0].UserID, assets, caption)
	}
	if err == nil && len(msgs) < len(frames) {
		err = errors.New("sendMediaGroup returned fewer messages than it was sent")
	}
	if err != nil {
		if ctx.Err() == nil {
			f.log.Error("site result not sent", "jobs", jobIDs(sending), "error", err)
			f.endSite(ctx, db.DeliveryFailed, sending...)
		}
		return
	}
	for _, r := range sending {
		i := first[r.JobID]
		res := resultOf(msgs[i])
		res.AssetID = frames[i].joined
		f.sentSite(ctx, r, lang, res)
	}
}

// albumCaption is the caption of a press's album: what the frames share
// once, the prompt as a quote and the model under it, and what differs a
// line per frame, numbered as Telegram numbers the album's items.
func albumCaption(frames []frame) string {
	samePrompt, sameModel := true, true
	for _, fr := range frames[1:] {
		samePrompt = samePrompt && strings.TrimSpace(fr.r.Prompt) == strings.TrimSpace(frames[0].r.Prompt)
		sameModel = sameModel && fr.r.Model == frames[0].r.Model
	}
	if samePrompt && sameModel {
		return resultCaption(frames[0].r.Prompt, frames[0].r.Model)
	}
	parts := make([]string, 0, len(frames)+2)
	if samePrompt {
		parts = append(parts, promptQuote(frames[0].r.Prompt))
	}
	for i, fr := range frames {
		line := "<b>" + strconv.Itoa(i+1) + "</b>"
		if !sameModel {
			line += " " + html.EscapeString(fr.r.Model)
		}
		if !samePrompt {
			line += " · " + html.EscapeString(shortPrompt(fr.r.Prompt))
		}
		parts = append(parts, line)
	}
	if sameModel {
		parts = append(parts, "<b>"+html.EscapeString(frames[0].r.Model)+"</b>")
	}
	return lines(parts...)
}

// albumPrompt is how much of a frame's own prompt its line shows: six
// lines of it stay well inside a caption's 1024 characters.
const albumPrompt = 120

// shortPrompt is the prompt on one line, cut to albumPrompt characters.
func shortPrompt(prompt string) string {
	prompt = strings.Join(strings.Fields(prompt), " ")
	if utf8.RuneCountInString(prompt) > albumPrompt {
		prompt = strings.TrimSpace(string([]rune(prompt)[:albumPrompt])) + "…"
	}
	return prompt
}

// mayNotWrite tells a refusal that says the bot may not write to the
// person: a 403 (never started, blocked, deactivated) or a 400 (the chat
// is unknown to the bot). Nothing sent there would arrive.
func mayNotWrite(err error) bool {
	var apiErr *tg.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode == 403 || apiErr.ErrorCode == 400)
}

// postAlbum sends pictures as one album, the caption on the first, and
// returns its messages, one per picture: as photos, or all as documents
// when one would not go as a photo, since Telegram does not mix the two. An
// album takes no keyboard, so it has no Animate.
func (f *Flow) postAlbum(ctx context.Context, chatID int64, assets []storage.Asset, caption string) ([]tg.Message, error) {
	assets = assets[:min(len(assets), albumItems)]
	photos := true
	for _, a := range assets {
		photos = photos && photoFits(a)
	}
	items := make([]tg.InputMedia, 0, len(assets))
	for i, a := range assets {
		body, err := f.cfg.Files.Open(ctx, a)
		if err != nil {
			return nil, err
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
	return f.sendGroup(ctx, chatID, items)
}

// sendGroup sends an album and returns its messages.
func (f *Flow) sendGroup(ctx context.Context, chatID int64, items []tg.InputMedia) ([]tg.Message, error) {
	msgs, err := f.cfg.Telegram.SendMediaGroup(ctx, chatID, items, 0)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, errors.New("sendMediaGroup returned no message")
	}
	return msgs, nil
}

// postVideoAlbum sends videos as one album, the caption on the first, each
// readied and with its cover as postVideo sends one alone, and returns its
// messages. Covers Telegram refuses do not keep the videos back: they go
// again without.
func (f *Flow) postVideoAlbum(ctx context.Context, chatID int64, assets []storage.Asset, caption string) ([]tg.Message, error) {
	type ready struct {
		v       media.Video
		streams bool
		cover   string
	}
	videos := make([]ready, 0, len(assets))
	for _, a := range assets[:min(len(assets), albumItems)] {
		dir, done, err := f.cfg.Work.Dir("result")
		if err != nil {
			return nil, err
		}
		defer done()
		v, streams, err := f.readyVideo(ctx, a, dir)
		if err != nil {
			return nil, err
		}
		videos = append(videos, ready{v: v, streams: streams, cover: f.coverOf(ctx, dir, a.ID)})
	}
	send := func(covers bool) ([]tg.Message, error) {
		items := make([]tg.InputMedia, 0, len(videos))
		for i, r := range videos {
			body, err := os.Open(r.v.Path)
			if err != nil {
				return nil, err
			}
			defer body.Close()
			item := tg.InputMediaVideo{Media: tg.InputFileUpload{Filename: "framy.mp4", Data: body},
				Width: r.v.Width, Height: r.v.Height, Duration: int(math.Ceil(r.v.Seconds)), SupportsStreaming: r.streams}
			if covers && r.cover != "" {
				c, err := os.Open(r.cover)
				if err != nil {
					return nil, err
				}
				defer c.Close()
				item.Cover = tg.InputFileUpload{Filename: "cover.jpg", Data: c}
			}
			if i == 0 {
				item.Caption = caption
			}
			items = append(items, item)
		}
		return f.sendGroup(ctx, chatID, items)
	}
	msgs, err := send(true)
	var apiErr *tg.APIError
	if err == nil || !errors.As(err, &apiErr) || apiErr.ErrorCode != 400 {
		return msgs, err
	}
	f.log.Warn("covers refused; videos sent without", "error", err)
	return send(false)
}
