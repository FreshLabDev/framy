// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/anim"
	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/storage"
)

// call is one Telegram call the flow made.
type call struct {
	method    string
	chatID    int64
	messageID int64 // the message edited or deleted, or replied to
	thread    int
	text      string // text or caption
	markup    *tg.InlineKeyboardMarkup
	file      string // "upload:<name>" or the file_id sent
	action    string
	// video is what a video send said of it: "<w>x<h> <s>s", and
	// " streams" when it streams; cover is its cover, as file is.
	video, cover string
	// ephemeral is what an ephemeral send asked for; nil for one the chat
	// sees. replyEphemeral is the ephemeral message it answers.
	ephemeral      *tg.Ephemeral
	replyEphemeral int64
	// inline is the inline message an edit changed: a guest run's answer.
	inline string
}

type fakeTelegram struct {
	mu      sync.Mutex
	calls   []call
	next    int64
	fail    map[string]error // by method
	answers []string         // "text" or "alert:text"
	files   map[string]string
	// inlineAnswers are the inline answers, in order.
	inlineAnswers []tg.InlineAnswer
	// prepared are the results saved for shareMessage, in order.
	prepared []tg.InlineQueryResult
	// guestAnswers are the answers to guest messages, in order.
	guestAnswers []tg.InlineQueryResult
	// beforeEphemeral runs as an ephemeral send starts, as a stop would
	// come in the middle of one.
	beforeEphemeral func()
	// onVideo sees a video send before its files are read.
	onVideo func(tg.VideoOptions)
}

func newTelegram() *fakeTelegram {
	return &fakeTelegram{next: 100, fail: map[string]error{}, files: map[string]string{}}
}

// record keeps a call. An ephemeral send fails with fail["ephemeral"], and
// comes back with an ephemeral id instead of a message id; a send with a
// cover fails with fail["cover"].
func (f *fakeTelegram) record(c call) (tg.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.ephemeral != nil && f.beforeEphemeral != nil {
		f.beforeEphemeral()
	}
	if err := f.fail[c.method]; err != nil {
		return tg.Message{}, err
	}
	if err := f.fail["ephemeral"]; err != nil && c.ephemeral != nil {
		return tg.Message{}, err
	}
	if err := f.fail["cover"]; err != nil && c.cover != "" {
		return tg.Message{}, err
	}
	f.calls = append(f.calls, c)
	f.next++
	if c.ephemeral != nil {
		return tg.Message{EphemeralMessageID: f.next}, nil
	}
	return tg.Message{MessageID: f.next}, nil
}

func fileOf(file tg.InputFile) string {
	switch v := file.(type) {
	case tg.InputFileUpload:
		_, _ = io.ReadAll(v.Data)
		return "upload:" + v.Filename
	case tg.InputFileString:
		return v.Data
	}
	return "?"
}

func (f *fakeTelegram) SendReply(_ context.Context, chatID, replyTo int64, threadID int, text string, markup *tg.InlineKeyboardMarkup) (tg.Message, error) {
	return f.record(call{method: "sendMessage", chatID: chatID, messageID: replyTo, thread: threadID, text: text, markup: markup})
}

func (f *fakeTelegram) SendEphemeralMessage(_ context.Context, chatID, receiver, ephemeralID int64, text string, markup *tg.InlineKeyboardMarkup) (tg.Message, error) {
	return f.record(call{method: "sendMessage", chatID: chatID, text: text, markup: markup,
		ephemeral: &tg.Ephemeral{ReceiverUserID: receiver}, replyEphemeral: ephemeralID})
}

func (f *fakeTelegram) SendPhoto(_ context.Context, chatID int64, photo tg.InputFile, o tg.CaptionOptions) (tg.Message, error) {
	m, err := f.record(call{method: "sendPhoto", chatID: chatID, messageID: o.ReplyTo, thread: o.ThreadID, text: o.Caption, markup: o.Markup, file: fileOf(photo),
		ephemeral: o.Ephemeral, replyEphemeral: o.ReplyToEphemeral})
	m.Photo = []tg.Photo{{FileID: "small"}, {FileID: "photo-file", FileUniqueID: "photo-unique"}}
	return m, err
}

func (f *fakeTelegram) SendAnimation(_ context.Context, chatID int64, an tg.InputFile, o tg.AnimationOptions) (tg.Message, error) {
	m, err := f.record(call{method: "sendAnimation", chatID: chatID, messageID: o.ReplyTo, thread: o.ThreadID, text: o.Caption, markup: o.Markup, file: fileOf(an),
		ephemeral: o.Ephemeral, replyEphemeral: o.ReplyToEphemeral})
	m.Animation = &tg.Animation{FileID: "anim-file"}
	return m, err
}

