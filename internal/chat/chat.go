// SPDX-License-Identifier: Apache-2.0

// Package chat runs /img and /edit in a chat (docs/design.md, `/img` and
// `/edit` in a group): the command becomes a draft job and a preview that
// replies to it,
// Generate queues the job and turns the preview into the generating loop, and
// the loop becomes the result or an error card. One message carries the run
// from start to end.
//
// The bot handles updates one at a time, so a command and a press run in
// goroutines of their own here, and the bot goes on to the next update. What
// the chat has been told is kept in framy.chat_run, so a restart picks every
// run up where it was.
package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/anim"
	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/storage"
)

// Telegram is what the flow calls: *tg.Client.
type Telegram interface {
	SendReply(ctx context.Context, chatID, replyTo int64, threadID int, text string, markup *tg.InlineKeyboardMarkup) (tg.Message, error)
	SendEphemeralMessage(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, text string, markup *tg.InlineKeyboardMarkup) (tg.Message, error)
	SendPhoto(ctx context.Context, chatID int64, photo tg.InputFile, opts tg.CaptionOptions) (tg.Message, error)
	SendAnimation(ctx context.Context, chatID int64, animation tg.InputFile, opts tg.AnimationOptions) (tg.Message, error)
	SendDocument(ctx context.Context, chatID int64, document tg.InputFile, opts tg.DocumentOptions) (tg.Message, error)
	SendVideo(ctx context.Context, chatID int64, video tg.InputFile, opts tg.VideoOptions) (tg.Message, error)
	SendMediaGroup(ctx context.Context, chatID int64, media []tg.InputMedia, threadID int) ([]tg.Message, error)
	EditMessageMediaMessage(ctx context.Context, chatID, messageID int64, media tg.InputMedia, markup *tg.InlineKeyboardMarkup) (tg.Message, error)
	EditMessageCaption(ctx context.Context, chatID, messageID int64, caption string, opts tg.EditCaptionOptions) (tg.Message, error)
	EditMessageReplyMarkup(ctx context.Context, chatID, messageID int64, markup *tg.InlineKeyboardMarkup) error
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
	EditEphemeralMessageMedia(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, media tg.InputMedia, markup *tg.InlineKeyboardMarkup) error
	EditEphemeralMessageReplyMarkup(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, markup *tg.InlineKeyboardMarkup) error
	SendRich(ctx context.Context, chatID int64, html string, opts tg.RichOptions) (tg.Message, error)
	EditMessageRich(ctx context.Context, chatID, messageID int64, html string, media []tg.RichMedia, markup *tg.InlineKeyboardMarkup, opts ...tg.RichOption) error
	DeleteEphemeralMessage(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64) error
	SendChatAction(ctx context.Context, chatID int64, threadID int, action string) error
	GetFile(ctx context.Context, fileID string) (tg.File, error)
	DownloadToFile(ctx context.Context, filePath, dst string, maxBytes int64) error
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
	AnswerCallbackQueryAlert(ctx context.Context, callbackID, text string) error
	AnswerInlineQuery(ctx context.Context, inlineQueryID string, answer tg.InlineAnswer) error
	SavePreparedInlineMessage(ctx context.Context, userID int64, result tg.InlineQueryResult, chats tg.PreparedChats) (tg.PreparedInlineMessage, error)
	AnswerGuestQuery(ctx context.Context, guestQueryID string, result tg.InlineQueryResult) (tg.SentGuestMessage, error)
	EditInlineMessageMedia(ctx context.Context, inlineMessageID string, media tg.InputMedia, markup *tg.InlineKeyboardMarkup) error
	EditInlineMessageCaption(ctx context.Context, inlineMessageID, caption string, opts tg.EditCaptionOptions) error
	EditInlineMessageTextWithPreview(ctx context.Context, inlineMessageID, text string, preview *tg.LinkPreviewOptions, markup *tg.InlineKeyboardMarkup) error
	EditInlineMessageReplyMarkup(ctx context.Context, inlineMessageID string, markup *tg.InlineKeyboardMarkup) error
}

