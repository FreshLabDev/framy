// SPDX-License-Identifier: Apache-2.0
package bot

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/metrics"
)

// Telegram sends every photo of an album as a message of its own, and a
// reply holds only the photo that was answered. So the bot keeps the items of
// albums as they pass (docs/design.md, `/f` in a group, Albums) and /f reads
// the rest back. It sees them all in a direct chat, and in a group only while
// it is an admin: Telegram hides the other messages from a bot in privacy
// mode, and admin rights open them without any right to act.
//
// db.AlbumItem is the row's type and the bot imports it for that, rather than
// the store importing the bot for a type of the bot's: the store stays a leaf
// that knows nothing of Telegram's flow.

const (
	// albumKeep is how long an item stays, counted from the first time the bot
	// saw it. The design says 48 hours.
	albumKeep       = 48 * time.Hour
	albumSweepEvery = time.Hour
	// arrivalKeep is how long the bot remembers when an album last grew. An
	// album is done arriving within seconds, so a minute is more than enough.
	arrivalKeep = time.Minute
	// arrivalSweepEvery bounds the cost of forgetting: the map is cleaned at
	// most this often, and holds the albums of the last minute and a bit.
	arrivalSweepEvery = 10 * time.Second
)

type albumKey struct {
	chat  int64
	group string
}

// arrivals is when each album last got an item, in memory. Telegram does not
// say how many items an album has or which one is last, so the only sign that
// it is complete is that no new item came for a moment.
type arrivals struct {
	mu    sync.Mutex
	last  map[albumKey]time.Time
	swept time.Time
}

func newArrivals() *arrivals { return &arrivals{last: map[albumKey]time.Time{}} }

// mark records an arrival and forgets the albums nothing came to for a
// minute, so the map holds the recent albums and no more.
func (a *arrivals) mark(k albumKey, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.last[k] = now
	if now.Sub(a.swept) < arrivalSweepEvery {
		return
	}
	a.swept = now
	for old, at := range a.last {
		if now.Sub(at) > arrivalKeep {
			delete(a.last, old)
		}
	}
}

// at is when the album last got an item; zero when it got none in the last
// minute or so.
func (a *arrivals) at(k albumKey) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last[k]
}

// albumItem is what the cache keeps of a message: the file to fetch again,
// and never the text. It reports false for a message that is not in an album,
// is not in a direct chat, a group or a supergroup, or carries nothing /f
// could use.
func albumItem(m *tg.Message) (db.AlbumItem, bool) {
	if m.MediaGroupID == "" {
		return db.AlbumItem{}, false
	}
	switch m.Chat.Type {
	case "private", "group", "supergroup":
	default:
		return db.AlbumItem{}, false
	}
	it := db.AlbumItem{
		ChatID:       m.Chat.ID,
		MediaGroupID: m.MediaGroupID,
		MessageID:    m.MessageID,
		SentAt:       time.Unix(m.Date, 0).UTC(),
	}
	if m.From != nil {
		it.UserID = m.From.ID
	}
	switch {
	case m.Animation != nil:
		// Telegram fills Document with a GIF as well. An animation cannot be
		// part of an album, and is no photo to edit.
		return db.AlbumItem{}, false
	case m.LivePhoto != nil:
		// A live photo's own file is its video. The still frame is what an
		// edit takes, and Telegram repeats its sizes in Photo.
		sizes := m.LivePhoto.Photo
		if len(sizes) == 0 {
			sizes = m.Photo
		}
		if !pickPhoto(&it, sizes) {
			return db.AlbumItem{}, false
		}
	case len(m.Photo) > 0:
		if !pickPhoto(&it, m.Photo) {
			return db.AlbumItem{}, false
		}
	case m.Video != nil:
		v := m.Video
		it.Kind, it.FileID, it.FileUniqueID = db.AlbumVideo, v.FileID, v.FileUniqueID
		it.Width, it.Height, it.FileSize = v.Width, v.Height, v.FileSize
	case m.Document != nil && imageOrVideo(m.Document.MimeType):
		d := m.Document
		it.Kind, it.FileID, it.FileUniqueID, it.FileSize = db.AlbumDocument, d.FileID, d.FileUniqueID, d.FileSize
	default:
		return db.AlbumItem{}, false
	}
	return it, it.FileID != "" && it.FileUniqueID != ""
}