func (f *fakeTelegram) SendDocument(_ context.Context, chatID int64, doc tg.InputFile, o tg.DocumentOptions) (tg.Message, error) {
	m, err := f.record(call{method: "sendDocument", chatID: chatID, messageID: o.ReplyTo, thread: o.ThreadID, text: o.Caption, markup: o.Markup, file: fileOf(doc)})
	m.Document = &tg.Document{FileID: "doc-file", FileUniqueID: "doc-unique"}
	return m, err
}

func (f *fakeTelegram) SendVideo(_ context.Context, chatID int64, v tg.InputFile, o tg.VideoOptions) (tg.Message, error) {
	if f.onVideo != nil {
		f.onVideo(o)
	}
	c := call{method: "sendVideo", chatID: chatID, messageID: o.ReplyTo, thread: o.ThreadID, text: o.Caption, file: fileOf(v),
		video: videoOf(o.Width, o.Height, o.Duration, o.SupportsStreaming)}
	if o.Cover != nil {
		c.cover = fileOf(o.Cover)
	}
	m, err := f.record(c)
	m.Video = &tg.Video{FileID: "video-file", FileUniqueID: "video-unique"}
	return m, err
}

func videoOf(w, h, secs int, streams bool) string {
	s := strconv.Itoa(w) + "x" + strconv.Itoa(h) + " " + strconv.Itoa(secs) + "s"
	if streams {
		s += " streams"
	}
	return s
}

func (f *fakeTelegram) EditMessageMediaMessage(_ context.Context, chatID, messageID int64, item tg.InputMedia, markup *tg.InlineKeyboardMarkup) (tg.Message, error) {
	c := call{method: "editMessageMedia", chatID: chatID, messageID: messageID, markup: markup}
	m := tg.Message{MessageID: messageID}
	switch v := item.(type) {
	case tg.InputMediaAnimation:
		c.text, c.file = v.Caption, "animation "+fileOf(v.Media)
		m.Animation = &tg.Animation{FileID: "loop-file"}
	case tg.InputMediaPhoto:
		c.text, c.file = v.Caption, "photo "+fileOf(v.Media)
		m.Photo = []tg.Photo{{FileID: "result-file", FileUniqueID: "result-unique"}}
	case tg.InputMediaVideo:
		c.text, c.file = v.Caption, "video "+fileOf(v.Media)
		c.video = videoOf(v.Width, v.Height, v.Duration, v.SupportsStreaming)
		m.Video = &tg.Video{FileID: "video-file", FileUniqueID: "video-unique"}
	}
	if _, err := f.record(c); err != nil {
		return tg.Message{}, err
	}
	return m, nil
}

func (f *fakeTelegram) EditMessageCaption(_ context.Context, chatID, messageID int64, caption string, o tg.EditCaptionOptions) (tg.Message, error) {
	return f.record(call{method: "editMessageCaption", chatID: chatID, messageID: messageID, text: caption, markup: o.Markup})
}

func (f *fakeTelegram) EditMessageReplyMarkup(_ context.Context, chatID, messageID int64, markup *tg.InlineKeyboardMarkup) error {
	_, err := f.record(call{method: "editMessageReplyMarkup", chatID: chatID, messageID: messageID, markup: markup})
	return err
}

func (f *fakeTelegram) DeleteMessage(_ context.Context, chatID, messageID int64) error {
	_, err := f.record(call{method: "deleteMessage", chatID: chatID, messageID: messageID})
	return err
}

// The ephemeral edits keep the receiver in action.
func (f *fakeTelegram) EditEphemeralMessageMedia(_ context.Context, chatID, receiver, id int64, item tg.InputMedia, markup *tg.InlineKeyboardMarkup) error {
	c := call{method: "editEphemeralMessageMedia", chatID: chatID, messageID: id, markup: markup, action: strconv.FormatInt(receiver, 10)}
	switch v := item.(type) {
	case tg.InputMediaAnimation:
		c.text, c.file = v.Caption, "animation "+fileOf(v.Media)
	case tg.InputMediaPhoto:
		c.text, c.file = v.Caption, "photo "+fileOf(v.Media)
	}
	_, err := f.record(c)
	return err
}

func (f *fakeTelegram) EditEphemeralMessageReplyMarkup(_ context.Context, chatID, receiver, id int64, markup *tg.InlineKeyboardMarkup) error {
	_, err := f.record(call{method: "editEphemeralMessageReplyMarkup", chatID: chatID, messageID: id, markup: markup, action: strconv.FormatInt(receiver, 10)})
	return err
}

func (f *fakeTelegram) DeleteEphemeralMessage(_ context.Context, chatID, receiver, id int64) error {
	_, err := f.record(call{method: "deleteEphemeralMessage", chatID: chatID, messageID: id, action: strconv.FormatInt(receiver, 10)})
	return err
}