// Store is framy's database: *db.Store.
type Store interface {
	Offer(ctx context.Context, id string) (catalog.Model, catalog.Offer, error)
	Catalog(ctx context.Context) ([]catalog.Model, error)
	Settings(ctx context.Context, userID int64) (db.Settings, error)
	UserLang(ctx context.Context, userID int64) (string, error)
	ChatLang(ctx context.Context, chatID int64) (string, error)
	Currency(ctx context.Context, userID int64) (string, error)
	Quote(ctx context.Context, code string, day time.Time) (fx.Quote, error)
	JobByKey(ctx context.Context, key string) (int64, bool, error)
	AddChatRun(ctx context.Context, r db.ChatRun) error
	ChatRun(ctx context.Context, jobID int64) (db.ChatRun, error)
	OpenChatRuns(ctx context.Context) ([]db.ChatRun, error)
	DeletedChatRuns(ctx context.Context, open bool) ([]db.ChatRun, error)
	DropChatRun(ctx context.Context, jobID int64) (bool, error)
	MoveChatRun(ctx context.Context, jobID int64, from, to string) (bool, error)
	SetChatRunPreview(ctx context.Context, jobID, messageID int64, ephemeral bool) error
	SetChatRunPanel(ctx context.Context, jobID, panelID int64) error
	SetChatRunInline(ctx context.Context, jobID int64, inlineMessageID string) error
	SetChatRunNote(ctx context.Context, jobID int64, note string) error
	ResultRun(ctx context.Context, chatID, messageID int64) (int64, bool, error)
	FoundResultMessage(ctx context.Context, jobID, messageID int64) (bool, error)
	StopChatRun(ctx context.Context, jobID int64) (bool, error)
	FinishChatRun(ctx context.Context, jobID int64, stage string, res db.ChatResult) (bool, error)
	SiteResults(ctx context.Context, since time.Time, limit int) ([]db.SiteResult, error)
	ClaimSiteResult(ctx context.Context, jobID int64) (bool, error)
	ReleaseSiteResult(ctx context.Context, jobID int64) error
	EndSiteResult(ctx context.Context, jobID int64, state string) error
	SentSiteResult(ctx context.Context, run db.ChatRun, res db.ChatResult) error
	InlineResults(ctx context.Context, user int64, search string, before int64, limit int) ([]db.InlineResult, error)
	ShareSource(ctx context.Context, userID, assetID int64) (db.ShareSource, error)
}

// Queue makes and works the jobs: *jobs.Queue.
type Queue interface {
	Draft(ctx context.Context, n jobs.New) (id int64, created bool, err error)
	Generate(ctx context.Context, id int64) error
	Revise(ctx context.Context, id int64, n jobs.New) error
	Cancel(ctx context.Context, id int64) error
}

// Files keeps the photos and the clip a run takes and reads its results:
// *storage.Store.
type Files interface {
	UploadCopy(ctx context.Context, userID int64, r io.Reader, copyTo string) (storage.Asset, error)
	KeepVideo(ctx context.Context, userID int64, path string, cut *media.Cut) (storage.Asset, error)
	Results(ctx context.Context, jobID int64) ([]storage.Asset, error)
	Joined(ctx context.Context, jobID int64) (storage.Asset, bool, error)
	JobInputs(ctx context.Context, jobID int64) ([]storage.Asset, error)
	Open(ctx context.Context, a storage.Asset) (io.ReadCloser, error)
}

// Clips reads the videos /edit takes and readies a video result for
// Telegram: *media.Converter.
type Clips interface {
	ProbeVideo(ctx context.Context, path string) (media.Video, error)
	Poster(ctx context.Context, dir string) (string, error)
	NormalizeVideo(ctx context.Context, dir string, cut *media.Cut) (media.Video, error)
}

// Allowance says how much of the free tier a person has left: *free.Allowance.
type Allowance interface {
	Usage(ctx context.Context, userID int64) (free.Usage, error)
}

// Connections lists a person's own keys: *keys.Service. A nil *keys.Service
// answers keys.ErrDisabled, which is no key.
type Connections interface {
	List(ctx context.Context, userID int64) ([]keys.Connection, error)
}

