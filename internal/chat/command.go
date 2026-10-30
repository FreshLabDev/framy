// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/storage"
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
	// noteClipCut: the clip was longer than the run takes, and its start
	// was taken.
	noteClipCut = "edit.cut"
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

// clip is a video a run edits, as Telegram has it: a video, a GIF, a round
// video or a video document.
type clip struct {
	fileID string
	size   int64
}

// found is what a command message points at: photos or a clip.
type found struct {
	photos []photo
	// group is the media_group_id of the message the photo came from.
	group string
	clip  *clip
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

// mediaOf is the picture or the clip among a message's attachments. An
// animation comes with a document as well, and is looked at first.
func mediaOf(photos []tg.Photo, doc *tg.Document, an *tg.Animation, video *tg.Video, note *tg.VideoNote) (found, bool) {
	switch {
	case an != nil:
		return found{clip: &clip{fileID: an.FileID, size: an.FileSize}}, true
	case video != nil:
		return found{clip: &clip{fileID: video.FileID, size: video.FileSize}}, true
	case note != nil:
		return found{clip: &clip{fileID: note.FileID, size: note.FileSize}}, true
	case len(photos) > 0:
		// Telegram lists the sizes from the smallest up.
		p := photos[len(photos)-1]
		return found{photos: []photo{{fileID: p.FileID, size: p.FileSize, w: p.Width, h: p.Height}}}, true
	case doc != nil && strings.HasPrefix(doc.MimeType, "image/"):
		return found{photos: []photo{{fileID: doc.FileID, size: doc.FileSize, document: true}}}, true
	case doc != nil && strings.HasPrefix(doc.MimeType, "video/"):
		return found{clip: &clip{fileID: doc.FileID, size: doc.FileSize}}, true
	}
	return found{}, false
}

// dedupeKey makes a replayed update start nothing: the chat and the command
// message are the same in every copy of the update. An ephemeral command has
// no message id, and Telegram gives its ephemeral id to another message once
// it is gone, so the date goes with it.
func dedupeKey(m *tg.Message) string {
	chat := "tg:" + strconv.FormatInt(m.Chat.ID, 10) + ":"
	if ephemeralCommand(m) {
		return chat + "e" + strconv.FormatInt(m.EphemeralMessageID, 10) + ":" + strconv.FormatInt(m.Date, 10)
	}
	return chat + strconv.FormatInt(m.MessageID, 10)
}

// ephemeralCommand: the command is an ephemeral message, which only framy
// and its sender see.
func ephemeralCommand(m *tg.Message) bool { return m.MessageID == 0 && m.EphemeralMessageID != 0 }

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
	settings map[string]any
	paths    []string
	taken    int
	album    int
	note     string
	// clip: the run edits a clip, source as Telegram has it. keptSeconds is
	// the clip as kept, and clipSeconds the whole of it.
	clip        bool
	source      clip
	keptSeconds float64
	clipSeconds float64
	// hasKey: the person has a key for the run's provider. freeLeft: the
	// week's free video is left, as far as the meter knows, for an edit on
	// the key that could go free.
	hasKey   bool
	freeLeft bool
}

// video tells whether the run makes a video.
func (d draft) video() bool { return d.offer.Kind == catalog.Video }