// SendMediaGroup records an album as one call: file lists its items as
// "<type>:<file>", and text is the first item's caption. Each item comes
// back as a message of its own.
func (f *fakeTelegram) SendMediaGroup(_ context.Context, chatID int64, items []tg.InputMedia, threadID int) ([]tg.Message, error) {
	var files []string
	var caption string
	for i, item := range items {
		switch v := item.(type) {
		case tg.InputMediaPhoto:
			files = append(files, "photo:"+fileOf(v.Media))
			if i == 0 {
				caption = v.Caption
			}
		case tg.InputMediaDocument:
			files = append(files, "document:"+fileOf(v.Media))
			if i == 0 {
				caption = v.Caption
			}
		default:
			files = append(files, "?")
		}
	}
	m, err := f.record(call{method: "sendMediaGroup", chatID: chatID, thread: threadID, text: caption, file: strings.Join(files, ",")})
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []tg.Message{m}
	for range items[1:] {
		f.next++
		out = append(out, tg.Message{MessageID: f.next})
	}
	out[0].Photo = []tg.Photo{{FileID: "album-file", FileUniqueID: "album-unique"}}
	return out, nil
}

func (f *fakeTelegram) SendChatAction(_ context.Context, chatID int64, threadID int, action string) error {
	_, err := f.record(call{method: "sendChatAction", chatID: chatID, thread: threadID, action: action})
	return err
}

func (f *fakeTelegram) GetFile(_ context.Context, fileID string) (tg.File, error) {
	if _, err := f.record(call{method: "getFile", file: fileID}); err != nil {
		return tg.File{}, err
	}
	return tg.File{FileID: fileID, FilePath: "photos/" + fileID + ".jpg", FileSize: 1000}, nil
}

func (f *fakeTelegram) DownloadToFile(_ context.Context, filePath, dst string, _ int64) error {
	if _, err := f.record(call{method: "download", file: filePath}); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte("photo "+filePath), 0o600)
}

func (f *fakeTelegram) AnswerCallbackQuery(_ context.Context, _ string, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, text)
	return nil
}

// SavePreparedInlineMessage keeps the result in prepared and answers
// "prep-<n>", which ends a day later.
func (f *fakeTelegram) SavePreparedInlineMessage(_ context.Context, userID int64, result tg.InlineQueryResult, chats tg.PreparedChats) (tg.PreparedInlineMessage, error) {
	if _, err := f.record(call{method: "savePreparedInlineMessage", chatID: userID}); err != nil {
		return tg.PreparedInlineMessage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if chats != tg.AnyChat {
		return tg.PreparedInlineMessage{}, fmt.Errorf("chats %+v", chats)
	}
	f.prepared = append(f.prepared, result)
	return tg.PreparedInlineMessage{ID: fmt.Sprintf("prep-%d", len(f.prepared)), ExpirationDate: clock.Add(24 * time.Hour).Unix()}, nil
}

// AnswerGuestQuery keeps the answer whole in guestAnswers; the inline
// message it becomes is "inline-<query>".
func (f *fakeTelegram) AnswerGuestQuery(_ context.Context, id string, result tg.InlineQueryResult) (tg.SentGuestMessage, error) {
	if _, err := f.record(call{method: "answerGuestQuery", text: id}); err != nil {
		return tg.SentGuestMessage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guestAnswers = append(f.guestAnswers, result)
	return tg.SentGuestMessage{InlineMessageID: "inline-" + id}, nil
}

func (f *fakeTelegram) EditInlineMessageMedia(_ context.Context, id string, item tg.InputMedia, markup *tg.InlineKeyboardMarkup) error {
	c := call{method: "editInlineMessageMedia", inline: id, markup: markup}
	switch v := item.(type) {
	case tg.InputMediaAnimation:
		c.text, c.file = v.Caption, "animation "+fileOf(v.Media)
	case tg.InputMediaPhoto:
		c.text, c.file = v.Caption, "photo "+fileOf(v.Media)
	case tg.InputMediaVideo:
		c.text, c.file = v.Caption, "video "+fileOf(v.Media)
		c.video = videoOf(v.Width, v.Height, v.Duration, v.SupportsStreaming)
	}
	_, err := f.record(c)
	return err
}

func (f *fakeTelegram) EditInlineMessageCaption(_ context.Context, id, caption string, o tg.EditCaptionOptions) error {
	_, err := f.record(call{method: "editInlineMessageCaption", inline: id, text: caption, markup: o.Markup})
	return err
}

func (f *fakeTelegram) EditInlineMessageReplyMarkup(_ context.Context, id string, markup *tg.InlineKeyboardMarkup) error {
	_, err := f.record(call{method: "editInlineMessageReplyMarkup", inline: id, markup: markup})
	return err
}

// AnswerInlineQuery keeps the answer whole in inlineAnswers.
func (f *fakeTelegram) AnswerInlineQuery(_ context.Context, id string, answer tg.InlineAnswer) error {
	if _, err := f.record(call{method: "answerInlineQuery", text: id}); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inlineAnswers = append(f.inlineAnswers, answer)
	return nil
}

func (f *fakeTelegram) AnswerCallbackQueryAlert(_ context.Context, _ string, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, "alert:"+text)
	return nil
}

// methods are the calls of a method, in order.
func (f *fakeTelegram) methods(method string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTelegram) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.answers = nil, nil
}

