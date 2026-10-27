// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/price"
)

// maxPrompt is the longest prompt taken, in characters, as on the site.
const maxPrompt = 32000

// Notes a preview can carry about what was taken: i18n keys.
const (
	// noteAlbumOne and noteAlbumPart: framy saw only one or some photos of
	// the album, since it is not an admin of the group.
	noteAlbumOne  = "album.not_admin"
	noteAlbumPart = "run.album_part"
	// noteCut: the album had more photos than the model takes.
	noteCut = "run.cut"
)

// Albums is the bot's album cache: *bot.Bot. Album waits for the rest of an
// album and returns what the bot saw of it; SeesEveryMessage tells whether
// that is the whole album, which it is when framy is an admin of the group.
// A nil Albums sees only the photo that was answered.
type Albums interface {
	Album(ctx context.Context, chatID int64, mediaGroupID string, quiet, max time.Duration) ([]db.AlbumItem, error)
	SeesEveryMessage(ctx context.Context, chatID int64) (bool, error)
}

// How long an album is waited for: until no photo of it came for albumQuiet,
// at most albumMax (docs/design.md, Albums).
const (
	albumQuiet = 1500 * time.Millisecond
	albumMax   = 5 * time.Second
)

// SetAlbums gives the flow the album cache. It is set before the bot starts.
func (f *Flow) SetAlbums(a Albums) { f.albums = a }

// photo is a file a run takes, as Telegram has it.
type photo struct {
	fileID   string
	size     int64
	w, h     int
	document bool
}

// found is what a command message points at.
type found struct {
	photos []photo
	// group is the media_group_id of the message the photo came from.
	group string
	// video: a video, a GIF or a round video, which /img does not take.
	video bool
}

// sourcesOf reads what the command works on: the photo it is the caption
// of, or the message it replies to. A reply to text, or to nothing, makes an
// image from scratch.
func sourcesOf(m *tg.Message) found {
	if s, ok := mediaOf(m.Photo, m.Document, m.Animation, m.Video, m.VideoNote); ok {
		s.group = m.MediaGroupID
		return s
	}
	if r := m.ReplyToMessage; r != nil {
		if s, ok := mediaOf(r.Photo, r.Document, r.Animation, r.Video, r.VideoNote); ok {
			s.group = r.MediaGroupID
			return s
		}
		return found{}
	}
	if r := m.ExternalReply; r != nil {
		if s, ok := mediaOf(r.Photo, r.Document, r.Animation, r.Video, r.VideoNote); ok {
			return s
		}
	}
	return found{}
}

// mediaOf is the picture among a message's attachments. An animation comes
// with a document as well, and is looked at first.
func mediaOf(photos []tg.Photo, doc *tg.Document, an *tg.Animation, video *tg.Video, note *tg.VideoNote) (found, bool) {
	switch {
	case an != nil, video != nil, note != nil:
		return found{video: true}, true
	case len(photos) > 0:
		// Telegram lists the sizes from the smallest up.
		p := photos[len(photos)-1]
		return found{photos: []photo{{fileID: p.FileID, size: p.FileSize, w: p.Width, h: p.Height}}}, true
	case doc != nil && strings.HasPrefix(doc.MimeType, "image/"):
		return found{photos: []photo{{fileID: doc.FileID, size: doc.FileSize, document: true}}}, true
	case doc != nil && strings.HasPrefix(doc.MimeType, "video/"):
		return found{video: true}, true
	}
	return found{}, false
}

// dedupeKey makes a replayed update start nothing: the chat and the command
// message are the same in every copy of the update.
func dedupeKey(m *tg.Message) string {
	return "tg:" + strconv.FormatInt(m.Chat.ID, 10) + ":" + strconv.FormatInt(m.MessageID, 10)
}

// cutPrompt keeps the first maxPrompt characters.
func cutPrompt(s string) string {
	if utf8.RuneCountInString(s) <= maxPrompt {
		return s
	}
	return string([]rune(s)[:maxPrompt])
}

// Payers of a run.
const (
	payFree = "free"
	payKey  = "key"
)

// draft is a job made from a command, before its preview is sent.
type draft struct {
	id       int64
	pay      string
	model    catalog.Model
	offer    catalog.Offer
	estimate price.Estimate
	quote    fx.Quote
	usage    free.Meter
	paths    []string
	taken    int
	album    int
	note     string
}