func (f *Flow) command(ctx context.Context, m *tg.Message, command, prompt string) error {
	lang := f.chatLang(ctx, m.Chat, *m.From)
	edit, motion := command == CommandVideo, command == commandMotion
	if prompt == "" {
		if edit {
			return f.reply(ctx, m, t(lang, "edit.empty"))
		}
		return f.reply(ctx, m, t(lang, "run.empty"))
	}
	key := dedupeKey(m)
	if _, seen, err := f.cfg.Store.JobByKey(ctx, key); err != nil {
		return err
	} else if seen {
		return nil
	}
	src := sourcesOf(m)
	switch {
	case edit && src.clip == nil && len(src.photos) > 0:
		return f.reply(ctx, m, t(lang, "edit.photo"))
	case edit && src.clip == nil:
		return f.reply(ctx, m, t(lang, "edit.no_video"))
	case !edit && src.clip != nil:
		return f.reply(ctx, m, t(lang, "run.video_edit"))
	}
	thread := threadOf(m)
	if err := f.cfg.Telegram.SendChatAction(ctx, m.Chat.ID, thread, chatAction(edit || motion)); err != nil {
		f.log.Debug("chat action not sent", "chat", m.Chat.ID, "error", err)
	}
	dir, done, err := f.cfg.Work.Dir("chat")
	if err != nil {
		return f.fail(ctx, m, lang, err)
	}
	defer done()

	var (
		d       draft
		refusal string
	)
	switch {
	case edit:
		d, refusal, err = f.draftVideo(ctx, m, *src.clip, cutPrompt(prompt), key, dir)
	case motion:
		d, refusal, err = f.draftMotion(ctx, m, src, cutPrompt(prompt), key, dir)
	default:
		d, refusal, err = f.draft(ctx, m, src, cutPrompt(prompt), key, dir)
	}
	if err != nil {
		return f.fail(ctx, m, lang, err)
	}
	if refusal != "" {
		return f.reply(ctx, m, refusal)
	}
	if d.id == 0 {
		// The same command came again while its draft was being made.
		return nil
	}
	preview, ephemeral, err := f.sendPreview(ctx, m, d, prompt, lang, dir, "")
	if err != nil {
		f.cancel(ctx, d.id)
		return f.fail(ctx, m, lang, err)
	}
	run := db.ChatRun{
		JobID: d.id, ChatID: m.Chat.ID, ThreadID: thread, CommandMessageID: m.MessageID,
		PreviewMessageID: preview, PreviewEphemeral: ephemeral, CommandEphemeral: ephemeralCommand(m),
		Lang: lang, Taken: d.taken, Album: d.album, Note: d.note, UserID: m.From.ID,
	}
	if d.clip {
		run.ClipFileID, run.ClipBytes, run.ClipSeconds = d.source.fileID, d.source.size, d.clipSeconds
	}
	if err := f.cfg.Store.AddChatRun(ctx, run); err != nil {
		f.removePreview(ctx, run)
		f.cancel(ctx, d.id)
		return f.fail(ctx, m, lang, err)
	}
	return nil
}

// fail tells the chat a command could not be worked. The error goes on to
// the log.
func (f *Flow) fail(ctx context.Context, m *tg.Message, lang string, err error) error {
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
	d.model, d.offer, d.paths, d.taken, d.settings = model, offer, paths, len(inputs), settings
	d.estimate = price.Of(model, offer, priceRequest(prompt, settings, sizes, nil))
	if d.quote, err = f.quote(ctx, user, lang); err != nil {
		return d, "", err
	}
	d.pay = payFree
	if refusal, err := f.queueDraft(ctx, lang, newJob(m, key, prompt, inputs, &d), &d); refusal != "" || err != nil || d.id == 0 {
		return draft{}, refusal, err
	}
	if d.pay == payFree {
		u, err := f.cfg.Allowance.Usage(ctx, user)
		if err != nil {
			f.log.Warn("free usage unread", "user", user, "error", err)
		}
		d.usage = u.Images
	}
	return d, "", nil
}

// draftMotion keeps the photo, if there is one, and makes the job of a
// video from the prompt with the photo as its start frame: on the free
// video while the week's is left, on the person's key past it. A video
// takes one start frame, so of more photos the first goes.
func (f *Flow) draftMotion(ctx context.Context, m *tg.Message, src found, prompt, key, dir string) (draft, string, error) {
	lang := f.chatLang(ctx, m.Chat, *m.From)
	user := m.From.ID
	plan, model, offer, err := f.videoPlan(ctx)
	if err != nil {
		return draft{}, "", err
	}
	photos := src.photos
	if len(photos) > plan.MaxInputs {
		photos = photos[:plan.MaxInputs]
	}
	inputs, sizes, paths, err := f.keep(ctx, user, dir, photos)
	if err != nil {
		return draft{}, "", err
	}
	settings := plan.SettingsFor(len(inputs))
	d := draft{model: model, offer: offer, pay: payFree, paths: paths, taken: len(inputs), settings: settings}
	d.estimate = price.Of(model, offer, priceRequest(prompt, settings, sizes, nil))
	if d.quote, err = f.quote(ctx, user, lang); err != nil {
		return draft{}, "", err
	}
	if refusal, err := f.queueDraft(ctx, lang, newJob(m, key, prompt, inputs, &d), &d); refusal != "" || err != nil || d.id == 0 {
		return draft{}, refusal, err
	}
	if d.pay == payFree {
		u, err := f.cfg.Allowance.Usage(ctx, user)
		if err != nil {
			f.log.Warn("free usage unread", "user", user, "error", err)
		}
		d.usage = u.Videos
	}
	return d, "", nil
}