// fakeJob is a job as the fake queue keeps it.
type fakeJob struct {
	n      jobs.New
	status jobs.Status
	class  jobs.Class
}

type fakeStore struct {
	mu       sync.Mutex
	jobs     map[int64]*fakeJob
	runs     map[int64]*db.ChatRun
	userLang map[int64]string
	chatLang map[int64]string
	currency map[int64]string
	quotes   map[string]fx.Quote
	keys     map[string]int64
	offers   map[string]catalog.Offer
	// models are the catalog: Offer finds an offer's model here, and
	// Catalog lists them. hidden are offers the catalog does not offer.
	models []catalog.Model
	hidden map[string]bool
	now    func() time.Time
	// site are the site's done jobs SiteResults lists, and delivered how
	// far each was sent; siteSince is what the last SiteResults was asked.
	site      []db.SiteResult
	delivered map[int64]string
	siteSince time.Time
	// results are what each finished run recorded of its result message.
	results map[int64]db.ChatResult
	// inline are the person's results InlineResults lists, newest first,
	// and inlineAsked what it was last asked.
	inline      []db.InlineResult
	inlineAsked inlineAsk
	// shares are the results ShareSource finds, by asset id.
	shares map[int64]db.ShareSource
	// shareOwner is whose each of shares is.
	shareOwner map[int64]int64
}

// ShareSource finds the person's result in s.shares.
func (s *fakeStore) ShareSource(_ context.Context, userID, assetID int64) (db.ShareSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.shares[assetID]
	if !ok || s.shareOwner[assetID] != userID {
		return db.ShareSource{}, db.ErrNoResult
	}
	return src, nil
}

type inlineAsk struct {
	user, before int64
	search       string
	limit        int
}

