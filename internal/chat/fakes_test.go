// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
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
	reaction  string
	action    string
}

type fakeTelegram struct {
	mu      sync.Mutex
	calls   []call
	next    int64
	fail    map[string]error // by method
	answers []string         // "text" or "alert:text"
	files   map[string]string
}

func newTelegram() *fakeTelegram {
	return &fakeTelegram{next: 100, fail: map[string]error{}, files: map[string]string{}}
}

func (f *fakeTelegram) record(c call) (tg.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[c.method]; err != nil {
		return tg.Message{}, err
	}
	f.calls = append(f.calls, c)
	f.next++
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

func (f *fakeTelegram) SendPhoto(_ context.Context, chatID int64, photo tg.InputFile, o tg.CaptionOptions) (tg.Message, error) {
	m, err := f.record(call{method: "sendPhoto", chatID: chatID, messageID: o.ReplyTo, thread: o.ThreadID, text: o.Caption, markup: o.Markup, file: fileOf(photo)})
	m.Photo = []tg.Photo{{FileID: "small"}, {FileID: "photo-file", FileUniqueID: "photo-unique"}}
	return m, err
}

func (f *fakeTelegram) SendAnimation(_ context.Context, chatID int64, an tg.InputFile, o tg.AnimationOptions) (tg.Message, error) {
	m, err := f.record(call{method: "sendAnimation", chatID: chatID, messageID: o.ReplyTo, thread: o.ThreadID, text: o.Caption, markup: o.Markup, file: fileOf(an)})
	m.Animation = &tg.Animation{FileID: "anim-file"}
	return m, err
}

func (f *fakeTelegram) SendDocument(_ context.Context, chatID int64, doc tg.InputFile, o tg.DocumentOptions) (tg.Message, error) {
	m, err := f.record(call{method: "sendDocument", chatID: chatID, messageID: o.ReplyTo, thread: o.ThreadID, text: o.Caption, file: fileOf(doc)})
	m.Document = &tg.Document{FileID: "doc-file", FileUniqueID: "doc-unique"}
	return m, err
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
	}
	if _, err := f.record(c); err != nil {
		return tg.Message{}, err
	}
	return m, nil
}

func (f *fakeTelegram) EditMessageCaption(_ context.Context, chatID, messageID int64, caption string, o tg.EditCaptionOptions) (tg.Message, error) {
	return f.record(call{method: "editMessageCaption", chatID: chatID, messageID: messageID, text: caption, markup: o.Markup})
}

func (f *fakeTelegram) DeleteMessage(_ context.Context, chatID, messageID int64) error {
	_, err := f.record(call{method: "deleteMessage", chatID: chatID, messageID: messageID})
	return err
}

func (f *fakeTelegram) SetMessageReaction(_ context.Context, chatID, messageID int64, r []tg.ReactionType) error {
	c := call{method: "setMessageReaction", chatID: chatID, messageID: messageID}
	if len(r) > 0 {
		c.reaction = r[0].Emoji
	}
	_, err := f.record(c)
	return err
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
	now      func() time.Time
}

func newStore() *fakeStore {
	return &fakeStore{
		jobs: map[int64]*fakeJob{}, runs: map[int64]*db.ChatRun{},
		userLang: map[int64]string{}, chatLang: map[int64]string{}, currency: map[int64]string{},
		quotes: map[string]fx.Quote{}, keys: map[string]int64{}, offers: map[string]catalog.Offer{},
	}
}

func (s *fakeStore) Offer(_ context.Context, id string) (catalog.Model, catalog.Offer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.offers[id]
	if !ok {
		o = catalog.Offer{ID: id, Provider: catalog.OpenRouter, Kind: catalog.Image}
	}
	return catalog.Model{Name: "MAI Image 2.6 Flash", Kind: catalog.Image}, o, nil
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
		out.Model = "MAI Image 2.6 Flash"
	}
	return out, nil
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