// errNoVideoModel: neither free video model is in the catalog.
var errNoVideoModel = errors.New("no free video model is offered")

// videoPlan is the plan of the free video from a prompt, and its model:
// HeyGen, or Grok Lite while the catalog does not offer HeyGen.
func (f *Flow) videoPlan(ctx context.Context) (free.Plan, catalog.Model, catalog.Offer, error) {
	for _, plan := range []free.Plan{free.VideoHeyGen, free.VideoGrok} {
		model, offer, err := f.cfg.Store.Offer(ctx, plan.Offer)
		if errors.Is(err, db.ErrNoOffer) {
			continue
		}
		if err != nil {
			return free.Plan{}, catalog.Model{}, catalog.Offer{}, fmt.Errorf("the free model %s: %w", plan.Offer, err)
		}
		return plan, model, offer, nil
	}
	return free.Plan{}, catalog.Model{}, catalog.Offer{}, errNoVideoModel
}

// priceRequest is what a run is priced by. Its first pictures are frames,
// as many as frame_images names, and the rest references, as the adapter
// sends them; video is the clip it edits.
func priceRequest(prompt string, settings map[string]any, images []price.Image, video *price.Video) price.Request {
	req := price.Request{Settings: settings, Prompt: prompt, Video: video}
	frames := frameCount(settings)
	for _, im := range images {
		if req.Frames < frames {
			req.Frames++
			continue
		}
		req.Images = append(req.Images, im)
	}
	return req
}

// requestOf prices a run by the files it takes, as kept: the pictures and
// the first clip.
func requestOf(prompt string, settings map[string]any, inputs []storage.Asset) price.Request {
	var (
		images []price.Image
		video  *price.Video
	)
	for _, a := range inputs {
		switch {
		case !strings.HasPrefix(a.ContentType, "video/"):
			images = append(images, price.Image{Width: a.Width, Height: a.Height})
		case video == nil:
			video = &price.Video{Seconds: a.Seconds, Width: a.Width, Height: a.Height}
		}
	}
	return priceRequest(prompt, settings, images, video)
}

// frameCount is how many frame types the frame_images setting names, as
// the job has it or as it comes back from JSON.
func frameCount(settings map[string]any) int {
	switch list := settings["frame_images"].(type) {
	case []string:
		return len(list)
	case []any:
		return len(list)
	}
	return 0
}

// newJob is the draft job of a command, as d has it: free when d.pay is.
func newJob(m *tg.Message, key, prompt string, inputs []int64, d *draft) jobs.New {
	surface := jobs.Group
	if m.Chat.Type == "private" {
		surface = jobs.DM
	}
	n := jobs.New{
		UserID: m.From.ID, Surface: surface, ChatID: m.Chat.ID, MessageID: m.MessageID, DedupeKey: key,
		Offer: d.offer, Prompt: prompt, Settings: d.settings, Free: d.pay == payFree, Estimate: &d.estimate, Inputs: inputs,
	}
	if d.quote.Code != fx.USD {
		n.FX = &d.quote
	}
	return n
}