// InlineResults pages s.inline as the database would; finding the search
// in the prompts is the database's, and tested there.
func (s *fakeStore) InlineResults(_ context.Context, user int64, search string, before int64, limit int) ([]db.InlineResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inlineAsked = inlineAsk{user: user, before: before, search: search, limit: limit}
	var out []db.InlineResult
	for _, r := range s.inline {
		if (before == 0 || r.JobID < before) && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func newStore() *fakeStore {
	return &fakeStore{
		jobs: map[int64]*fakeJob{}, runs: map[int64]*db.ChatRun{},
		userLang: map[int64]string{}, chatLang: map[int64]string{}, currency: map[int64]string{},
		quotes: map[string]fx.Quote{}, keys: map[string]int64{}, offers: map[string]catalog.Offer{},
		delivered: map[int64]string{}, results: map[int64]db.ChatResult{},
	}
}

func (s *fakeStore) SiteResults(_ context.Context, since time.Time, limit int) ([]db.SiteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.siteSince = since
	var out []db.SiteResult
	for _, r := range s.site {
		if _, ok := s.delivered[r.JobID]; !ok && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeStore) ClaimSiteResult(_ context.Context, jobID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.delivered[jobID]; ok {
		return false, nil
	}
	s.delivered[jobID] = db.DeliverySending
	return true, nil
}

func (s *fakeStore) ReleaseSiteResult(_ context.Context, jobID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delivered[jobID] == db.DeliverySending {
		delete(s.delivered, jobID)
	}
	return nil
}

func (s *fakeStore) EndSiteResult(_ context.Context, jobID int64, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delivered[jobID] == db.DeliverySending {
		s.delivered[jobID] = state
	}
	return nil
}

func (s *fakeStore) SentSiteResult(_ context.Context, run db.ChatRun, res db.ChatResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delivered[run.JobID] == db.DeliverySending {
		s.delivered[run.JobID] = db.DeliverySent
	}
	if _, ok := s.runs[run.JobID]; !ok {
		s.runs[run.JobID] = &db.ChatRun{JobID: run.JobID, ChatID: run.ChatID, Lang: run.Lang, Stage: db.StageDone, ResultMessageID: res.MessageID}
		s.results[run.JobID] = res
	}
	return nil
}

func (s *fakeStore) delivery(jobID int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered[jobID]
}

func (s *fakeStore) Offer(_ context.Context, id string) (catalog.Model, catalog.Offer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offer(id)
}

func (s *fakeStore) offer(id string) (catalog.Model, catalog.Offer, error) {
	if s.hidden[id] {
		return catalog.Model{}, catalog.Offer{}, db.ErrNoOffer
	}
	for _, m := range s.models {
		for _, o := range m.Offers {
			if o.ID == id {
				return m, o, nil
			}
		}
	}
	o, ok := s.offers[id]
	if !ok {
		_, model, _ := strings.Cut(id, ":")
		o = catalog.Offer{ID: id, ModelID: model, Provider: catalog.OpenRouter, Kind: catalog.Image}
	}
	return catalog.Model{ID: o.ModelID, Name: "MAI Image 2.6 Flash", Maker: "Microsoft AI", Kind: catalog.Image}, o, nil
}

func (s *fakeStore) Catalog(context.Context) ([]catalog.Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.models), nil
}

func (s *fakeStore) UserLang(_ context.Context, id int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userLang[id], nil
}

func (s *fakeStore) ChatLang(_ context.Context, id int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chatLang[id], nil
}

func (s *fakeStore) Currency(_ context.Context, id int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currency[id], nil
}

func (s *fakeStore) Quote(_ context.Context, code string, _ time.Time) (fx.Quote, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if code == fx.USD {
		return fx.Dollar, nil
	}
	q, ok := s.quotes[code]
	if !ok {
		return fx.Quote{}, fx.ErrNoRate
	}
	return q, nil
}

func (s *fakeStore) JobByKey(_ context.Context, key string) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.keys[key]
	return id, ok, nil
}

func (s *fakeStore) AddChatRun(_ context.Context, r db.ChatRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[r.JobID]; !ok {
		r.Stage = db.StagePreview
		s.runs[r.JobID] = &r
	}
	return nil
}

func (s *fakeStore) ChatRun(_ context.Context, jobID int64) (db.ChatRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(jobID)
}

func (s *fakeStore) read(jobID int64) (db.ChatRun, error) {
	r, ok := s.runs[jobID]
	if !ok {
		return db.ChatRun{}, db.ErrNoRun
	}
	out := *r
	if j := s.jobs[jobID]; j != nil {
		out.UserID, out.Prompt, out.Free, out.Status, out.ErrorClass = j.n.UserID, j.n.Prompt, j.n.Free, string(j.status), string(j.class)
		out.Kind = string(j.n.Offer.Kind)
		m, _, _ := s.offer(j.n.Offer.ID)
		out.Model, out.OfferID = m.Name, j.n.Offer.ID
		// As the database gives them back: numbers from JSON.
		out.Settings = map[string]any{}
		for k, v := range j.n.Settings {
			if n, ok := v.(int); ok {
				v = float64(n)
			}
			out.Settings[k] = v
		}
	}
	return out, nil
}

func (s *fakeStore) SetChatRunPreview(_ context.Context, jobID, messageID int64, ephemeral bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[jobID]; ok {
		r.PreviewMessageID, r.PreviewEphemeral, r.PanelMessageID = messageID, ephemeral, 0
	}
	return nil
}

func (s *fakeStore) SetChatRunInline(_ context.Context, jobID int64, inlineMessageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[jobID]; ok {
		r.InlineMessageID = inlineMessageID
	}
	return nil
}

func (s *fakeStore) SetChatRunPanel(_ context.Context, jobID, panelID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[jobID]; ok {
		r.PanelMessageID = panelID
	}
	return nil
}

func (s *fakeStore) OpenChatRuns(context.Context) ([]db.ChatRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []db.ChatRun
	for id, r := range s.runs {
		if r.Stage == db.StagePreview || r.Stage == db.StageGenerating {
			run, _ := s.read(id)
			out = append(out, run)
		}
	}
	slices.SortFunc(out, func(a, b db.ChatRun) int { return int(a.JobID - b.JobID) })
	return out, nil
}

func (s *fakeStore) MoveChatRun(_ context.Context, jobID int64, from, to string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[jobID]
	if !ok || r.Stage != from {
		return false, nil
	}
	r.Stage = to
	if to == db.StageGenerating {
		r.GenerateAt = s.now()
	}
	return true, nil
}

func (s *fakeStore) SetChatRunNote(_ context.Context, jobID int64, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[jobID]; ok {
		r.Note = note
	}
	return nil
}

func (s *fakeStore) ResultRun(_ context.Context, chatID, messageID int64) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.runs {
		if r.ChatID == chatID && r.ResultMessageID != 0 && r.ResultMessageID == messageID {
			return id, true, nil
		}
	}
	return 0, false, nil
}

func (s *fakeStore) StopChatRun(_ context.Context, jobID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[jobID]
	if !ok || r.Stage != db.StageGenerating {
		return false, nil
	}
	r.Stage, r.Stopped = db.StageGone, true
	return true, nil
}