func (f *Flow) command(ctx context.Context, m *tg.Message, command, prompt string) error {
	lang := f.chatLang(ctx, m.Chat, *m.From)
	if command == CommandVideo {
		return f.reply(ctx, m, t(lang, "run.video_soon"))
	}
	if prompt == "" {
		return f.reply(ctx, m, t(lang, "run.empty"))
	}
	key := dedupeKey(m)
	if _, seen, err := f.cfg.Store.JobByKey(ctx, key); err != nil {
		return err
	} else if seen {
		return nil
	}
	src := sourcesOf(m)
	if src.video {
		return f.reply(ctx, m, t(lang, "run.video_soon"))
	}
	f.react(ctx, m.Chat.ID, m.MessageID, reactSeen)
	thread := threadOf(m)
	if err := f.cfg.Telegram.SendChatAction(ctx, m.Chat.ID, thread, "upload_photo"); err != nil {
		f.log.Debug("chat action not sent", "chat", m.Chat.ID, "error", err)
	}
	dir, done, err := f.cfg.Work.Dir("chat")
	if err != nil {
		return f.fail(ctx, m, lang, err)
	}
	defer done()

	d, refusal, err := f.draft(ctx, m, src, cutPrompt(prompt), key, dir)
	if err != nil {
		return f.fail(ctx, m, lang, err)
	}
	if refusal != "" {
		f.react(ctx, m.Chat.ID, m.MessageID, "")
		return f.reply(ctx, m, refusal)
	}
	if d.id == 0 {
		// The same command came again while its draft was being made.
		return nil
	}
	preview, err := f.sendPreview(ctx, m, d, prompt, lang)
	if err != nil {
		f.cancel(ctx, d.id)
		return f.fail(ctx, m, lang, err)
	}
	run := db.ChatRun{
		JobID: d.id, ChatID: m.Chat.ID, ThreadID: thread, CommandMessageID: m.MessageID,
		PreviewMessageID: preview, Lang: lang, Taken: d.taken, Album: d.album, Note: d.note,
	}
	if err := f.cfg.Store.AddChatRun(ctx, run); err != nil {
		f.deleteMessage(ctx, m.Chat.ID, preview)
		f.cancel(ctx, d.id)
		return f.fail(ctx, m, lang, err)
	}
	return nil
}

// fail tells the chat a command could not be worked and takes the reaction
// off. The error goes on to the log.
func (f *Flow) fail(ctx context.Context, m *tg.Message, lang string, err error) error {
	f.react(ctx, m.Chat.ID, m.MessageID, "")
	if rerr := f.reply(ctx, m, errorText(lang, err)); rerr != nil {
		f.log.Warn("failure not told", "chat", m.Chat.ID, "error", rerr)
	}
	return err
}

// draft collects the photos, keeps them, prices the run and makes the job.
// A refusal is the text to answer with instead, when the run cannot start:
// the free tier is used up and the person has no key. A zero id with no
// refusal is a job the same command made already.
func (f *Flow) draft(ctx context.Context, m *tg.Message, src found, prompt, key, dir string) (draft, string, error) {
	lang := f.chatLang(ctx, m.Chat, *m.From)
	user := m.From.ID
	var d draft
	photos := src.photos
	if src.group != "" {
		photos, d.note = f.album(ctx, m.Chat.ID, src.group, photos)
	}
	if max := free.ImageKlein.MaxInputs; len(photos) > max {
		d.album = len(photos)
		photos = photos[:max]
		d.note = noteCut
	}
	plan, err := free.ForImages(len(photos))
	if err != nil {
		return d, "", err
	}
	model, offer, err := f.cfg.Store.Offer(ctx, plan.Offer)
	if err != nil {
		return d, "", fmt.Errorf("the free model %s: %w", plan.Offer, err)
	}
	inputs, sizes, paths, err := f.keep(ctx, user, dir, photos)
	if err != nil {
		return d, "", err
	}
	settings := plan.SettingsFor(len(inputs))
	d.model, d.offer, d.paths, d.taken = model, offer, paths, len(inputs)
	d.estimate = price.Of(model, offer, price.Request{Settings: settings, Prompt: prompt, Images: sizes})
	if d.quote, err = f.quote(ctx, user, lang); err != nil {
		return d, "", err
	}
	surface := jobs.Group
	if m.Chat.Type == "private" {
		surface = jobs.DM
	}
	n := jobs.New{
		UserID: user, Surface: surface, ChatID: m.Chat.ID, MessageID: m.MessageID, DedupeKey: key,
		Offer: offer, Prompt: prompt, Settings: settings, Free: true, Estimate: &d.estimate, Inputs: inputs,
	}
	if d.quote.Code != fx.USD {
		n.FX = &d.quote
	}
	d.pay = payFree
	id, created, err := f.cfg.Queue.Draft(ctx, n)
	var (
		limit  *free.LimitError
		closed *free.ClosedError
	)
	if errors.As(err, &limit) || errors.As(err, &closed) {
		// Past the free tier the person's own key runs the same model, at
		// the price the preview shows. Without one the reason is the answer.
		has, kerr := f.hasKey(ctx, user, offer.Provider)
		if kerr != nil {
			return d, "", kerr
		}
		if !has {
			if limit != nil {
				return d, t(lang, "run.limit", "limit", strconv.Itoa(limit.Limit)), nil
			}
			return d, t(lang, "run.closed"), nil
		}
		n.Free, d.pay = false, payKey
		id, created, err = f.cfg.Queue.Draft(ctx, n)
	}
	if err != nil {
		return d, "", err
	}
	if !created {
		return draft{}, "", nil
	}
	d.id = id
	if d.pay == payFree {
		u, err := f.cfg.Allowance.Usage(ctx, user)
		if err != nil {
			f.log.Warn("free usage unread", "user", user, "error", err)
		}
		d.usage = u.Images
	}
	return d, "", nil
}