// Renderer draws the preview, the loop and the cards: *anim.Renderer.
type Renderer interface {
	Preview(ctx context.Context, dir string, p anim.Preview, paths []string) (anim.Media, error)
	Generating(ctx context.Context) (anim.Media, error)
	ErrorCard(ctx context.Context, text, lang string) (anim.Media, error)
}

// Work makes a directory for one command's files: media.Work.
type Work interface {
	Dir(name string) (dir string, done func(), err error)
}

// Config is what the flow needs. Every field but Log and Keys is required.
type Config struct {
	Telegram  Telegram
	Store     Store
	Queue     Queue
	Files     Files
	Allowance Allowance
	Keys      Connections
	Renderer  Renderer
	Clips     Clips
	Work      Work
	// Outbox makes directories on the volume framy shares with a local Bot
	// API server, at the server's own path (BOT_API_FILES_DIR): a result
	// written there is sent by its path, which the server reads from its
	// disk, instead of uploaded, which tg holds in memory whole (outbox.go).
	// Nil uploads. Its paths name the bot's token and never reach a log.
	Outbox Work
	Log    *slog.Logger
	// Commands is how many commands are worked at once: downloads, uploads
	// and the preview. 0 is 4.
	Commands int
	// MaxDownload caps a photo read from Telegram. 0 is storage's upload
	// limit.
	MaxDownload int64
	// MaxVideo caps a clip read from Telegram. 0 is storage's limit on a
	// clip.
	MaxVideo int64
	// AppURL is the site (SITE_URL), where Telegram fetches the pictures of
	// a guest run and of a shared result. Telegram takes only https; with
	// anything else it is empty.
	AppURL string
	// Stack is the bot this flow is (config.Config.Stack). It goes into the
	// key of a job a message starts: the DM of one person with two bots has
	// the same chat id, and each counts its message ids from one.
	Stack string
	// Username is the bot's, without the @. A mention of it in a plain
	// message of a DM is left out of the prompt, and a refusal outside
	// framy's direct chat links to that chat by it (settings.go). Empty
	// leaves the words as they came and the refusal without a link.
	Username string
}

// Flow runs /img and /edit in chats, and a plain message in a DM, and sends
// the results made on the site to the DM.
type Flow struct {
	cfg  Config
	log  *slog.Logger
	now  func() time.Time
	slot chan struct{}
	// stopping is closed once the process stops (Run's context ends).
	stopping chan struct{}
	stopOnce sync.Once
	albums   Albums
	// mention is framy's mention in a plain message (Config.Username).
	mention *regexp.Regexp

	// locks keeps a press and the delivery from changing one run at once.
	locksMu sync.Mutex
	locks   map[int64]*runLock
	// files are the file_ids of the pictures every run shares, by their
	// anim key: Telegram keeps the upload, and the next run sends the id.
	files sync.Map // anim key → file_id
	// ticks are when a running job last told its chat something; chats
	// when a chat last had the chat action and a caption, whichever run
	// they were of (deliver.go).
	ticks sync.Map // job id → *ticker
	chats sync.Map // chat id → *chatTick
	// retries are the runs whose result or card did not reach the chat,
	// and when each is tried again (deliver.go).
	retries sync.Map // job id → retry
	// ended are the runs whose result or card went out and whose end is not
	// recorded yet (deliver.go, finish).
	ended sync.Map // job id → ending
	// pace holds a chat Telegram asked to wait for (telegram.go); busy is
	// how many runs each chat has generating, as the last round read them.
	pace   *pace
	busyMu sync.Mutex
	busy   map[int64]int
	// made is when a button of a result or an error card last made a draft,
	// so a second tap makes no second one; kept for tapAgain.
	made marks // "<job>:<action>"
	// originals are the results whose file went out, once each; kept for
	// originalOnce.
	originals marks // job id
	// drafting are the keys of the commands being worked (claim).
	drafting sync.Map // dedupe key → struct{}
	// asks are the plain messages of a DM waiting for their answer, by the
	// question; dmAlbums the albums of a DM being collected.
	asks     sync.Map // askKey → ask
	dmAlbums sync.Map // albumKey → *dmAlbum
	// shareKey signs the links of shared results; shareSlots bounds the
	// pictures made for them at once.
	shareKey   [32]byte
	shareSlots chan struct{}
	// shareSeq makes each link of a guest run new (guestURL).
	shareSeq atomic.Int64
	// swept is when takeDown last looked at every run of a deleted job, not
	// only the open ones. Only the loop of Run reads and writes it.
	swept time.Time
	// byPath is set once a result went by its path, and uploadsOnly once
	// the server took one uploaded it had refused by its path (sendFiles).
	byPath, uploadsOnly atomic.Bool

	wg sync.WaitGroup
}