func (s *fakeStore) FinishChatRun(_ context.Context, jobID int64, stage string, res db.ChatResult) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[jobID]
	if !ok || r.Stage != db.StageGenerating {
		return false, nil
	}
	r.Stage, r.ResultMessageID = stage, res.MessageID
	s.results[jobID] = res
	return true, nil
}

func (s *fakeStore) stage(jobID int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[jobID]; ok {
		return r.Stage
	}
	return ""
}

func (s *fakeStore) setStatus(jobID int64, st jobs.Status, class jobs.Class) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[jobID].status, s.jobs[jobID].class = st, class
}

// fakeQueue makes jobs in the fake store; refuse fails a free draft.
type fakeQueue struct {
	store     *fakeStore
	next      int64
	refuse    error
	generate  error
	cancelErr error
	cancelled []int64
	revised   []jobs.New
}

// Revise changes a draft as the queue does; refuse fails a free one.
func (q *fakeQueue) Revise(_ context.Context, id int64, n jobs.New) error {
	if n.Free && q.refuse != nil {
		return q.refuse
	}
	q.store.mu.Lock()
	defer q.store.mu.Unlock()
	j := q.store.jobs[id]
	if j == nil || j.status != jobs.Draft {
		return jobs.ErrNotDraft
	}
	q.revised = append(q.revised, n)
	j.n.Offer, j.n.Settings, j.n.Free, j.n.Estimate, j.n.FX = n.Offer, n.Settings, n.Free, n.Estimate, n.FX
	if n.Inputs != nil {
		j.n.Inputs = n.Inputs
	}
	return nil
}

func (q *fakeQueue) Draft(_ context.Context, n jobs.New) (int64, bool, error) {
	if n.Free && q.refuse != nil {
		return 0, false, q.refuse
	}
	q.store.mu.Lock()
	defer q.store.mu.Unlock()
	if id, ok := q.store.keys[n.DedupeKey]; ok {
		return id, false, nil
	}
	q.next++
	q.store.jobs[q.next] = &fakeJob{n: n, status: jobs.Draft}
	q.store.keys[n.DedupeKey] = q.next
	return q.next, true, nil
}

func (q *fakeQueue) Generate(_ context.Context, id int64) error {
	if q.generate != nil {
		return q.generate
	}
	q.store.mu.Lock()
	defer q.store.mu.Unlock()
	j := q.store.jobs[id]
	if j.status != jobs.Draft {
		return jobs.ErrNotDraft
	}
	j.status = jobs.Queued
	return nil
}

func (q *fakeQueue) Cancel(_ context.Context, id int64) error {
	if q.cancelErr != nil {
		return q.cancelErr
	}
	q.store.mu.Lock()
	defer q.store.mu.Unlock()
	q.cancelled = append(q.cancelled, id)
	if j := q.store.jobs[id]; j != nil && (j.status == jobs.Draft || j.status == jobs.Queued) {
		j.status = jobs.Cancelled
	}
	return nil
}

// fakeFiles keeps uploads in memory; results maps a job to its files.
type fakeFiles struct {
	mu      sync.Mutex
	next    int64
	uploads int
	fail    error
	results map[int64][]storage.Asset
	// blobs are what Open reads of an object, by its key; any other reads
	// "result".
	blobs map[string][]byte
	// store has the jobs, whose inputs JobInputs reads.
	store  *fakeStore
	assets map[int64]storage.Asset
	// clips says how long a clip is; cuts are what KeepVideo was asked to
	// cut, nil for the whole clip; clipBytes is the size of a kept clip.
	clips     *fakeClips
	cuts      []*media.Cut
	clipBytes int64
}

func (f *fakeFiles) KeepVideo(_ context.Context, userID int64, path string, cut *media.Cut) (storage.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return storage.Asset{}, f.fail
	}
	if _, err := os.Stat(path); err != nil {
		return storage.Asset{}, err
	}
	f.cuts = append(f.cuts, cut)
	secs := f.clips.length()
	if cut != nil {
		secs = min(secs, cut.Seconds)
	}
	f.next++
	f.uploads++
	a := storage.Asset{ID: f.next, UserID: userID, Kind: "upload", ContentType: "video/mp4", Width: 1280, Height: 720,
		Seconds: secs, Bytes: max(f.clipBytes, 1<<20)}
	if f.assets == nil {
		f.assets = map[int64]storage.Asset{}
	}
	f.assets[a.ID] = a
	return a, nil
}

func (f *fakeFiles) JobInputs(_ context.Context, jobID int64) ([]storage.Asset, error) {
	f.store.mu.Lock()
	var ids []int64
	if j := f.store.jobs[jobID]; j != nil {
		ids = j.n.Inputs
	}
	f.store.mu.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []storage.Asset
	for _, id := range ids {
		out = append(out, f.assets[id])
	}
	return out, nil
}

