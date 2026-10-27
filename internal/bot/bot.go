// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/metrics"
)

type Store interface {
	Offset(context.Context) (int64, error)
	AdvanceOffset(context.Context, int64) error
	Touch(context.Context, tg.User, *tg.Chat) error
	UserLang(context.Context, int64) (string, error)
	ChatLang(context.Context, int64) (string, error)
	SetUserLang(context.Context, int64, string) error
	ClearUserLang(context.Context, int64) error
	SetChatLang(context.Context, int64, string) error
	ClearChatLang(context.Context, int64) error
	NoteAlbumItem(context.Context, db.AlbumItem) error
	AlbumItems(ctx context.Context, chatID int64, mediaGroupID string) ([]db.AlbumItem, error)
	DeleteAlbumItemsBefore(context.Context, time.Time) (int64, error)
	// Currency is the one the person chose, empty while they chose none.
	Currency(context.Context, int64) (string, error)
}

// Allowance is what the home panel of a direct chat reads of the free
// allowance: *free.Allowance.
type Allowance interface {
	Usage(ctx context.Context, userID int64) (free.Usage, error)
}

// Connections is what the home panel reads of a person's provider keys:
// *keys.Service. A nil *keys.Service answers keys.ErrDisabled, which is a
// framy with nothing connected.
type Connections interface {
	List(ctx context.Context, userID int64) ([]keys.Connection, error)
}

type Telegram interface {
	DeleteWebhook(context.Context) error
	GetUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]tg.Update, error)
	SendMessage(ctx context.Context, chatID int64, text string, markup *tg.InlineKeyboardMarkup) (tg.Message, error)
	EditMessageText(ctx context.Context, chatID, messageID int64, text string, markup *tg.InlineKeyboardMarkup) error
	SendEphemeralMessage(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, text string, markup *tg.InlineKeyboardMarkup) (tg.Message, error)
	EditEphemeralMessageText(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64, text string, markup *tg.InlineKeyboardMarkup) error
	DeleteEphemeralMessage(ctx context.Context, chatID, receiverUserID, ephemeralMessageID int64) error
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
	AnswerCallbackQueryAlert(ctx context.Context, callbackID, text string) error
	GetChatMember(ctx context.Context, chatID, userID int64) (tg.ChatMember, error)
	SetMyCommandsForScope(ctx context.Context, commands []tg.BotCommand, scope *tg.BotCommandScope) error
	SetChatMenuButton(ctx context.Context, chatID int64, button tg.MenuButton) error
	SetMyDescription(ctx context.Context, description, languageCode string) error
	SetMyShortDescription(ctx context.Context, shortDescription, languageCode string) error
	CallGet(ctx context.Context, method string, values url.Values, out any) error
}

type Bot struct {
	store       Store
	tg          Telegram
	log         *slog.Logger
	username    string
	selfID      int64
	version     string
	arrivals    *arrivals
	allowance   Allowance
	connections Connections
	// appURL is where the Mini App opens, from the home panel's button and
	// from the menu button. Empty: framy has none to offer.
	appURL string
	// readTimeout bounds what the home panel reads about the person.
	readTimeout time.Duration
	lastPoll    atomic.Int64
	initialized atomic.Bool
}

// Option gives the bot what the home panel of a direct chat reads or opens.
// Without one, the panel leaves that line or that button out.
type Option func(*Bot)

// WithAllowance shows the person what is left of the free allowance.
func WithAllowance(a Allowance) Option { return func(b *Bot) { b.allowance = a } }

// WithConnections shows which providers the person has connected.
func WithConnections(c Connections) Option { return func(b *Bot) { b.connections = c } }

// WithSiteURL is the site's origin (SITE_URL). The home panel gets an "Open
// framy" button to it, and the menu button of a direct chat opens it as a Mini
// App. Telegram takes only https for either, so any other address leaves both
// out.
func WithSiteURL(site string) Option { return func(b *Bot) { b.appURL = site } }

// New takes the username and the id Preflight returned: the username tells a
// command for framy from one for another bot in the same group, and the id is
// what the bot's own rights in a chat are asked by. The build's version is for
// the About card.
func New(store Store, client Telegram, log *slog.Logger, username string, selfID int64, version string, opts ...Option) *Bot {
	b := &Bot{
		store: store, tg: client, log: log, username: username, selfID: selfID, version: version,
		arrivals: newArrivals(), readTimeout: homeReadTimeout,
	}
	for _, opt := range opts {
		opt(b)
	}
	if b.appURL != "" && !isHTTPS(b.appURL) {
		log.Warn("the site address is not https, so the Open framy button and the menu button are off")
		b.appURL = ""
	}
	return b
}

func isHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// LastPoll is when getUpdates last answered, for /healthz.
func (b *Bot) LastPoll() time.Time {
	if unix := b.lastPoll.Load(); unix != 0 {
		return time.Unix(unix, 0)
	}
	return time.Time{}
}

func (b *Bot) Initialized() bool {
	return b.initialized.Load()
}

func (b *Bot) Run(ctx context.Context) error {
	if !b.initialize(ctx) {
		return nil
	}
	// The profile is dozens of calls; polling does not wait for them.
	var side sync.WaitGroup
	side.Go(func() { b.publishProfile(ctx) })
	side.Go(func() { b.sweepAlbums(ctx, albumSweepEvery) })
	side.Go(func() { b.publishMenuButton(ctx) })
	defer side.Wait()

	offset, err := b.store.Offset(ctx)
	if err != nil {
		// Starting from 0 replays at most the updates Telegram still holds.
		// Refusing to start would be the worse failure.
		b.log.Error("telegram offset load failed; starting from the oldest pending update", "error", err)
		offset = 0
	}
	var pollFailures int
	for ctx.Err() == nil {
		updates, err := b.tg.GetUpdates(ctx, offset, 25)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			metrics.PollFailures.Inc()
			pollFailures++
			delay := retryDelay(pollFailures)
			if pollFailures <= 3 || pollFailures%10 == 0 {
				b.log.Error("telegram polling failed", "error", err, "retry_in", delay)
			}
			if !wait(ctx, delay) {
				return nil
			}
			continue
		}
		if pollFailures > 0 {
			b.log.Info("telegram polling recovered", "after_failures", pollFailures)
			pollFailures = 0
		}
		b.lastPoll.Store(time.Now().Unix())
		if len(updates) == 0 {
			continue
		}
		for _, u := range updates {
			b.handle(ctx, u)
		}
		if ctx.Err() != nil {
			return nil
		}
		offset = updates[len(updates)-1].UpdateID + 1
		if err := b.store.AdvanceOffset(ctx, offset); err != nil {
			// The in-memory offset still moves; a restart before the next
			// successful write replays the batch.
			b.log.Error("telegram offset persist failed", "offset", offset, "error", err)
		}
	}
	return nil
}

// initialize drops any webhook, since getUpdates fails while one is set. It
// retries until it succeeds or ctx ends, and reports which of the two happened.
func (b *Bot) initialize(ctx context.Context) bool {
	for attempt := 1; ctx.Err() == nil; attempt++ {
		if err := b.tg.DeleteWebhook(ctx); err != nil {
			b.log.Warn("telegram initialization failed", "step", "deleteWebhook", "attempt", attempt, "error", err)
			if !wait(ctx, retryDelay(attempt)) {
				return false
			}
			continue
		}
		b.initialized.Store(true)
		return true
	}
	return false
}

// handle records who wrote in core, then answers. A failed touch does not
// stop the answer: an ephemeral reply must go out within seconds, and the
// next update touches again.
func (b *Bot) handle(ctx context.Context, u tg.Update) {
	// An item of an album is kept whoever sent it and whatever else the
	// message is, so before anything that could return early. A failure to
	// keep it is logged and counted there and stops nothing.
	switch {
	case u.Message != nil:
		b.noteAlbumItem(ctx, u.Message)
	case u.EditedMessage != nil:
		// An edit can swap the file of an item, and nothing else about it is
		// handled: no touch, no command.
		b.noteAlbumItem(ctx, u.EditedMessage)
		return
	}
	var (
		user *tg.User
		chat *tg.Chat
	)
	switch {
	case u.Message != nil:
		// Channel posts carry no sender.
		user, chat = u.Message.From, &u.Message.Chat
	case u.Callback != nil:
		user, chat = &u.Callback.From, &u.Callback.Message.Chat
	case u.MyChatMember != nil:
		user, chat = &u.MyChatMember.From, &u.MyChatMember.Chat
	}
	if user == nil || user.ID == 0 {
		return
	}
	if err := b.store.Touch(ctx, *user, chat); err != nil {
		metrics.TouchFailures.Inc()
		b.log.Error("core touch failed", "update_id", u.UpdateID, "error", err)
	}
	var err error
	switch {
	case u.Message != nil:
		err = b.onMessage(ctx, u.Message)
	case u.Callback != nil:
		err = b.onCallback(ctx, u.Callback)
	}
	if err != nil {
		metrics.UpdatesFailed.Inc()
		b.log.Error("update failed", "update_id", u.UpdateID, "error", err)
		return
	}
	metrics.UpdatesHandled.Inc()
}

func retryDelay(attempt int) time.Duration {
	delay := time.Duration(attempt) * time.Second
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}

func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