// New checks the config.
func New(cfg Config) (*Flow, error) {
	switch {
	case cfg.Telegram == nil:
		return nil, errors.New("chat: no Telegram")
	case cfg.Store == nil:
		return nil, errors.New("chat: no Store")
	case cfg.Queue == nil:
		return nil, errors.New("chat: no Queue")
	case cfg.Files == nil:
		return nil, errors.New("chat: no Files")
	case cfg.Allowance == nil:
		return nil, errors.New("chat: no Allowance")
	case cfg.Renderer == nil:
		return nil, errors.New("chat: no Renderer")
	case cfg.Clips == nil:
		return nil, errors.New("chat: no Clips")
	case cfg.Work == nil:
		return nil, errors.New("chat: no Work")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Commands <= 0 {
		cfg.Commands = 4
	}
	if cfg.MaxDownload <= 0 {
		cfg.MaxDownload = storage.DefaultMaxUpload
	}
	if cfg.MaxVideo <= 0 {
		cfg.MaxVideo = storage.DefaultMaxVideo
	}
	cfg.AppURL = miniAppURL(cfg.AppURL)
	p := newPace()
	cfg.Telegram = guarded{Telegram: cfg.Telegram, pace: p}
	f := &Flow{cfg: cfg, log: cfg.Log, now: time.Now, pace: p, slot: make(chan struct{}, cfg.Commands), stopping: make(chan struct{}),
		shareKey: newShareKey(), shareSlots: make(chan struct{}, shareSlots)}
	if name := strings.TrimPrefix(cfg.Username, "@"); name != "" {
		f.mention = regexp.MustCompile(`(?i)[ \t]*@` + regexp.QuoteMeta(name) + `\b[ \t]*`)
	}
	return f, nil
}

// Wait waits for the commands and presses in flight; the caller cancels
// their context first.
func (f *Flow) Wait() { f.wg.Wait() }

// The commands the bot passes here.
const (
	CommandImage = "img"
	CommandVideo = "edit"
)

// commandMotion is a video made from a prompt and at most one photo, its
// start frame: the answer Video to the question of a DM. No command of the
// bot's starts it.
const commandMotion = "motion"

// Command takes /img or /edit with the text after it and works it in the
// background. The bot has checked the command is framy's.
func (f *Flow) Command(ctx context.Context, m *tg.Message, command, prompt string) {
	if m == nil || m.From == nil {
		return
	}
	f.wg.Go(func() {
		f.withSlot(ctx, m, func() {
			if err := f.command(ctx, m, command, strings.TrimSpace(prompt)); err != nil && ctx.Err() == nil {
				f.log.Error("chat command failed", "chat", m.Chat.ID, "message", m.MessageID, "error", err)
			}
		})
	})
}

// Anonymous answers /img, /edit or framy's mention sent on behalf of a
// chat: by an anonymous admin, or by a person posting as a channel, whom
// Telegram does not name. A run is a person's, who pays for it and alone
// presses its buttons, so none is made; the chat is told why, and how to
// send it so it works. A key pasted after it is as public as anyone's, and
// goes as theirs does (keyInCommand): deleted, with the answer that says
// where keys go.
func (f *Flow) Anonymous(ctx context.Context, m *tg.Message) {
	if m == nil || m.From == nil {
		return
	}
	f.wg.Go(func() {
		if handled, err := f.keyInCommand(ctx, m, ""); handled {
			if err != nil && ctx.Err() == nil {
				f.log.Warn("an anonymous key not answered", "chat", m.Chat.ID, "error", err)
			}
			return
		}
		if err := f.reply(ctx, m, t(f.chatLang(ctx, m.Chat, *m.From), "run.anonymous")); err != nil && ctx.Err() == nil {
			f.log.Warn("an anonymous command not answered", "chat", m.Chat.ID, "error", err)
		}
	})
}

// Mention takes a group message that starts with framy's mention, the
// mention taken out: "@framyin_bot a fox" is /img a fox, as the same words
// are in a chat framy is not in (guest mode). A clip it carries or replies
// to is edited, as /edit would, since that is what the words can mean there.
func (f *Flow) Mention(ctx context.Context, m *tg.Message, prompt string) {
	if m == nil || m.From == nil {
		return
	}
	f.wg.Go(func() {
		f.withSlot(ctx, m, func() {
			if err := f.byName(ctx, m, strings.TrimSpace(prompt)); err != nil && ctx.Err() == nil {
				f.log.Error("chat mention failed", "chat", m.Chat.ID, "message", m.MessageID, "error", err)
			}
		})
	})
}

func (f *Flow) byName(ctx context.Context, m *tg.Message, prompt string) error {
	if prompt == "" {
		// The hint of /img names the command; this one the mention.
		return f.reply(ctx, m, t(f.langOf(ctx, m), "guest.empty"))
	}
	command := CommandImage
	if sourcesOf(m).clip != nil {
		command = CommandVideo
	}
	return f.command(ctx, m, command, prompt)
}

// withSlot runs work as one of the commands worked at once, and tells
// whether it did. A stop does not wait for a slot: the commands that have
// one go on for the bot's grace, and one still waiting when the stop comes,
// or that comes after it and finds none free, is not worked. Telegram has
// been told its update was taken, and gives it to no one again, so m, what
// it answers, is told to come back rather than left with nothing.
func (f *Flow) withSlot(ctx context.Context, m *tg.Message, work func()) bool {
	select {
	case f.slot <- struct{}{}:
	default:
		select {
		case f.slot <- struct{}{}:
		case <-ctx.Done():
			f.turnAway(ctx, m)
			return false
		case <-f.stopping:
			f.turnAway(ctx, m)
			return false
		}
	}
	defer func() { <-f.slot }()
	work()
	return true
}

// turnAwayWithin bounds the answer to a command a stop turned away.
const turnAwayWithin = 5 * time.Second

// turnAway tells the sender of m that framy is restarting and to send it
// again: in the chat, or in the answer to a guest message.
func (f *Flow) turnAway(ctx context.Context, m *tg.Message) {
	if m == nil || m.From == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), turnAwayWithin)
	defer cancel()
	text := t(f.langOf(ctx, m), "run.restarting")
	var err error
	if m.GuestQueryID != "" {
		err = f.guestText(ctx, m, text)
	} else {
		err = f.reply(ctx, m, text)
	}
	if err != nil {
		f.log.Warn("a command a stop turned away was not told", "chat", m.Chat.ID, "error", err)
		return
	}
	f.log.Info("a command a stop turned away was told to come back", "chat", m.Chat.ID)
}