// pickPhoto takes the largest size, by pixels. Telegram lists the sizes from
// the smallest, but the order is not part of its contract.
func pickPhoto(it *db.AlbumItem, sizes []tg.Photo) bool {
	best := -1
	for i, p := range sizes {
		if p.FileID == "" {
			continue
		}
		if best < 0 || p.Width*p.Height >= sizes[best].Width*sizes[best].Height {
			best = i
		}
	}
	if best < 0 {
		return false
	}
	p := sizes[best]
	it.Kind, it.FileID, it.FileUniqueID = db.AlbumPhoto, p.FileID, p.FileUniqueID
	it.Width, it.Height, it.FileSize = p.Width, p.Height, p.FileSize
	return true
}

func imageOrVideo(mime string) bool {
	mime = strings.ToLower(mime)
	return strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/")
}

// noteAlbumItem keeps the item a message carries, command or not. A failure
// is logged and counted and never stops the update: /f will take fewer
// photos and the preview says how many it took.
//
// A direct message that looks like a key is deleted on arrival and nothing
// from it is kept, so it is not noted either.
func (b *Bot) noteAlbumItem(ctx context.Context, m *tg.Message) {
	it, ok := albumItem(m)
	if !ok || m.Chat.Type == "private" && keys.LooksLikeKey(m.Text, m.Caption) {
		return
	}
	// Marked before the write as well as after it: an Album call that comes
	// between the two must wait for this item, not read the album without it.
	k := albumKey{it.ChatID, it.MediaGroupID}
	b.arrivals.mark(k, time.Now())
	err := b.store.NoteAlbumItem(ctx, it)
	b.arrivals.mark(k, time.Now())
	if err != nil {
		metrics.AlbumFailures.Inc()
		b.log.Error("album item not saved", "chat", it.ChatID, "message", it.MessageID, "error", err)
	}
}

// Album returns the items of an album in chatID, waiting until no new item
// has arrived for quiet (1.5 s in production), at most max in all. An album
// that stopped growing earlier, or that the bot has not seen grow in the last
// minute, is read at once.
//
// The update loop keeps noting items while Album waits, so the flow of /f
// calls it from a goroutine of its own. Called from inside the loop it would
// hold the loop up, and nothing new could arrive meanwhile. It returns what
// the bot saw, which is the whole album only when the bot sees every message
// of the chat (SeesEveryMessage).
func (b *Bot) Album(ctx context.Context, chatID int64, mediaGroupID string, quiet, max time.Duration) ([]db.AlbumItem, error) {
	k := albumKey{chatID, mediaGroupID}
	deadline := time.Now().Add(max)
	for {
		last := b.arrivals.at(k)
		now := time.Now()
		wake := last.Add(quiet)
		if last.IsZero() || !wake.After(now) || !now.Before(deadline) {
			break
		}
		if wake.After(deadline) {
			wake = deadline
		}
		if !wait(ctx, wake.Sub(now)) {
			return nil, ctx.Err()
		}
	}
	return b.store.AlbumItems(ctx, chatID, mediaGroupID)
}

// SeesEveryMessage reports whether the bot receives every message of the
// chat, which is what a whole album needs. A direct chat always does. A group
// does while the bot is an admin or its creator, with no right needed; as a
// member in privacy mode it sees only commands and replies to it. The status
// is asked of Telegram on every call: rights change, and nothing is cached.
func (b *Bot) SeesEveryMessage(ctx context.Context, chatID int64) (bool, error) {
	// Telegram numbers people from 1 up and groups and channels below zero.
	if chatID > 0 {
		return true, nil
	}
	return b.isAdmin(ctx, chatID, b.selfID)
}

// sweepAlbums deletes the items older than albumKeep, at start and then every
// `every`, until ctx ends.
func (b *Bot) sweepAlbums(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		n, err := b.store.DeleteAlbumItemsBefore(ctx, time.Now().Add(-albumKeep))
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			b.log.Error("album cache not swept", "error", err)
		default:
			b.log.Debug("album cache swept", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