func (s *fakeStore) FinishChatRun(_ context.Context, jobID int64, stage string, _ db.ChatResult) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[jobID]
	if !ok || r.Stage != db.StageGenerating {
		return false, nil
	}
	r.Stage = stage
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
	return storage.Asset{ID: f.next, UserID: userID, Kind: "upload", ContentType: "image/jpeg", Width: 1024, Height: 768}, nil
}

func (f *fakeFiles) Results(_ context.Context, jobID int64) ([]storage.Asset, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.results[jobID], nil
}

func (f *fakeFiles) Open(context.Context, storage.Asset) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("result")), nil
}

type fakeAllowance struct{ left int }

func (a fakeAllowance) Usage(context.Context, int64) (free.Usage, error) {
	return free.Usage{Images: free.Meter{Limit: free.ImagesPerWindow, Used: free.ImagesPerWindow - a.left}}, nil
}

type fakeKeys struct{ conns []keys.Connection }

func (k fakeKeys) List(context.Context, int64) ([]keys.Connection, error) { return k.conns, nil }

// fakeRenderer writes small files where the real one would draw.
type fakeRenderer struct {
	dir    string
	albums [][]string
	cards  []string
}

func (r *fakeRenderer) file(name string) string {
	path := filepath.Join(r.dir, name)
	_ = os.WriteFile(path, []byte(name), 0o600)
	return path
}

func (r *fakeRenderer) Album(_ context.Context, dir string, paths []string) (anim.Media, error) {
	r.albums = append(r.albums, paths)
	path := filepath.Join(dir, "album.mp4")
	if err := os.WriteFile(path, []byte("album"), 0o600); err != nil {
		return anim.Media{}, err
	}
	return anim.Media{Path: path, ContentType: "video/mp4", Width: 1280, Height: 720, Duration: 4 * time.Second}, nil
}

func (r *fakeRenderer) Blank(context.Context) (anim.Media, error) {
	return anim.Media{Path: r.file("blank.png"), ContentType: "image/png", Width: 1280, Height: 720, Key: "blank-1"}, nil
}

func (r *fakeRenderer) Generating(context.Context) (anim.Media, error) {
	return anim.Media{Path: r.file("loop.mp4"), ContentType: "video/mp4", Width: 1280, Height: 720, Duration: 7200 * time.Millisecond, Key: "loop-1"}, nil
}

func (r *fakeRenderer) ErrorCard(_ context.Context, text, lang string) (anim.Media, error) {
	r.cards = append(r.cards, text)
	return anim.Media{Path: r.file("card.png"), ContentType: "image/png", Width: 1280, Height: 720, Key: "card", TextDrawn: lang != "ja"}, nil
}

// fakeAlbums is the bot's album cache.
type fakeAlbums struct {
	items []db.AlbumItem
	admin bool
}

func (a fakeAlbums) Album(context.Context, int64, string, time.Duration, time.Duration) ([]db.AlbumItem, error) {
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
	now      time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store := newStore()
	h := &harness{
		tg: newTelegram(), store: store, queue: &fakeQueue{store: store},
		files: &fakeFiles{results: map[int64][]storage.Asset{}}, renderer: &fakeRenderer{dir: t.TempDir()}, now: clock,
	}
	f, err := New(Config{
		Telegram: h.tg, Store: store, Queue: h.queue, Files: h.files, Allowance: fakeAllowance{left: 5},
		Renderer: h.renderer, Work: media.Work{Root: t.TempDir()},
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

func (h *harness) press(jobID int64, from int64, action string) error {
	run, err := h.store.ChatRun(context.Background(), jobID)
	if err != nil {
		return err
	}
	cq := &tg.CallbackQuery{
		ID: "cq", From: tg.User{ID: from, LanguageCode: "en"}, Data: callback(jobID, action),
		Message: tg.Message{MessageID: run.PreviewMessageID, Chat: tg.Chat{ID: run.ChatID, Type: "supergroup"}, Date: 1},
	}
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