// markStopping tells the commands waiting for a slot that the process stops.
func (f *Flow) markStopping() { f.stopOnce.Do(func() { close(f.stopping) }) }

// callbackPrefix starts the data of a run's buttons: "j:<job>:<action>".
// askPrefix starts the answers to the question of a DM:
// "d:<message>:<choice>".
const (
	callbackPrefix = "j:"
	askPrefix      = "d:"
)

// Actions a run's buttons carry. They are a contract with previews and
// results already sent. A model is picked with actPickModel and the hash of
// its id, a setting with actPickSetting and "<setting>#<hash of the value>"
// (settingData); older buttons number the value, "<setting>.<index>".
const (
	actGenerate    = "go"
	actNo          = "no"
	actStop        = "stop"
	actModel       = "m"
	actSettings    = "s"
	actBack        = "b"
	actPickModel   = "M"
	actPickSetting = "S"
	// actFamily lists a family's variants, by famHash; actLocked the
	// families that need a key the person has not connected.
	actFamily = "F"
	actLocked = "L"
	// actVideo turns a run of a prompt and at most one photo into a video
	// from them, the photo its start frame; actImage turns it back.
	actVideo = "v"
	actImage = "i"
	// actKeyClip takes as much of the clip as the model does, on the
	// person's key; actFreeClip its first seconds, on the free video.
	actKeyClip  = "k"
	actFreeClip = "f"
	// actAnimate, on an image result, makes the image the start frame of a
	// video.
	actAnimate = "a"
	// actAgain, on a result, makes a new draft of the run; actOriginal, on
	// an image that went as a photo, sends the file as the model made it.
	actAgain    = "g"
	actOriginal = "o"
	// On the error card: actRetry makes a new draft of the run, actOther
	// the same with the model list open, and actQuiet one without sound.
	actRetry = "r"
	actOther = "c"
	actQuiet = "q"
)