// album is the photos of the album a command answered, and the note about
// what was taken: the one photo when the album cannot be read.
func (f *Flow) album(ctx context.Context, chatID int64, group string, one []photo) ([]photo, string) {
	if f.albums == nil {
		return one, noteAlbumOne
	}
	items, err := f.albums.Album(ctx, chatID, group, albumQuiet, albumMax)
	if err != nil {
		f.log.Warn("album unread; taking the one photo", "chat", chatID, "error", err)
		return one, noteAlbumOne
	}
	var photos []photo
	for _, it := range items {
		// A video or a file in the album is not a photo to edit.
		if it.Kind == db.AlbumPhoto {
			photos = append(photos, photo{fileID: it.FileID, size: it.FileSize, w: it.Width, h: it.Height})
		}
	}
	if len(photos) == 0 {
		photos = one
	}
	whole, err := f.albums.SeesEveryMessage(ctx, chatID)
	if err != nil {
		f.log.Warn("admin rights unread", "chat", chatID, "error", err)
	}
	switch {
	case whole:
		return photos, ""
	case len(photos) == 1:
		return photos, noteAlbumOne
	}
	return photos, noteAlbumPart
}

// keep downloads the photos from Telegram and keeps them as the person's
// files, the run's inputs in order. paths are the normalized pictures in dir,
// which the preview is drawn from.
func (f *Flow) keep(ctx context.Context, user int64, dir string, photos []photo) (ids []int64, sizes []price.Image, paths []string, err error) {
	for i, p := range photos {
		if p.size > f.cfg.MaxDownload {
			return nil, nil, nil, errTooBig
		}
		file, err := f.cfg.Telegram.GetFile(ctx, p.fileID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("get file %d: %w", i, err)
		}
		if file.FileSize > f.cfg.MaxDownload {
			return nil, nil, nil, errTooBig
		}
		raw := filepath.Join(dir, fmt.Sprintf("raw-%d", i))
		if err := f.cfg.Telegram.DownloadToFile(ctx, file.FilePath, raw, f.cfg.MaxDownload); err != nil {
			return nil, nil, nil, fmt.Errorf("download file %d: %w", i, err)
		}
		in, err := os.Open(raw)
		if err != nil {
			return nil, nil, nil, err
		}
		path := filepath.Join(dir, fmt.Sprintf("in-%d", i))
		a, err := f.cfg.Files.UploadCopy(ctx, user, in, path)
		in.Close()
		os.Remove(raw)
		if err != nil {
			return nil, nil, nil, err
		}
		ids = append(ids, a.ID)
		sizes = append(sizes, price.Image{Width: a.Width, Height: a.Height})
		paths = append(paths, path)
	}
	return ids, sizes, paths, nil
}

// hasKey tells whether the person has a key for provider that the provider
// has not refused.
func (f *Flow) hasKey(ctx context.Context, user int64, provider string) (bool, error) {
	if f.cfg.Keys == nil {
		return false, nil
	}
	conns, err := f.cfg.Keys.List(ctx, user)
	if errors.Is(err, keys.ErrDisabled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, c := range conns {
		if c.Provider == provider && c.Status != keys.Invalid {
			return true, nil
		}
	}
	return false, nil
}

// quote is the currency the price is shown in, and its rate: the person's
// choice, else the one of their language, else USD. A currency with no rate
// yet is shown in USD.
func (f *Flow) quote(ctx context.Context, user int64, lang string) (fx.Quote, error) {
	chosen, err := f.cfg.Store.Currency(ctx, user)
	if err != nil {
		return fx.Quote{}, err
	}
	q, err := f.cfg.Store.Quote(ctx, fx.Default(chosen, "", lang), fx.Day(f.now()))
	if errors.Is(err, fx.ErrNoRate) {
		return fx.Dollar, nil
	}
	return q, err
}

// cancel ends a draft whose preview could not be shown.
func (f *Flow) cancel(ctx context.Context, jobID int64) {
	ctx, done := detach(ctx)
	defer done()
	if err := f.cfg.Queue.Cancel(ctx, jobID); err != nil {
		f.log.Warn("draft not cancelled", "job", jobID, "error", err)
	}
}

func (f *Flow) deleteMessage(ctx context.Context, chatID, messageID int64) {
	if messageID == 0 {
		return
	}
	if err := f.cfg.Telegram.DeleteMessage(ctx, chatID, messageID); err != nil {
		f.log.Warn("message not deleted", "chat", chatID, "message", messageID, "error", err)
	}
}

// detach lets the cleanup after a failure finish when the failure was the
// end of ctx.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}
