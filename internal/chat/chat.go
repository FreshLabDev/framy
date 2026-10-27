// SPDX-License-Identifier: Apache-2.0

// Package chat runs /img in a chat (docs/design.md, `/img` and `/edit` in a
// group): the command becomes a draft job and a preview that replies to it,
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
	"strconv"
	"strings"
	"sync"
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
	EditMessageMediaMessage(ctx context.Context, chatID, messageID int64, media tg.InputMedia, markup *tg.InlineKeyboardMarkup) (tg.Message, error)
	EditMessageCaption(ctx context.Context, chatID, messageID int64, caption string, opts tg.EditCaptionOptions) (tg.Message, error)
	EditMessageReplyMarkup(ctx context.Context, chatID, messageID int64, markup *tg.InlineKeyboardMarkup) error
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
	EditEphemeralMessageMedia(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, media tg.InputMedia, markup *tg.InlineKeyboardMarkup) error
	EditEphemeralMessageReplyMarkup(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, markup *tg.InlineKeyboardMarkup) error
	DeleteEphemeralMessage(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64) error
	SendChatAction(ctx context.Context, chatID int64, threadID int, action string) error
	GetFile(ctx context.Context, fileID string) (tg.File, error)
	DownloadToFile(ctx context.Context, filePath, dst string, maxBytes int64) error
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
	AnswerCallbackQueryAlert(ctx context.Context, callbackID, text string) error
}

// Store is framy's database: *db.Store.
type Store interface {
	Offer(ctx context.Context, id string) (catalog.Model, catalog.Offer, error)
	Catalog(ctx context.Context) ([]catalog.Model, error)
	UserLang(ctx context.Context, userID int64) (string, error)
	ChatLang(ctx context.Context, chatID int64) (string, error)
	Currency(ctx context.Context, userID int64) (string, error)
	Quote(ctx context.Context, code string, day time.Time) (fx.Quote, error)
	JobByKey(ctx context.Context, key string) (int64, bool, error)
	AddChatRun(ctx context.Context, r db.ChatRun) error
	ChatRun(ctx context.Context, jobID int64) (db.ChatRun, error)
	OpenChatRuns(ctx context.Context) ([]db.ChatRun, error)
	MoveChatRun(ctx context.Context, jobID int64, from, to string) (bool, error)
	SetChatRunPreview(ctx context.Context, jobID, messageID int64, ephemeral bool) error
	SetChatRunPanel(ctx context.Context, jobID, panelID int64) error
	StopChatRun(ctx context.Context, jobID int64) (bool, error)
	FinishChatRun(ctx context.Context, jobID int64, stage string, res db.ChatResult) (bool, error)
}

// Queue makes and works the jobs: *jobs.Queue.
type Queue interface {
	Draft(ctx context.Context, n jobs.New) (id int64, created bool, err error)
	Generate(ctx context.Context, id int64) error
	Revise(ctx context.Context, id int64, n jobs.New) error
	Cancel(ctx context.Context, id int64) error
}

// Files keeps the photos a run takes and reads its results: *storage.Store.
type Files interface {
	UploadCopy(ctx context.Context, userID int64, r io.Reader, copyTo string) (storage.Asset, error)
	Results(ctx context.Context, jobID int64) ([]storage.Asset, error)
	JobInputs(ctx context.Context, jobID int64) ([]storage.Asset, error)
	Open(ctx context.Context, a storage.Asset) (io.ReadCloser, error)
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
	Work      Work
	Log       *slog.Logger
	// Commands is how many commands are worked at once: downloads, uploads
	// and the preview. 0 is 4.
	Commands int
	// MaxDownload caps a file read from Telegram. 0 is storage's upload
	// limit.
	MaxDownload int64
}