// Owns reports whether callback data is a run's button or an answer to the
// question of a DM.
func (f *Flow) Owns(data string) bool {
	return strings.HasPrefix(data, callbackPrefix) || strings.HasPrefix(data, askPrefix)
}

func callback(jobID int64, action string) string {
	return callbackPrefix + strconv.FormatInt(jobID, 10) + ":" + action
}

func parseCallback(data string) (jobID int64, action string, ok bool) {
	rest, found := strings.CutPrefix(data, callbackPrefix)
	if !found {
		return 0, "", false
	}
	id, action, found := strings.Cut(rest, ":")
	if !found || action == "" {
		return 0, "", false
	}
	jobID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || jobID <= 0 {
		return 0, "", false
	}
	return jobID, action, true
}

// Callback takes a press of a run's button and works it in the background.
func (f *Flow) Callback(ctx context.Context, cq *tg.CallbackQuery) {
	if cq == nil {
		return
	}
	// An inline message has a run's buttons only as a guest run's answer,
	// and never the question of a DM.
	if cq.Message == nil && (cq.InlineMessageID == "" || strings.HasPrefix(cq.Data, askPrefix)) {
		f.wg.Go(func() { _ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "") })
		return
	}
	f.wg.Go(func() {
		press := f.press
		if strings.HasPrefix(cq.Data, askPrefix) {
			press = f.answerAsk
		}
		if err := press(ctx, cq); err != nil && ctx.Err() == nil {
			f.log.Error("chat press failed", "data", cq.Data, "error", err)
		}
	})
}

// runLock is a run's mutex and how many hold it or wait for it. The last
// to let go removes it: a mutex dropped while held would let the next
// press work the run beside the one holding it, and one kept after its
// last use would stay for good, as a press on a result's button does.
type runLock struct {
	mu   sync.Mutex
	refs int
}

// marks remember when something was done, for as long as that matters
// (keep), and forget it after: every mark ever made would stay in memory
// until a restart otherwise.
type marks struct {
	mu    sync.Mutex
	at    map[string]time.Time
	swept time.Time
}

// put marks key as done at now, unless it is marked within keep already,
// and tells whether it marked it.
func (m *marks) put(key string, now time.Time, keep time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.at == nil {
		m.at = map[string]time.Time{}
	}
	if at, ok := m.at[key]; ok && now.Sub(at) < keep {
		return false
	}
	m.at[key] = now
	if now.Sub(m.swept) >= keep {
		for k, at := range m.at {
			if now.Sub(at) >= keep {
				delete(m.at, k)
			}
		}
		m.swept = now
	}
	return true
}

// within tells whether key was marked less than keep before now.
func (m *marks) within(key string, now time.Time, keep time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.at[key]
	return ok && now.Sub(at) < keep
}

// drop forgets key.
func (m *marks) drop(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.at, key)
}

// lock holds the run's mutex, and returns what lets it go.
func (f *Flow) lock(jobID int64) func() {
	l := f.takeLock(jobID)
	l.mu.Lock()
	return func() { l.mu.Unlock(); f.dropLock(jobID, l) }
}

// tryLock holds the run's mutex unless something holds it already.
func (f *Flow) tryLock(jobID int64) (unlock func(), ok bool) {
	l := f.takeLock(jobID)
	if !l.mu.TryLock() {
		f.dropLock(jobID, l)
		return nil, false
	}
	return func() { l.mu.Unlock(); f.dropLock(jobID, l) }, true
}