func (f *fakeFiles) UploadCopy(_ context.Context, userID int64, r io.Reader, copyTo string) (storage.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return storage.Asset{}, f.fail
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return storage.Asset{}, err
	}
	if err := os.WriteFile(copyTo, body, 0o600); err != nil {
		return storage.Asset{}, err
	}
	f.next++
	f.uploads++
	a := storage.Asset{ID: f.next, UserID: userID, Kind: "upload", ContentType: "image/jpeg", Width: 1024, Height: 768}
	if f.assets == nil {
		f.assets = map[int64]storage.Asset{}
	}
	f.assets[a.ID] = a
	return a, nil
}

func (f *fakeFiles) Results(_ context.Context, jobID int64) ([]storage.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.results[jobID], nil
}

func (f *fakeFiles) Open(_ context.Context, a storage.Asset) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.blobs[a.Key]; ok {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	return io.NopCloser(strings.NewReader("result")), nil
}

// fakeAllowance has left free images; videosUsed is the free videos used.
type fakeAllowance struct{ left, videosUsed int }

func (a fakeAllowance) Usage(context.Context, int64) (free.Usage, error) {
	u := free.Usage{
		Images: free.Meter{Limit: free.ImagesPerWindow, Used: free.ImagesPerWindow - a.left},
		Videos: free.Meter{Limit: free.VideosPerWindow, Used: a.videosUsed, Window: free.VideoWindow},
	}
	if a.videosUsed >= free.VideosPerWindow {
		u.Videos.FreesAt = clock.Add(50 * time.Hour)
	}
	return u, nil
}

// fakeClips reads every clip as seconds long at 1280×720, and makes a
// result fit for Telegram by copying it.
type fakeClips struct {
	mu         sync.Mutex
	seconds    float64
	probeErr   error
	normErr    error
	posterErr  error
	normalized int
	// poster is the size of the frame Poster writes; zero is 16x9.
	poster image.Point
}

func (c *fakeClips) length() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seconds
}

func (c *fakeClips) ProbeVideo(_ context.Context, path string) (media.Video, error) {
	if c.probeErr != nil {
		return media.Video{}, c.probeErr
	}
	if _, err := os.Stat(path); err != nil {
		return media.Video{}, err
	}
	return media.Video{Path: path, ContentType: "video/mp4", Width: 1280, Height: 720, Seconds: c.length()}, nil
}

// Poster writes a grey frame of c.poster as a PNG.
func (c *fakeClips) Poster(_ context.Context, dir string) (string, error) {
	c.mu.Lock()
	err, size := c.posterErr, c.poster
	c.mu.Unlock()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, media.In)); err != nil {
		return "", err
	}
	if size == (image.Point{}) {
		size = image.Pt(16, 9)
	}
	img := image.NewGray(image.Rectangle{Max: size})
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "poster.png")
	return path, os.WriteFile(path, buf.Bytes(), 0o600)
}

func (c *fakeClips) NormalizeVideo(_ context.Context, dir string, _ *media.Cut) (media.Video, error) {
	c.mu.Lock()
	c.normalized++
	err := c.normErr
	c.mu.Unlock()
	if err != nil {
		return media.Video{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, media.In))
	if err != nil {
		return media.Video{}, err
	}
	out := filepath.Join(dir, "out.mp4")
	return media.Video{Path: out, ContentType: "video/mp4", Width: 1080, Height: 1920, Seconds: 4.2, Copied: true},
		os.WriteFile(out, b, 0o600)
}

type fakeKeys struct{ conns []keys.Connection }

func (k fakeKeys) List(context.Context, int64) ([]keys.Connection, error) { return k.conns, nil }

// fakeRenderer writes small files where the real one would draw.
type fakeRenderer struct {
	dir      string
	previews []anim.Preview
	albums   [][]string // the photos of each preview
	cards    []string
	// pictures: the cards are PNGs that decode, small, for a test that
	// serves them.
	pictures bool
}

func (r *fakeRenderer) file(name string) string {
	path := filepath.Join(r.dir, name)
	_ = os.WriteFile(path, r.bytesOf(name), 0o600)
	return path
}

// bytesOf is a picture's content: its name, or a small PNG with pictures.
func (r *fakeRenderer) bytesOf(name string) []byte {
	if !r.pictures || strings.HasSuffix(name, ".mp4") {
		return []byte(name)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 64, 36)))
	return buf.Bytes()
}