// Flow runs /img in chats.
type Flow struct {
	cfg    Config
	log    *slog.Logger
	now    func() time.Time
	slot   chan struct{}
	albums Albums

	// locks keeps a press and the delivery from changing one run at once.
	locks sync.Map // job id → *sync.Mutex
	// files are the file_ids of the pictures every run shares, by their
	// anim key: Telegram keeps the upload, and the next run sends the id.
	files sync.Map // anim key → file_id
	// ticks are when a running job last told its chat something.
	ticks sync.Map // job id → *ticker

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
	return &Flow{cfg: cfg, log: cfg.Log, now: time.Now, slot: make(chan struct{}, cfg.Commands)}, nil
}

// Wait waits for the commands and presses in flight; the caller cancels
// their context first.
func (f *Flow) Wait() { f.wg.Wait() }

// The commands the bot passes here.
const (
	CommandImage = "img"
	CommandVideo = "edit"
)

// Command takes /img or /edit with the text after it and works it in the
// background. The bot has checked the command is framy's.
func (f *Flow) Command(ctx context.Context, m *tg.Message, command, prompt string) {
	if m == nil || m.From == nil {
		return
	}
	f.wg.Go(func() {
		select {
		case f.slot <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-f.slot }()
		if err := f.command(ctx, m, command, strings.TrimSpace(prompt)); err != nil && ctx.Err() == nil {
			f.log.Error("chat command failed", "chat", m.Chat.ID, "message", m.MessageID, "error", err)
		}
	})
}

// callbackPrefix starts the data of a run's buttons: "j:<job>:<action>".
const callbackPrefix = "j:"

// Actions a run's buttons carry. They are a contract with previews already
// sent. A model is picked with actPickModel and the hash of its id, a
// setting with actPickSetting and "<setting>.<value>", numbered as the
// panel lists them.
const (
	actGenerate    = "go"
	actNo          = "no"
	actStop        = "stop"
	actModel       = "m"
	actSettings    = "s"
	actBack        = "b"
	actPickModel   = "M"
	actPickSetting = "S"
)

// Owns reports whether callback data is a run's button.
func (f *Flow) Owns(data string) bool { return strings.HasPrefix(data, callbackPrefix) }

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
	f.wg.Go(func() {
		if err := f.press(ctx, cq); err != nil && ctx.Err() == nil {
			f.log.Error("chat press failed", "chat", cq.Message.Chat.ID, "data", cq.Data, "error", err)
		}
	})
}

// lock is the run's mutex.
func (f *Flow) lock(jobID int64) func() {
	v, _ := f.locks.LoadOrStore(jobID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// forget drops what the flow kept in memory of an ended run.
func (f *Flow) forget(jobID int64) {
	f.locks.Delete(jobID)
	f.ticks.Delete(jobID)
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
	case errors.Is(err, media.ErrTooLarge), errors.Is(err, storage.ErrTooBig), errors.Is(err, errTooBig):
		return t(lang, "run.too_big")
	}
	return t(lang, "run.failed")
}

// errTooBig is a file Telegram says is over the download limit.
var errTooBig = errors.New("the file is over the download limit")

func t(lang, key string, pairs ...string) string { return i18n.T(i18n.Resolve(lang), key, pairs...) }

// reply answers the command with text: an ephemeral command only its
// sender sees, while Telegram takes the reply.
func (f *Flow) reply(ctx context.Context, m *tg.Message, text string) error {
	if ephemeralCommand(m) {
		_, err := f.cfg.Telegram.SendEphemeralMessage(ctx, m.Chat.ID, m.From.ID, m.EphemeralMessageID, text, nil)
		if err == nil {
			return nil
		}
		f.log.Warn("ephemeral reply refused; the chat sees it", "chat", m.Chat.ID, "error", err)
	}
	if _, err := f.cfg.Telegram.SendReply(ctx, m.Chat.ID, m.MessageID, threadOf(m), text, nil); err != nil {
		return fmt.Errorf("reply in %d: %w", m.Chat.ID, err)
	}
	return nil
}