func (f *Flow) takeLock(jobID int64) *runLock {
	f.locksMu.Lock()
	defer f.locksMu.Unlock()
	if f.locks == nil {
		f.locks = map[int64]*runLock{}
	}
	l := f.locks[jobID]
	if l == nil {
		l = &runLock{}
		f.locks[jobID] = l
	}
	l.refs++
	return l
}

func (f *Flow) dropLock(jobID int64, l *runLock) {
	f.locksMu.Lock()
	defer f.locksMu.Unlock()
	if l.refs--; l.refs == 0 {
		delete(f.locks, jobID)
	}
}

// forget drops what the flow kept in memory of an ended run. Its lock goes
// by itself once nothing holds it.
func (f *Flow) forget(jobID int64) {
	f.ticks.Delete(jobID)
	f.retries.Delete(jobID)
	f.ended.Delete(jobID)
}

// personLang is the person's language in core, or their client's.
func (f *Flow) personLang(ctx context.Context, u tg.User) string {
	lang, err := f.cfg.Store.UserLang(ctx, u.ID)
	if err != nil {
		f.log.Warn("person language unread", "error", err)
	}
	if lang == "" {
		lang = u.LanguageCode
	}
	return lang
}

// chatLang is what a run in the chat is written in: the group's language,
// or the requester's while the group has none.
func (f *Flow) chatLang(ctx context.Context, chat tg.Chat, u tg.User) string {
	if chat.Type != "private" {
		lang, err := f.cfg.Store.ChatLang(ctx, chat.ID)
		if err != nil {
			f.log.Warn("chat language unread", "chat", chat.ID, "error", err)
		}
		if lang != "" {
			return lang
		}
	}
	return f.personLang(ctx, u)
}

// langOf is what a run of m is written in: the chat's, or the person's for
// a guest message, since framy keeps nothing of a chat it is not in, and
// its id may name another chat framy knows.
func (f *Flow) langOf(ctx context.Context, m *tg.Message) string {
	if m.GuestQueryID != "" {
		return f.personLang(ctx, *m.From)
	}
	return f.chatLang(ctx, m.Chat, *m.From)
}

// threadOf is the forum topic a message is in; 0 for none. A reply chain in
// an ordinary supergroup has a thread id too, and no topic to send into.
func threadOf(m *tg.Message) int {
	if m.IsTopicMessage {
		return m.MessageThreadID
	}
	return 0
}

// errorText is the reason a person reads for an error before a preview.
func errorText(lang string, err error) string {
	switch {
	case errors.Is(err, media.ErrUnsupported), errors.Is(err, media.ErrUnreadable):
		return t(lang, "run.unsupported")
	case errors.Is(err, media.ErrUnsupportedVideo), errors.Is(err, media.ErrUnreadableVideo):
		return t(lang, "edit.unsupported")
	case errors.Is(err, media.ErrTooLarge), errors.Is(err, media.ErrVideoTooLarge),
		errors.Is(err, storage.ErrTooBig), errors.Is(err, errTooBig):
		return t(lang, "run.too_big")
	}
	return t(lang, "run.failed")
}

// errTooBig is a file Telegram says is over the download limit.
var errTooBig = errors.New("the file is over the download limit")

func t(lang, key string, pairs ...string) string { return i18n.T(i18n.Resolve(lang), key, pairs...) }

// limitText says the free allowance of the run's kind is used up and how
// long until the next free run, for an alert, which takes no formatting.
func (f *Flow) limitText(lang string, e *free.LimitError) string {
	return t(lang, limitKey(e), "limit", strconv.Itoa(e.Limit), "wait", waitText(lang, e.FreesAt.Sub(f.now())))
}

// limitMessage is limitText for a message: the date and the time of the next
// free run, which Telegram shows in the reader's own time zone, in place of
// the wait.
func (f *Flow) limitMessage(lang string, e *free.LimitError) string {
	if e.FreesAt.IsZero() {
		return f.limitText(lang, e)
	}
	return t(lang, limitKey(e)+"_at", "limit", strconv.Itoa(e.Limit), "at", timeHTML(e.FreesAt))
}