// queueDraft makes the job and puts its id in d. Past the free tier the
// person's own key runs the same model, at the price the preview shows;
// without one the reason is the answer. A zero id with no refusal is a job
// the same command made already.
func (f *Flow) queueDraft(ctx context.Context, lang string, n jobs.New, d *draft) (string, error) {
	id, created, err := f.cfg.Queue.Draft(ctx, n)
	var (
		limit  *free.LimitError
		closed *free.ClosedError
	)
	if n.Free && (errors.As(err, &limit) || errors.As(err, &closed)) {
		has, kerr := f.hasKey(ctx, n.UserID, n.Offer.Provider)
		if kerr != nil {
			return "", kerr
		}
		if !has {
			if limit != nil {
				return f.limitText(lang, limit), nil
			}
			return closedText(lang, n.Offer.Kind), nil
		}
		// The queue just said no to the free run, whatever the meter said.
		n.Free, d.pay, d.hasKey, d.freeLeft = false, payKey, true, false
		id, created, err = f.cfg.Queue.Draft(ctx, n)
	}
	if err != nil {
		return "", err
	}
	if created {
		d.id = id
	}
	return "", nil
}

// clipSlack is how much longer than a run takes a clip may be and still go
// whole: a frame or so. The free plan allows as much.
const clipSlack = 0.05

// draftVideo keeps the clip a command answered and makes the job that edits
// it on the free plan's model, FLUX Video Edit. The free allowance comes
// first, as for images, and takes the first seconds its plan allows; past
// it, the person's key takes as much as the model does. A refusal is the
// text to answer with instead.
func (f *Flow) draftVideo(ctx context.Context, m *tg.Message, c clip, prompt, key, dir string) (draft, string, error) {
	lang := f.chatLang(ctx, m.Chat, *m.From)
	user := m.From.ID
	plan := free.VideoEdit
	model, offer, err := f.cfg.Store.Offer(ctx, plan.Offer)
	if err != nil {
		return draft{}, "", fmt.Errorf("the free model %s: %w", plan.Offer, err)
	}
	d := draft{clip: true, source: c, model: model, offer: offer, pay: payFree, settings: plan.SettingsFor(1), taken: 1}
	// The meter tells ahead whether the week's free video is left, so the
	// clip is cut once, to what the run that takes it allows, and a refusal
	// downloads nothing. The queue still has the last word.
	u, err := f.cfg.Allowance.Usage(ctx, user)
	switch {
	case err != nil:
		f.log.Warn("free usage unread", "user", user, "error", err)
	case u.Videos.Limit > 0 && u.Videos.Left() == 0:
		has, err := f.hasKey(ctx, user, offer.Provider)
		if err != nil {
			return draft{}, "", err
		}
		if !has {
			return draft{}, f.limitText(lang, &free.LimitError{
				Kind: catalog.Video, Limit: u.Videos.Limit, Window: u.Videos.Window, FreesAt: u.Videos.FreesAt,
			}), nil
		}
		d.pay, d.hasKey = payKey, true
	default:
		d.freeLeft = true
	}
	src := filepath.Join(dir, media.In)
	if err := f.download(ctx, c.fileID, c.size, f.cfg.MaxVideo, src); err != nil {
		return draft{}, "", err
	}
	v, err := f.cfg.Clips.ProbeVideo(ctx, src)
	if err != nil {
		return draft{}, "", err
	}
	var limits catalog.SourceVideo
	if s := model.Traits.SourceVideo; s != nil {
		limits = *s
	}
	if v.Seconds < limits.MinSeconds {
		return draft{}, t(lang, "edit.too_short", "min", strconv.Itoa(int(math.Ceil(limits.MinSeconds)))), nil
	}
	longest := plan.MaxSourceSeconds
	if d.pay == payKey {
		longest = float64(limits.MaxSeconds)
	}
	var cut *media.Cut
	if longest > 0 && v.Seconds > longest+clipSlack {
		cut = &media.Cut{Seconds: longest}
		d.note = noteClipCut
	}
	d.clipSeconds = v.Seconds
	if d.pay == payFree && v.Seconds > plan.MaxSourceSeconds+clipSlack {
		// The preview offers the clip on the key, marked when there is none.
		if d.hasKey, err = f.hasKey(ctx, user, offer.Provider); err != nil {
			return draft{}, "", err
		}
	}
	a, err := f.cfg.Files.KeepVideo(ctx, user, src, cut)
	if err != nil {
		return draft{}, "", err
	}
	if limits.MaxMiB > 0 && a.Bytes > int64(limits.MaxMiB)<<20 {
		return draft{}, "", errTooBig
	}
	d.keptSeconds = a.Seconds
	poster, err := f.cfg.Clips.Poster(ctx, dir)
	if err != nil {
		return draft{}, "", err
	}
	d.paths = []string{poster}
	d.estimate = price.Of(model, offer, priceRequest(prompt, d.settings, nil, &price.Video{Seconds: a.Seconds, Width: a.Width, Height: a.Height}))
	if d.quote, err = f.quote(ctx, user, lang); err != nil {
		return draft{}, "", err
	}
	if refusal, err := f.queueDraft(ctx, lang, newJob(m, key, prompt, []int64{a.ID}, &d), &d); refusal != "" || err != nil || d.id == 0 {
		return draft{}, refusal, err
	}
	if d.pay == payFree {
		u, err := f.cfg.Allowance.Usage(ctx, user)
		if err != nil {
			f.log.Warn("free usage unread", "user", user, "error", err)
		}
		d.usage = u.Videos
	}
	return d, "", nil
}