func (r *fakeRenderer) Preview(_ context.Context, dir string, p anim.Preview, paths []string) (anim.Media, error) {
	r.previews = append(r.previews, p)
	r.albums = append(r.albums, paths)
	path := filepath.Join(dir, "preview.png")
	if err := os.WriteFile(path, r.bytesOf("preview"), 0o600); err != nil {
		return anim.Media{}, err
	}
	return anim.Media{Path: path, ContentType: "image/png", Width: 1280, Height: 720, TextDrawn: p.Label != "" && p.Lang != "ja"}, nil
}

func (r *fakeRenderer) Generating(context.Context) (anim.Media, error) {
	return anim.Media{Path: r.file("loop.mp4"), ContentType: "video/mp4", Width: 1280, Height: 720, Duration: 7200 * time.Millisecond, Key: "loop-1"}, nil
}

func (r *fakeRenderer) ErrorCard(_ context.Context, text, lang string) (anim.Media, error) {
	r.cards = append(r.cards, text)
	return anim.Media{Path: r.file("card.png"), ContentType: "image/png", Width: 1280, Height: 720, Key: "card", TextDrawn: lang != "ja"}, nil
}

// fakeAlbums is the bot's album cache. With gate, an album is read once
// the gate closes, as the real one waits for the album to come.
type fakeAlbums struct {
	items []db.AlbumItem
	admin bool
	gate  chan struct{}
}

func (a fakeAlbums) Album(context.Context, int64, string, time.Duration, time.Duration) ([]db.AlbumItem, error) {
	if a.gate != nil {
		<-a.gate
	}
	return a.items, nil
}

func (a fakeAlbums) SeesEveryMessage(context.Context, int64) (bool, error) { return a.admin, nil }

// clock is the fake now: tests move it.
var clock = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

type harness struct {
	flow     *Flow
	tg       *fakeTelegram
	store    *fakeStore
	queue    *fakeQueue
	files    *fakeFiles
	renderer *fakeRenderer
	clips    *fakeClips
	now      time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store := newStore()
	clips := &fakeClips{seconds: 4}
	h := &harness{
		tg: newTelegram(), store: store, queue: &fakeQueue{store: store},
		files:    &fakeFiles{results: map[int64][]storage.Asset{}, store: store, clips: clips},
		renderer: &fakeRenderer{dir: t.TempDir()}, clips: clips, now: clock,
	}
	f, err := New(Config{
		Telegram: h.tg, Store: store, Queue: h.queue, Files: h.files, Allowance: fakeAllowance{left: 5},
		Renderer: h.renderer, Clips: clips, Work: media.Work{Root: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.now = func() time.Time { return h.now }
	store.now = f.now
	h.flow = f
	return h
}

// command runs a command to its end.
func (h *harness) command(m *tg.Message, command, prompt string) error {
	return h.flow.command(context.Background(), m, command, prompt)
}

// press presses a button of the run's preview, wherever it is.
func (h *harness) press(jobID int64, from int64, action string) error {
	run, err := h.store.ChatRun(context.Background(), jobID)
	if err != nil {
		return err
	}
	msg := tg.Message{MessageID: run.PreviewMessageID, Chat: tg.Chat{ID: run.ChatID, Type: "supergroup"}, Date: 1,
		Photo: []tg.Photo{{FileID: "small"}, {FileID: "preview-file"}}}
	if run.PreviewEphemeral {
		msg.MessageID, msg.EphemeralMessageID = 0, run.PreviewMessageID
	}
	return h.pressOn(jobID, from, action, msg)
}

// pressPanel presses a button of the requester's panel.
func (h *harness) pressPanel(jobID int64, from int64, action string) error {
	run, err := h.store.ChatRun(context.Background(), jobID)
	if err != nil {
		return err
	}
	return h.pressOn(jobID, from, action, tg.Message{EphemeralMessageID: run.PanelMessageID, Chat: tg.Chat{ID: run.ChatID, Type: "supergroup"}, Date: 1,
		Photo: []tg.Photo{{FileID: "preview-file"}}})
}

func (h *harness) pressOn(jobID, from int64, action string, msg tg.Message) error {
	cq := &tg.CallbackQuery{ID: "cq", From: tg.User{ID: from, LanguageCode: "en"}, Data: callback(jobID, action), Message: &msg}
	return h.flow.press(context.Background(), cq)
}

var group = tg.Chat{ID: -100, Type: "supergroup"}

func command(text string) *tg.Message {
	return &tg.Message{MessageID: 10, Chat: group, From: &tg.User{ID: 7, LanguageCode: "en"}, Text: text}
}

func photoReply(text string) *tg.Message {
	m := command(text)
	m.ReplyToMessage = &tg.Message{MessageID: 9, Chat: group, Photo: []tg.Photo{
		{FileID: "small", Width: 90, Height: 60}, {FileID: "big", Width: 1280, Height: 853, FileSize: 200_000},
	}}
	return m
}

var errBoom = errors.New("boom")