func limitKey(e *free.LimitError) string {
	if e.Kind == catalog.Video {
		return "edit.limit"
	}
	return "run.limit"
}

// timeHTML is at as a date_time entity: the date and the time in the
// reader's own time zone and format, and in UTC on a client too old for it.
// It is rounded up to the minute, as waitText rounds, so it never shows a
// free run before it is there.
func timeHTML(at time.Time) string {
	if m := at.Truncate(time.Minute); m.Before(at) {
		at = m.Add(time.Minute)
	}
	return fmt.Sprintf(`<tg-time unix="%d" format="dt">%s</tg-time>`, at.Unix(), at.UTC().Format("2006-01-02 15:04 UTC"))
}

// closedText says the free tier is paused for runs of the kind.
func closedText(lang string, kind catalog.Kind) string {
	if kind == catalog.Video {
		return t(lang, "edit.closed")
	}
	return t(lang, "run.closed")
}

// waitText is a wait in minutes, in hours and minutes, or past a day in days
// and hours, rounded up, so it never promises a free run before it is there.
func waitText(lang string, d time.Duration) string {
	m := max(int((d+time.Minute-1)/time.Minute), 1)
	switch {
	case m < 60:
		return t(lang, "run.wait_m", "m", strconv.Itoa(m))
	case m < 24*60:
		return t(lang, "run.wait_hm", "h", strconv.Itoa(m/60), "m", strconv.Itoa(m%60))
	}
	h := (m + 59) / 60
	return t(lang, "run.wait_dh", "d", strconv.Itoa(h/24), "h", strconv.Itoa(h%24))
}

// reply answers the command with text. In a group only its sender sees it
// where the preview would go to them alone: the command was ephemeral, or
// framy is an admin, which may write to one member at any time. What went
// wrong with someone's command is theirs, and the chat would only read the
// noise. The chat sees it when Telegram refuses that; a reply that got no
// answer may have reached the sender, so the chat is not told it too.
func (f *Flow) reply(ctx context.Context, m *tg.Message, text string) error {
	return f.replyWith(ctx, m, text, nil)
}

// replyWith is reply with buttons under the text.
func (f *Flow) replyWith(ctx context.Context, m *tg.Message, text string, kb *tg.InlineKeyboardMarkup) error {
	if isQuiet(ctx) {
		// A reply to a result: the chat does not hear about it.
		f.log.Info("an answer to a reply left unsaid", "chat", m.Chat.ID)
		return nil
	}
	switch {
	case ephemeralCommand(m):
		_, err := f.cfg.Telegram.SendEphemeralMessage(ctx, m.Chat.ID, m.From.ID, m.EphemeralMessageID, text, kb)
		if err == nil {
			return nil
		}
		if !refused(err) {
			return fmt.Errorf("reply in %d: %w", m.Chat.ID, err)
		}
		f.log.Warn("ephemeral reply refused; the chat sees it", "chat", m.Chat.ID, "error", err)
	case m.Chat.Type != "private" && m.From != nil && !m.From.IsBot && f.isAdmin(ctx, m.Chat.ID):
		// An ephemeral message as an answer to an ordinary one is a rich
		// one; a rich message reads its HTML as a page does.
		_, err := f.cfg.Telegram.SendRich(ctx, m.Chat.ID, "<p>"+strings.ReplaceAll(text, "\n", "<br>")+"</p>", tg.RichOptions{
			ThreadID: threadOf(m), ReplyTo: m.MessageID, Ephemeral: &tg.Ephemeral{ReceiverUserID: m.From.ID}, Markup: kb,
		})
		if err == nil {
			return nil
		}
		if !refused(err) {
			return fmt.Errorf("reply in %d: %w", m.Chat.ID, err)
		}
		f.log.Warn("ephemeral reply refused; the chat sees it", "chat", m.Chat.ID, "error", err)
	}
	if _, err := f.cfg.Telegram.SendReply(ctx, m.Chat.ID, m.MessageID, threadOf(m), text, kb); err != nil {
		return fmt.Errorf("reply in %d: %w", m.Chat.ID, err)
	}
	return nil
}