// chatAction is what the chat sees framy doing while a run of the kind is
// made.
func chatAction(video bool) string {
	if video {
		return "upload_video"
	}
	return "upload_photo"
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
		raw := filepath.Join(dir, fmt.Sprintf("raw-%d", i))
		if err := f.download(ctx, p.fileID, p.size, f.cfg.MaxDownload, raw); err != nil {
			return nil, nil, nil, fmt.Errorf("file %d: %w", i, err)
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

// download reads a file from Telegram into dst. One over limit, by what the
// message says, by what getFile says or by Telegram's own cap, is errTooBig.
func (f *Flow) download(ctx context.Context, fileID string, size, limit int64, dst string) error {
	if size > limit {
		return errTooBig
	}
	file, err := f.cfg.Telegram.GetFile(ctx, fileID)
	if err != nil {
		var apiErr *tg.APIError
		if errors.As(err, &apiErr) && strings.Contains(strings.ToLower(apiErr.Description), "file is too big") {
			return errTooBig
		}
		return fmt.Errorf("get file: %w", err)
	}
	if file.FileSize > limit {
		return errTooBig
	}
	if err := f.cfg.Telegram.DownloadToFile(ctx, file.FilePath, dst, limit); err != nil {
		return fmt.Errorf("download file: %w", err)
	}
	return nil
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

// removePreview deletes the message that shows a run before Generate, in
// the chat or the requester's own, and the run's panel.
func (f *Flow) removePreview(ctx context.Context, run db.ChatRun) {
	f.dropPanel(ctx, run)
	if run.PreviewEphemeral {
		f.deleteEphemeral(ctx, run.ChatID, run.UserID, run.PreviewMessageID)
		return
	}
	f.deleteMessage(ctx, run.ChatID, run.PreviewMessageID)
}

// dropPanel deletes the run's Model and Settings panel, if it has one.
func (f *Flow) dropPanel(ctx context.Context, run db.ChatRun) {
	if run.PanelMessageID == 0 {
		return
	}
	f.deleteEphemeral(ctx, run.ChatID, run.UserID, run.PanelMessageID)
	if err := f.cfg.Store.SetChatRunPanel(ctx, run.JobID, 0); err != nil {
		f.log.Warn("panel not cleared", "job", run.JobID, "error", err)
	}
}

func (f *Flow) deleteEphemeral(ctx context.Context, chatID, userID, ephemeralID int64) {
	if ephemeralID == 0 {
		return
	}
	// An ephemeral message may be gone already: Telegram lets them expire.
	if err := f.cfg.Telegram.DeleteEphemeralMessage(ctx, chatID, userID, ephemeralID); err != nil {
		f.log.Debug("ephemeral message not deleted", "chat", chatID, "message", ephemeralID, "error", err)
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
