// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"cmp"
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/free"
)

// A plain message in a DM (docs/design.md, DM): no command is needed. Words,
// alone or with a photo, get a question, an image or a video, and the answer
// starts the same flow as /img: the words are the prompt and the photo the
// source, for a video its start frame. What can go only one way goes there
// at once: a clip to /edit, and an album to /img, since a video takes one
// start frame. A photo or a clip without words gets a line on what to send.

// Answers to the question, in its buttons' data.
const (
	askImage = "i"
	askVideo = "v"
)

// askTTL is how long a question keeps its message in memory. Past it, or
// after a restart, an answer reads the message from the question's reply,
// which carries the message but not what that message answered.
const askTTL = 10 * time.Minute

// albumForget is how long an album of a DM is remembered once answered, so
// an item that comes late starts nothing.
const albumForget = time.Minute

// askKey is a question: its chat and its message.
type askKey struct{ chat, question int64 }

// albumKey is an album of a chat.
type albumKey struct {
	chat  int64
	group string
}

// dmAlbum is an album of a DM being collected. It is answered once, when no
// item has come for a while, about its item with words, or its first.
type dmAlbum struct {
	mu           sync.Mutex
	first, words *tg.Message
}

// Plain answers a plain message of a DM in the background: one that is no
// command and no key, from a person.
func (f *Flow) Plain(ctx context.Context, m *tg.Message) {
	if m == nil || m.From == nil || m.From.IsBot || m.Chat.Type != "private" {
		return
	}
	if m.MediaGroupID != "" {
		f.plainAlbumItem(ctx, m)
		return
	}
	f.wg.Go(func() { f.answerPlain(ctx, m) })
}

func (f *Flow) answerPlain(ctx context.Context, m *tg.Message) {
	f.withSlot(ctx, func() {
		if err := f.plain(ctx, m); err != nil && ctx.Err() == nil {
			f.log.Error("plain message failed", "chat", m.Chat.ID, "message", m.MessageID, "error", err)
		}
	})
}

// plainAlbumItem collects an album of a DM. Telegram sends each item as its
// own message, and the words are on one of them: the first item waits for
// the album, as /img does, then the album is answered once.
func (f *Flow) plainAlbumItem(ctx context.Context, m *tg.Message) {
	k := albumKey{m.Chat.ID, m.MediaGroupID}
	v, loaded := f.dmAlbums.LoadOrStore(k, &dmAlbum{first: m})
	a := v.(*dmAlbum)
	a.mu.Lock()
	if a.words == nil && strings.TrimSpace(m.Caption) != "" {
		a.words = m
	}
	a.mu.Unlock()
	if loaded {
		return
	}
	f.wg.Go(func() {
		if f.albums != nil {
			if _, err := f.albums.Album(ctx, m.Chat.ID, m.MediaGroupID, albumQuiet, albumMax); err != nil && ctx.Err() == nil {
				f.log.Warn("album unread; answering the items that came", "chat", m.Chat.ID, "error", err)
			}
		}
		a.mu.Lock()
		pick := cmp.Or(a.words, a.first)
		a.mu.Unlock()
		time.AfterFunc(albumForget, func() { f.dmAlbums.Delete(k) })
		f.answerPlain(ctx, pick)
	})
}

// plain answers one plain message: with the question, with the flow it can
// only go to, or with what to send.
func (f *Flow) plain(ctx context.Context, m *tg.Message) error {
	m, err := f.ownSources(ctx, m)
	if err != nil {
		return err
	}
	prompt := strings.TrimSpace(cmp.Or(m.Text, m.Caption))
	src := sourcesOf(m)
	switch {
	case prompt == "" && src.clip == nil && len(src.photos) == 0:
		// A sticker, a voice or a service message: nothing to make from.
		return nil
	case prompt == "":
		return f.reply(ctx, m, t(f.personLang(ctx, *m.From), "dm.say_what"))
	case src.clip != nil:
		return f.command(ctx, m, CommandVideo, prompt)
	case src.group != "" || len(src.photos) > free.VideoHeyGen.MaxInputs:
		return f.command(ctx, m, CommandImage, prompt)
	}
	if _, seen, err := f.cfg.Store.JobByKey(ctx, dedupeKey(m)); err != nil || seen {
		// A replayed update of a message already answered.
		return err
	}
	lang := f.personLang(ctx, *m.From)
	data := askPrefix + strconv.FormatInt(m.MessageID, 10) + ":"
	kb := &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{
		button(t(lang, "dm.image"), data+askImage, ""),
		button(t(lang, "dm.video"), data+askVideo, ""),
	}}}
	sent, err := f.cfg.Telegram.SendReply(ctx, m.Chat.ID, m.MessageID, 0, t(lang, "dm.ask"), kb)
	if err != nil {
		return err
	}
	k := askKey{m.Chat.ID, sent.MessageID}
	f.asks.Store(k, m)
	time.AfterFunc(askTTL, func() { f.asks.Delete(k) })
	return nil
}

// ownSources leaves out what a message answers when that is framy's own
// message and not a result: a reply to a preview, a question or a panel
// takes none of its pictures, and one to a result changes it.
func (f *Flow) ownSources(ctx context.Context, m *tg.Message) (*tg.Message, error) {
	r := m.ReplyToMessage
	if r == nil || r.From == nil || !r.From.IsBot {
		return m, nil
	}
	if _, result, err := f.cfg.Store.ResultRun(ctx, m.Chat.ID, r.MessageID); err != nil || result {
		return m, err
	}
	out := *m
	out.ReplyToMessage = nil
	return &out, nil
}

// parseAsk reads an answer's data: "d:<message>:<answer>".
func parseAsk(data string) (messageID int64, answer string, ok bool) {
	rest, found := strings.CutPrefix(data, askPrefix)
	if !found {
		return 0, "", false
	}
	id, answer, found := strings.Cut(rest, ":")
	messageID, err := strconv.ParseInt(id, 10, 64)
	if !found || err != nil || messageID <= 0 || (answer != askImage && answer != askVideo) {
		return 0, "", false
	}
	return messageID, answer, true
}

// answerAsk deletes the question and starts what its answer says with the
// message it asked about.
func (f *Flow) answerAsk(ctx context.Context, cq *tg.CallbackQuery) error {
	messageID, answer, ok := parseAsk(cq.Data)
	q := cq.Message
	if !ok || q.Chat.Type != "private" || q.MessageID == 0 {
		return f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
	}
	var m *tg.Message
	if v, found := f.asks.LoadAndDelete(askKey{q.Chat.ID, q.MessageID}); found {
		m = v.(*tg.Message)
	} else if r := q.ReplyToMessage; r != nil {
		m = r
	}
	f.deleteMessage(ctx, q.Chat.ID, q.MessageID)
	if m == nil || m.MessageID != messageID || m.From == nil || m.From.ID != cq.From.ID {
		return f.cfg.Telegram.AnswerCallbackQueryAlert(ctx, cq.ID, t(f.personLang(ctx, cq.From), "dm.expired"))
	}
	_ = f.cfg.Telegram.AnswerCallbackQuery(ctx, cq.ID, "")
	command := CommandImage
	if answer == askVideo {
		command = commandMotion
	}
	var err error
	f.withSlot(ctx, func() { err = f.command(ctx, m, command, strings.TrimSpace(cmp.Or(m.Text, m.Caption))) })
	return err
}
