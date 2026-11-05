// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/storage"
)

func TestATextCommandShowsAPreviewOfTheFreeRun(t *testing.T) {
	h := newHarness(t)
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	if job == nil || !job.n.Free || job.n.Surface != jobs.Group || job.n.Offer.ID != free.ImageMAI.Offer ||
		job.n.Prompt != "a fox" || job.n.DedupeKey != "tg:-100:10" || len(job.n.Inputs) != 0 {
		t.Fatalf("job = %+v", job)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 {
		t.Fatalf("previews = %+v", h.tg.calls)
	}
	p := sent[0]
	// The card, as a reply to the command, and the prompt under it.
	if p.chatID != -100 || p.messageID != 10 || p.file != "card photo upload:framy.png" || p.text != `<img src="tg://photo?id=card"/><blockquote>a fox</blockquote>` {
		t.Fatalf("preview = %+v", p)
	}
	card := h.renderer.previews[0]
	if card.Model != "MAI Image 2.6 Flash" || card.Maker != "Microsoft AI" || !card.Free || card.Left != 5 || card.Limit != 5 ||
		card.Label != i18n.T("en", "card.free") || len(h.renderer.albums[0]) != 0 {
		t.Fatalf("card = %+v", card)
	}
	row := p.markup.InlineKeyboard[0]
	if len(row) != 2 || row[0].CallbackData != "j:1:go" || row[0].Style != "success" || row[1].CallbackData != "j:1:no" || row[1].Style != "danger" {
		t.Fatalf("buttons = %+v", row)
	}
	run, err := h.store.ChatRun(context.Background(), 1)
	if err != nil || run.Stage != db.StagePreview || run.PreviewMessageID == 0 || run.CommandMessageID != 10 || run.Lang != "en" {
		t.Fatalf("run = %+v, %v", run, err)
	}
}

// A card the faces cannot label, as in Japanese, leaves the price to the
// text under it.
func TestACardWithoutItsLabelPutsThePriceInTheCaption(t *testing.T) {
	h := newHarness(t)
	h.store.chatLang[-100] = "ja"
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	p := h.tg.methods("sendRichMessage")[0]
	if !strings.Contains(p.text, i18n.T("ja", "run.free", "left", "5", "limit", "5")) {
		t.Fatalf("caption = %q", p.text)
	}
}

func TestAReplyToAPhotoTakesThePhoto(t *testing.T) {
	h := newHarness(t)
	if err := h.command(photoReply("/img make it night"), CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("getFile"); len(got) != 1 || got[0].file != "big" {
		t.Fatalf("the largest size was not taken: %+v", got)
	}
	job := h.store.jobs[1]
	if len(job.n.Inputs) != 1 || job.n.Offer.ID != free.ImageMAI.Offer {
		t.Fatalf("job = %+v", job.n)
	}
	if len(h.renderer.albums) != 1 || len(h.renderer.albums[0]) != 1 {
		t.Fatalf("card drawn from %v", h.renderer.albums)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 || sent[0].messageID != 10 {
		t.Fatalf("preview = %+v", sent)
	}
}

func TestTheCaptionOfOnesOwnPhotoTakesIt(t *testing.T) {
	h := newHarness(t)
	m := command("")
	m.Caption = "/img make it night"
	m.Photo = []tg.Photo{{FileID: "own", Width: 800, Height: 600}}
	if err := h.command(m, CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("getFile"); len(got) != 1 || got[0].file != "own" {
		t.Fatalf("files = %+v", got)
	}
}

func TestAnAlbumIsTakenWholeWhenFramyIsAnAdmin(t *testing.T) {
	h := newHarness(t)
	var items []db.AlbumItem
	for i := range 7 {
		items = append(items, db.AlbumItem{MessageID: int64(20 + i), Kind: db.AlbumPhoto, FileID: "p" + string(rune('a'+i))})
	}
	items = append(items, db.AlbumItem{MessageID: 30, Kind: db.AlbumVideo, FileID: "v"})
	h.flow.SetAlbums(fakeAlbums{items: items, admin: true})
	m := photoReply("/img one style")
	m.ReplyToMessage.MediaGroupID = "g1"
	if err := h.command(m, CommandImage, "one style"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	// Seven photos are more than MAI takes: Klein takes the album; the video
	// is no photo to edit.
	if len(job.n.Inputs) != 7 || job.n.Offer.ID != free.ImageKlein.Offer {
		t.Fatalf("job = %d inputs on %s", len(job.n.Inputs), job.n.Offer.ID)
	}
	run, _ := h.store.ChatRun(context.Background(), 1)
	if run.Note != "" || run.Taken != 7 {
		t.Fatalf("run = %+v", run)
	}
}

// A reply to one picture of a bot's album is about that picture: framy is
// never sent the album, so it waits for nothing and says nothing of it.
// Telegram names a bot as the sender of an anonymous admin's album, or one
// sent on behalf of a channel, and those framy is sent: they go whole.
func TestABotsAlbumIsOnePictureAndAPersonsIsWhole(t *testing.T) {
	items := []db.AlbumItem{{MessageID: 20, Kind: db.AlbumPhoto, FileID: "pa"}, {MessageID: 21, Kind: db.AlbumPhoto, FileID: "pb"}, {MessageID: 22, Kind: db.AlbumPhoto, FileID: "pc"}}
	for _, c := range []struct {
		name   string
		sender tg.User
		taken  int
	}{
		{"a bot", tg.User{ID: 5, IsBot: true, Username: "some_bot"}, 1},
		{"an anonymous admin", tg.User{ID: 1087968824, IsBot: true, Username: "GroupAnonymousBot"}, 3},
		{"a channel", tg.User{ID: 136817688, IsBot: true, Username: "Channel_Bot"}, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.flow.SetAlbums(fakeAlbums{items: items})
			m := photoReply("/img one style")
			m.ReplyToMessage.MediaGroupID, m.ReplyToMessage.From = "g1", &c.sender
			if err := h.command(m, CommandImage, "one style"); err != nil {
				t.Fatal(err)
			}
			run, _ := h.store.ChatRun(context.Background(), 1)
			if run.Taken != c.taken || (c.taken == 1) != (run.Note == "") {
				t.Fatalf("run = %+v", run)
			}
		})
	}
}

// An album of pictures sent as files, uncompressed, is taken whole as well:
// the cache notes each as a photo.
func TestAnAlbumOfFilesIsTakenWhole(t *testing.T) {
	h := newHarness(t)
	var items []db.AlbumItem
	for i := range 4 {
		items = append(items, db.AlbumItem{MessageID: int64(20 + i), Kind: db.AlbumPhoto, FileID: "file-" + string(rune('a'+i))})
	}
	h.flow.SetAlbums(fakeAlbums{items: items, admin: true})
	m := command("/img one style")
	m.ReplyToMessage = &tg.Message{MessageID: 20, Chat: group, MediaGroupID: "g1",
		Document: &tg.Document{FileID: "file-a", MimeType: "image/png", FileSize: 9 << 20}}
	if err := h.command(m, CommandImage, "one style"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.store.jobs[1].n.Inputs); n != 4 {
		t.Fatalf("%d inputs", n)
	}
	if run, _ := h.store.ChatRun(context.Background(), 1); run.Note != "" || run.Taken != 4 {
		t.Fatalf("run = %+v", run)
	}
}

func TestWithoutAdminRightsThePreviewSaysWhatItSaw(t *testing.T) {
	h := newHarness(t)
	h.flow.SetAlbums(fakeAlbums{items: []db.AlbumItem{{MessageID: 9, Kind: db.AlbumPhoto, FileID: "big"}}})
	m := photoReply("/img one style")
	m.ReplyToMessage.MediaGroupID = "g1"
	if err := h.command(m, CommandImage, "one style"); err != nil {
		t.Fatal(err)
	}
	sent := h.tg.methods("sendRichMessage")
	if len(sent) != 1 || !strings.Contains(sent[0].text, i18n.T("en", noteAlbumOne)) {
		t.Fatalf("preview = %+v", sent)
	}
}

func TestMoreThanTenPhotosAreCut(t *testing.T) {
	h := newHarness(t)
	var items []db.AlbumItem
	for i := range 12 {
		items = append(items, db.AlbumItem{MessageID: int64(20 + i), Kind: db.AlbumPhoto, FileID: "p"})
	}
	h.flow.SetAlbums(fakeAlbums{items: items, admin: true})
	m := photoReply("/img x")
	m.ReplyToMessage.MediaGroupID = "g1"
	if err := h.command(m, CommandImage, "x"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.store.jobs[1].n.Inputs); n != free.ImageKlein.MaxInputs {
		t.Fatalf("%d inputs", n)
	}
	sent := h.tg.methods("sendRichMessage")
	if !strings.Contains(sent[0].text, i18n.T("en", noteCut, "max", "10", "left", "2")) {
		t.Fatalf("caption = %q", sent[0].text)
	}
}

func TestPastTheFreeTierWithoutAKeyTheReasonIsTheAnswer(t *testing.T) {
	h := newHarness(t)
	h.queue.refuse = &free.LimitError{Kind: catalog.Image, Limit: 5, Window: free.ImageWindow, FreesAt: h.now.Add(2*time.Hour + 14*time.Minute + 30*time.Second)}
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 0 || len(h.tg.methods("sendRichMessage")) != 0 {
		t.Fatal("a preview went out")
	}
	got := h.tg.methods("sendMessage")
	// The message says when, in the reader's own time zone, rounded up to the minute.
	at := h.now.Add(2*time.Hour + 15*time.Minute)
	tag := fmt.Sprintf(`<tg-time unix="%d" format="dt">%s</tg-time>`, at.Unix(), at.UTC().Format("2006-01-02 15:04 UTC"))
	if len(got) != 1 || got[0].text != i18n.T("en", "run.limit_at", "limit", "5", "at", tag) || got[0].messageID != 10 {
		t.Fatalf("answer = %+v", got)
	}
}

func TestPastTheFreeTierTheKeyRunsIt(t *testing.T) {
	h := newHarness(t)
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	h.queue.refuse = &free.ClosedError{}
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	if job == nil || job.n.Free {
		t.Fatalf("job = %+v", job)
	}
	sent := h.tg.methods("sendRichMessage")
	card := h.renderer.previews[0]
	if len(sent) != 1 || card.Free || card.Label != i18n.T("en", "card.key", "provider", "OpenRouter") || card.Price == "" {
		t.Fatalf("preview = %+v, card = %+v", sent, card)
	}
}

// Past the free tier a key of another provider than the free model's runs
// the command on a model it runs: the one the picker suggests first for the
// run, else the first the list shows. The preview shows it, on that key and
// at its price, before anything runs.
func TestPastTheFreeTierAnotherProvidersKeyRunsItsModel(t *testing.T) {
	limit := &free.LimitError{Kind: catalog.Image, Limit: 5, Window: free.ImageWindow, FreesAt: clock.Add(2 * time.Hour)}
	for _, c := range []struct {
		name      string
		keys      []string
		suggested string
		offer     string
		provider  string
	}{
		{"the only model the key runs", []string{catalog.OpenAI}, "", "openai:openai/gpt-image-2.5", "OpenAI"},
		{"the first the list shows", []string{catalog.OpenAI, catalog.Gemini}, "", "gemini:google/nano-banana-pro", "Google Gemini"},
		{"the suggested one first", []string{catalog.OpenAI, catalog.Gemini}, "openai/gpt-image-2.5", "openai:openai/gpt-image-2.5", "OpenAI"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.models = panelCatalog()
			for i, m := range h.store.models {
				if m.ID == c.suggested {
					h.store.models[i].Traits.Recommend = map[string]int{catalog.ForImage: 1}
				}
			}
			var conns []keys.Connection
			for _, p := range c.keys {
				conns = append(conns, keys.Connection{Provider: p, Status: keys.Valid})
			}
			h.flow.cfg.Keys = fakeKeys{conns: conns}
			h.queue.refuse = limit
			if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
				t.Fatal(err)
			}
			if got := h.tg.methods("sendMessage"); len(got) != 0 {
				t.Fatalf("refused: %+v", got)
			}
			job := h.store.jobs[1]
			if job == nil || job.n.Free || job.n.Offer.ID != c.offer || job.n.Estimate == nil {
				t.Fatalf("job = %+v", job)
			}
			card := h.renderer.previews[0]
			if card.Free || card.Label != i18n.T("en", "card.key", "provider", c.provider) {
				t.Fatalf("card = %+v", card)
			}
		})
	}

	// With no model a key of theirs runs, the reason stays the answer.
	h := newHarness(t)
	h.store.models = panelCatalog()[2:] // without Nano Banana Pro and GPT Image 2.5
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenAI, Status: keys.Valid}}}
	h.queue.refuse = limit
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if len(h.store.jobs) != 0 || len(h.tg.methods("sendMessage")) != 1 {
		t.Fatalf("jobs %+v, calls %+v", h.store.jobs, h.tg.calls)
	}
}

// Past the free tier no model is listed as free: a free model goes on the
// person's key, at its price, or needs one, as a pick of it would run.
func TestPastTheFreeTierNoModelIsListedAsFree(t *testing.T) {
	limit := &free.LimitError{Kind: catalog.Image, Limit: 5, Window: free.ImageWindow, FreesAt: clock.Add(2 * time.Hour)}
	freeLabel := " · " + i18n.T("en", "card.free")
	for _, c := range []struct {
		name, provider, flux string
	}{
		{"an OpenRouter key", catalog.OpenRouter, `<tg-button type="callback_data" data="j:1:` + actPickModel + modelHash("black-forest-labs/flux-3-image-klein") + `">FLUX 3 Image Klein</tg-button>`},
		{"an OpenAI key", catalog.OpenAI, `<tg-button type="disabled">FLUX 3 Image Klein · ` + i18n.T("en", "run.need_key", "provider", "OpenRouter") + `</tg-button>`},
	} {
		h := newHarness(t)
		h.store.models = panelCatalog()
		h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: c.provider, Status: keys.Valid}}}
		h.flow.cfg.Allowance = fakeAllowance{left: 0}
		h.queue.refuse = limit
		if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
			t.Fatal(err)
		}
		body := h.tg.methods("sendRichMessage")[0].text
		if strings.Contains(body, freeLabel) || !strings.Contains(body, c.flux) {
			t.Fatalf("%s: preview = %s", c.name, body)
		}
	}

	// A pick of the free model goes on the key, as its button said.
	h := newHarness(t)
	h.store.models = panelCatalog()
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	h.flow.cfg.Allowance = fakeAllowance{left: 0}
	h.queue.refuse = limit
	if err := h.command(photoReply("/img make it night"), CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, pick("black-forest-labs/flux-3-image-klein")); err != nil {
		t.Fatal(err)
	}
	if n := h.queue.revised[len(h.queue.revised)-1]; n.Free || n.Offer.ID != "openrouter:black-forest-labs/flux-3-image-klein" {
		t.Fatalf("revision = %+v", n)
	}
	// The keyboard's list says the same.
	h.tg.reset()
	if err := h.pressPhoto(1, 7, actModel); err != nil {
		t.Fatal(err)
	}
	for _, row := range labels(h.tg.cards()[0].markup) {
		if strings.Contains(strings.Join(row, "|"), freeLabel) {
			t.Fatalf("list = %v", labels(h.tg.cards()[0].markup))
		}
	}
}

func TestARefusedKeyDoesNotRunIt(t *testing.T) {
	h := newHarness(t)
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Invalid}}}
	h.queue.refuse = &free.ClosedError{}
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.closed") {
		t.Fatalf("answer = %+v", got)
	}
}

func TestEmptyCommandsVideosAndRepeats(t *testing.T) {
	h := newHarness(t)
	if err := h.command(command("/img"), CommandImage, ""); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.empty") {
		t.Fatalf("empty: %+v", got)
	}
	h.tg.reset()
	m := command("/img rain")
	m.ReplyToMessage = &tg.Message{MessageID: 9, Video: &tg.Video{FileID: "v"}}
	if err := h.command(m, CommandImage, "rain"); err != nil {
		t.Fatal(err)
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.video_edit") {
		t.Fatalf("video: %+v", got)
	}
	h.tg.reset()
	// The same update again starts nothing.
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.tg.methods("sendRichMessage")); n != 1 || len(h.store.jobs) != 1 {
		t.Fatalf("%d previews, %d jobs", n, len(h.store.jobs))
	}
}

func TestAPhotoFramyCannotReadIsSaidSo(t *testing.T) {
	h := newHarness(t)
	h.files.fail = storage.ErrTooBig
	err := h.command(photoReply("/img x"), CommandImage, "x")
	if err == nil {
		t.Fatal("no error")
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.too_big") {
		t.Fatalf("answer = %+v", got)
	}
	if len(h.store.jobs) != 0 {
		t.Fatal("a job was made")
	}
}

func TestAFailedPreviewCancelsTheDraft(t *testing.T) {
	h := newHarness(t)
	h.tg.fail["sendRichMessage"] = errBoom
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err == nil {
		t.Fatal("no error")
	}
	if len(h.queue.cancelled) != 1 || h.store.jobs[1].status != jobs.Cancelled {
		t.Fatalf("cancelled = %v", h.queue.cancelled)
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.failed") {
		t.Fatalf("answer = %+v", got)
	}
}

func TestTheGroupsLanguageWritesThePreview(t *testing.T) {
	h := newHarness(t)
	h.store.chatLang[-100] = "uk"
	if err := h.command(command("/img лис"), CommandImage, "лис"); err != nil {
		t.Fatal(err)
	}
	p := h.tg.methods("sendRichMessage")[0]
	if p.markup.InlineKeyboard[0][0].Text != i18n.T("uk", "run.generate") {
		t.Fatalf("buttons = %+v", p.markup.InlineKeyboard)
	}
}

// preview makes a run waiting for Generate and returns its job id.
func (h *harness) preview(t *testing.T) int64 {
	t.Helper()
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	return 1
}

func TestOnlyTheRequesterPresses(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	if err := h.press(id, 8, actGenerate); err != nil {
		t.Fatal(err)
	}
	if len(h.tg.answers) != 1 || h.tg.answers[0] != "alert:"+i18n.T("en", "run.not_yours") {
		t.Fatalf("answers = %v", h.tg.answers)
	}
	if h.store.jobs[id].status != jobs.Draft {
		t.Fatal("someone else's press queued the job")
	}
	// A press against another message does nothing.
	cq := &tg.CallbackQuery{ID: "x", From: tg.User{ID: 7}, Data: callback(id, actGenerate), Message: &tg.Message{MessageID: 1, Chat: group}}
	if err := h.flow.press(context.Background(), cq); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[id].status != jobs.Draft {
		t.Fatal("a press on another message queued the job")
	}
}

func TestGenerateTurnsThePreviewIntoTheLoop(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[id].status != jobs.Queued || h.store.stage(id) != db.StageGenerating {
		t.Fatalf("status %s, stage %s", h.store.jobs[id].status, h.store.stage(id))
	}
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || edits[0].file != "animation upload:framy.mp4" || edits[0].markup.InlineKeyboard[0][0].CallbackData != "j:1:stop" {
		t.Fatalf("edits = %+v", edits)
	}
	if !strings.Contains(edits[0].text, i18n.T("en", "run.generating", "model", "<b>MAI Image 2.6 Flash</b>")) {
		t.Fatalf("caption = %q", edits[0].text)
	}
	// A second press starts nothing.
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if n := len(h.tg.methods("editMessageMedia")); n != 1 {
		t.Fatalf("%d edits", n)
	}
	// The next run's loop goes by its file_id.
	h.store.runs[2] = &db.ChatRun{JobID: 2, ChatID: -100, PreviewMessageID: 500, Lang: "en", Stage: db.StagePreview}
	h.store.jobs[2] = &fakeJob{n: jobs.New{UserID: 7}, status: jobs.Draft}
	if err := h.press(2, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if e := h.tg.methods("editMessageMedia"); e[len(e)-1].file != "animation loop-file" {
		t.Fatalf("second loop = %+v", e[len(e)-1])
	}
}

func TestGenerateOnAnExpiredDraftDeletesThePreview(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	h.queue.generate = jobs.ErrNotDraft
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if h.tg.answers[0] != "alert:"+i18n.T("en", "run.expired") || h.store.stage(id) != db.StageGone || len(h.tg.methods("deleteMessage")) != 1 {
		t.Fatalf("answers %v, stage %s, calls %+v", h.tg.answers, h.store.stage(id), h.tg.calls)
	}
}

func TestGenerateWhenTheFreeTierRanOut(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	h.queue.generate = &free.LimitError{Limit: 5, FreesAt: h.now.Add(40 * time.Second)}
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if h.tg.answers[0] != "alert:"+i18n.T("en", "run.limit", "limit", "5", "wait", "1 min") || h.store.stage(id) != db.StagePreview {
		t.Fatalf("answers %v, stage %s", h.tg.answers, h.store.stage(id))
	}
}

func TestNoCancelsAndDeletes(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	if err := h.press(id, 7, actNo); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[id].status != jobs.Cancelled || h.store.stage(id) != db.StageGone {
		t.Fatalf("status %s, stage %s", h.store.jobs[id].status, h.store.stage(id))
	}
	if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != h.store.runs[id].PreviewMessageID {
		t.Fatalf("deletes = %+v", d)
	}
}

// A run the model had started goes on when Stop takes it out of the chat,
// so the alert says what it costs rather than leaving it to read as given
// back.
func TestStopARunningJob(t *testing.T) {
	for _, c := range []struct {
		name  string
		free  bool
		offer catalog.Offer
		want  string
	}{
		{"free image", true, catalog.Offer{ID: "openrouter:mai/image", Provider: catalog.OpenRouter, Kind: catalog.Image}, i18n.T("en", "run.stopped_image")},
		{"free video", true, catalog.Offer{ID: "openrouter:wan/video", Provider: catalog.OpenRouter, Kind: catalog.Video}, i18n.T("en", "run.stopped_video")},
		{"own key", false, catalog.Offer{ID: "gemini:gemini-3-pro-image", Provider: catalog.Gemini, Kind: catalog.Image},
			"The model had already started, so the run goes on and your Google Gemini key pays for it. The result goes to your library on the site, not to the chat."},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			id := h.preview(t)
			if err := h.press(id, 7, actGenerate); err != nil {
				t.Fatal(err)
			}
			h.store.jobs[id].n.Free, h.store.jobs[id].n.Offer = c.free, c.offer
			h.store.setStatus(id, jobs.Running, "")
			h.queue.cancelErr = jobs.ErrRunning
			h.tg.reset()
			if err := h.press(id, 7, actStop); err != nil {
				t.Fatal(err)
			}
			if !h.store.runs[id].Stopped || h.store.stage(id) != db.StageGone || len(h.tg.methods("deleteMessage")) != 1 {
				t.Fatalf("run %+v, calls %+v", h.store.runs[id], h.tg.calls)
			}
			if len(h.tg.answers) != 1 || h.tg.answers[0] != "alert:"+c.want {
				t.Fatalf("answers = %v", h.tg.answers)
			}
		})
	}
}

func TestStopAQueuedJob(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if err := h.press(id, 7, actStop); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[id].status != jobs.Cancelled || h.store.stage(id) != db.StageGone || h.store.runs[id].Stopped {
		t.Fatalf("status %s, run %+v", h.store.jobs[id].status, h.store.runs[id])
	}
	// Nothing had started, so nothing is spent and there is nothing to say.
	if a := h.tg.answers; a[len(a)-1] != "" {
		t.Fatalf("answers = %v", a)
	}
}

// Stop a moment after the job ended: the result, paid for already, still
// comes in the loop's place, and so does the card of a run that failed.
func TestStopAJobThatJustEnded(t *testing.T) {
	for _, c := range []struct {
		status jobs.Status
		stage  string
	}{
		{jobs.Done, db.StageDone},
		{jobs.Failed, db.StageFailed},
		{jobs.Expired, db.StageFailed},
	} {
		t.Run(string(c.status), func(t *testing.T) {
			h := newHarness(t)
			id := h.generating(t)
			h.store.setStatus(id, c.status, "")
			h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 100, Width: 10, Height: 10}}
			if err := h.press(id, 7, actStop); err != nil {
				t.Fatal(err)
			}
			if h.store.stage(id) != db.StageGenerating || len(h.tg.methods("deleteMessage")) != 0 {
				t.Fatalf("Stop took the loop away: stage %s, calls %+v", h.store.stage(id), h.tg.calls)
			}
			if len(h.tg.answers) != 1 || h.tg.answers[0] != "" {
				t.Fatalf("answers = %v", h.tg.answers)
			}
			h.flow.step(context.Background(), id)
			if h.store.stage(id) != c.stage || len(h.tg.methods("editMessageMedia")) != 1 {
				t.Fatalf("stage %s, calls %+v", h.store.stage(id), h.tg.calls)
			}
		})
	}
}

// generating makes a run whose job is queued, Generate pressed at h.now.
func (h *harness) generating(t *testing.T) int64 {
	t.Helper()
	id := h.preview(t)
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	return id
}

func TestAQuickResultReplacesTheLoop(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.now = h.now.Add(12 * time.Second)
	h.flow.step(context.Background(), id)
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || edits[0].file != "photo upload:framy.png" {
		t.Fatalf("edits = %+v", edits)
	}
	// Under the image: Animate and Again, and Original under them.
	if got := buttons(edits[0].markup); !reflect.DeepEqual(got, [][]string{
		{callback(id, actAnimate), callback(id, actAgain)}, {callback(id, actOriginal)},
	}) || edits[0].markup.InlineKeyboard[0][0].Text != i18n.T("en", "result.animate") {
		t.Fatalf("the result's buttons: %v", got)
	}
	if !strings.Contains(edits[0].text, "a fox") || !strings.Contains(edits[0].text, "MAI Image 2.6 Flash") {
		t.Fatalf("caption = %q", edits[0].text)
	}
	if h.store.stage(id) != db.StageDone || len(h.tg.methods("deleteMessage")) != 0 {
		t.Fatalf("stage %s, calls %+v", h.store.stage(id), h.tg.calls)
	}
	// What inline mode sends again.
	if res, want := h.store.results[id], (db.ChatResult{MessageID: edits[0].messageID, FileID: "result-file", FileUniqueID: "result-unique", FileType: db.FilePhoto}); res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
}

func TestASlowResultIsAReply(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.now = h.now.Add(40 * time.Second)
	h.flow.step(context.Background(), id)
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 || sent[0].messageID != 10 {
		t.Fatalf("sent = %+v", sent)
	}
	if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != h.store.runs[id].PreviewMessageID {
		t.Fatalf("the loop stayed: %+v", d)
	}
	if h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s", h.store.stage(id))
	}
}

// In a DM a run on the key may make several results: the card offers the
// count, and they come as one album with the caption on the first, the
// loop gone, the run on the first picture.
func TestSeveralResultsOfADirectChatComeAsOneAlbum(t *testing.T) {
	h := newHarness(t)
	h.store.models = panelCatalog()
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	if err := h.command(photoSaid("make it night"), CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, pick("google/nano-banana-pro")); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.press(1, 7, actPickSetting+"n.2"); err != nil {
		t.Fatal(err)
	}
	edits := h.tg.methods("editMessageRich")
	if len(edits) != 1 || !strings.Contains(edits[0].text, "Settings: <b>Auto · 1K · ×3</b>") ||
		!strings.Contains(edits[0].text, `data="j:1:Sn#`+valueHash(4)+`">×4<`) {
		t.Fatalf("preview = %+v", edits)
	}
	if n := h.queue.revised[len(h.queue.revised)-1]; n.Settings["n"] != 3 {
		t.Fatalf("revision = %+v", n)
	}
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	loop := run(t, h).PreviewMessageID
	h.tg.reset()
	h.store.setStatus(1, jobs.Done, "")
	pic := storage.Asset{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}
	second, third := pic, pic
	second.ID, third.ID = 51, 52
	h.files.results[1] = []storage.Asset{pic, second, third}
	h.now = h.now.Add(12 * time.Second)
	before := h.tg.next
	h.flow.step(context.Background(), 1)
	first := before + 1 + int64(slices.IndexFunc(h.tg.calls, func(c call) bool { return c.method == "sendMediaGroup" }))
	sent := h.tg.methods("sendMediaGroup")
	if len(sent) != 1 || sent[0].chatID != 7 || sent[0].file != strings.Repeat("photo:upload:framy.png,", 2)+"photo:upload:framy.png" ||
		!strings.Contains(sent[0].text, "make it night") || !strings.Contains(sent[0].text, "Nano Banana Pro") {
		t.Fatalf("sent = %+v", sent)
	}
	if len(h.tg.methods("editMessageMedia")) != 0 || len(h.tg.methods("sendPhoto")) != 0 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != loop {
		t.Fatalf("the loop stayed: %+v", d)
	}
	if h.store.stage(1) != db.StageDone || h.store.results[1].MessageID != first || h.store.results[1].FileID != "album-file" {
		t.Fatalf("stage %s, result %+v", h.store.stage(1), h.store.results[1])
	}
}

// In a group one run makes one result: the card has no count, a count
// pressed anyway changes nothing, and a run that made several anyway, from
// before, sends its first.
func TestARunOfAGroupMakesOneResult(t *testing.T) {
	h := panelHarness(t)
	if err := h.press(1, 7, pick("google/nano-banana-pro")); err != nil {
		t.Fatal(err)
	}
	if body := h.tg.methods("editMessageRich")[0].text; strings.Contains(body, "Sn#") || strings.Contains(body, "×") {
		t.Fatalf("preview = %s", body)
	}
	h.tg.reset()
	revised := len(h.queue.revised)
	if err := h.press(1, 7, actPickSetting+"n.2"); err != nil {
		t.Fatal(err)
	}
	if len(h.queue.revised) != revised || len(h.tg.methods("editMessageRich")) != 0 {
		t.Fatalf("revised %+v, calls %+v", h.queue.revised, h.tg.calls)
	}
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	h.store.setStatus(1, jobs.Done, "")
	pic := storage.Asset{ID: 50, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}
	second := pic
	second.ID = 51
	h.files.results[1] = []storage.Asset{pic, second}
	h.now = h.now.Add(40 * time.Second)
	h.flow.step(context.Background(), 1)
	if len(h.tg.methods("sendMediaGroup")) != 0 || len(h.tg.methods("sendPhoto")) != 1 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
}

func TestAHugeResultIsADocument(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 30 << 20, Width: 8000, Height: 8000}}
	h.flow.step(context.Background(), id)
	if d := h.tg.methods("sendDocument"); len(d) != 1 || d[0].file != "upload:framy.png" {
		t.Fatalf("documents = %+v", d)
	}
	// A document is not offered inline, which takes photos and videos.
	if res := h.store.results[id]; res.FileType != db.FileDocument || res.FileID != "doc-file" {
		t.Fatalf("result = %+v", res)
	}
}

// An SVG goes as a file Telegram keeps the name of, with Again and no
// Animate: no video model takes one as a frame.
func TestAnSVGIsADocumentWithItsName(t *testing.T) {
	h := newHarness(t)
	withHeyGen(h)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/svg+xml", Bytes: 40 << 10, Width: 1024, Height: 1024}}
	h.now = h.now.Add(40 * time.Second)
	h.flow.step(context.Background(), id)
	if p := h.tg.methods("sendPhoto"); len(p) != 0 {
		t.Fatalf("photos = %+v", p)
	}
	d := h.tg.methods("sendDocument")
	if len(d) != 1 || d[0].file != "upload:framy.svg" || !reflect.DeepEqual(buttons(d[0].markup), [][]string{{callback(id, actAgain)}}) {
		t.Fatalf("documents = %+v", d)
	}
	if err := h.onResult(id, 7, actAnimate, "cq-1"); err != nil || len(h.store.jobs) != 1 {
		t.Fatalf("Animate on an SVG: %v, %d jobs", err, len(h.store.jobs))
	}
}

func TestAFailedRunShowsTheCard(t *testing.T) {
	retry, other := []string{actRetry}, []string{actOther}
	const topUp = "url:https://openrouter.ai/settings/credits"
	// Outside the DM the way to the settings is framy's DM (settings.go).
	const rekey = "url:https://t.me/framyin_bot?start=settings_openrouter"
	for _, tc := range []struct {
		status jobs.Status
		class  jobs.Class
		free   bool
		want   string
		// acts are the buttons under the card, in one row unless apart.
		acts  []string
		apart bool
	}{
		{jobs.Failed, jobs.Moderation, true, "err.moderation", other, false},
		// A real person in an input: another model may take it.
		{jobs.Failed, jobs.Faces, false, "err.faces", other, false},
		// The model's own sound: the same run without it may pass. This
		// run had no sound to turn off.
		{jobs.Failed, jobs.Sound, false, "err.sound", retry, false},
		// The person's key out of money: a link to top it up, then another
		// try, each on its own row since "Top up OpenRouter" is too long to
		// share one.
		{jobs.Failed, jobs.Money, false, "err.money", []string{topUp, actRetry}, true},
		{jobs.Failed, jobs.Money, true, "err.failed", retry, false},
		// A refused key is replaced in the settings.
		{jobs.Failed, jobs.Key, false, "err.key", []string{rekey}, false},
		// A free run is paid by framy's key, not the person's.
		{jobs.Failed, jobs.Key, true, "err.failed", retry, false},
		{jobs.Expired, "", true, "err.timeout", []string{actRetry, actOther}, false},
		// A video whose key stopped working while framy asked about it.
		{jobs.Expired, jobs.Key, false, "err.key", []string{rekey}, false},
		{jobs.Expired, jobs.Money, false, "err.money", []string{topUp, actRetry}, true},
		{jobs.Expired, jobs.Key, true, "err.failed", retry, false},
		{jobs.Expired, jobs.ProviderFailed, false, "err.timeout", []string{actRetry, actOther}, false},
		{jobs.Failed, jobs.Internal, true, "err.failed", []string{actRetry, actOther}, false},
	} {
		h := newHarness(t)
		id := h.generating(t)
		h.store.jobs[id].n.Free = tc.free
		h.store.setStatus(id, tc.status, tc.class)
		h.flow.step(context.Background(), id)
		if len(h.renderer.cards) != 1 || h.renderer.cards[0] != i18n.T("en", tc.want, "provider", "OpenRouter") {
			t.Errorf("%s/%s: cards %v, want %s", tc.status, tc.class, h.renderer.cards, tc.want)
			continue
		}
		edits := h.tg.methods("editMessageMedia")
		if len(edits) != 1 || edits[0].file != "photo upload:framy.png" || h.store.stage(id) != db.StageFailed {
			t.Errorf("%s/%s: edits %+v, stage %s", tc.status, tc.class, edits, h.store.stage(id))
			continue
		}
		var want [][]string
		if tc.acts != nil {
			want = [][]string{{}}
			for i, a := range tc.acts {
				data := a
				if !strings.HasPrefix(a, "url:") {
					data = callback(id, a)
				}
				if tc.apart && i > 0 {
					want = append(want, nil)
				}
				want[len(want)-1] = append(want[len(want)-1], data)
			}
		}
		if got := buttons(edits[0].markup); !reflect.DeepEqual(got, want) {
			t.Errorf("%s/%s: buttons %v, want %v", tc.status, tc.class, got, want)
			continue
		}
		if tc.class == jobs.Money && !tc.free {
			top := edits[0].markup.InlineKeyboard[0][0]
			if top.Text != i18n.T("en", "web.act.topup", "provider", "OpenRouter") || top.URL != "https://openrouter.ai/settings/credits" {
				t.Errorf("top up = %+v", top)
			}
		}
	}
}

// buttons is the data of a keyboard's buttons, row by row; nil for none.
func buttons(kb *tg.InlineKeyboardMarkup) [][]string {
	if kb == nil {
		return nil
	}
	var out [][]string
	for _, row := range kb.InlineKeyboard {
		var r []string
		for _, b := range row {
			switch {
			case b.URL != "":
				r = append(r, "url:"+b.URL)
			case b.WebApp != nil:
				r = append(r, "app:"+b.WebApp.URL)
			default:
				r = append(r, b.CallbackData)
			}
		}
		out = append(out, r)
	}
	return out
}

func TestACardWithoutTheLettersPutsTheLineInTheCaption(t *testing.T) {
	h := newHarness(t)
	h.store.chatLang[-100] = "ja"
	id := h.generating(t)
	h.store.setStatus(id, jobs.Failed, jobs.Moderation)
	h.flow.step(context.Background(), id)
	edits := h.tg.methods("editMessageMedia")
	if len(edits) != 1 || !strings.Contains(edits[0].text, i18n.T("ja", "err.moderation")) {
		t.Fatalf("edits = %+v", edits)
	}
}

func TestAnUnpressedPreviewGoesWhenItsDraftIsCancelled(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	h.flow.step(context.Background(), id)
	if len(h.tg.calls) != 0 {
		t.Fatalf("a waiting preview was touched: %+v", h.tg.calls)
	}
	h.store.setStatus(id, jobs.Cancelled, jobs.DraftExpired)
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageGone || len(h.tg.methods("deleteMessage")) != 1 {
		t.Fatalf("stage %s, calls %+v", h.store.stage(id), h.tg.calls)
	}
}

func TestAQueuedJobWhoseRunStayedInPreviewMovesOn(t *testing.T) {
	h := newHarness(t)
	id := h.preview(t)
	h.store.setStatus(id, jobs.Queued, "")
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageGenerating || len(h.tg.methods("editMessageMedia")) != 1 {
		t.Fatalf("stage %s, calls %+v", h.store.stage(id), h.tg.calls)
	}
}

func TestProgressKeepsItsCadence(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Running, "")
	ctx := context.Background()
	h.flow.step(ctx, id) // right after the press: nothing yet
	if len(h.tg.calls) != 0 {
		t.Fatalf("calls = %+v", h.tg.calls)
	}
	h.now = h.now.Add(5 * time.Second)
	h.flow.step(ctx, id)
	if len(h.tg.methods("sendChatAction")) != 1 || len(h.tg.methods("editMessageCaption")) != 0 {
		t.Fatalf("at 5 s: %+v", h.tg.calls)
	}
	h.now = h.now.Add(6 * time.Second)
	h.flow.step(ctx, id)
	caps := h.tg.methods("editMessageCaption")
	if len(caps) != 1 || !strings.Contains(caps[0].text, "0:11") {
		t.Fatalf("at 11 s: %+v", caps)
	}
}

func TestFollowWorksEveryOpenRun(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 50, ContentType: "image/png", Bytes: 100, Width: 10, Height: 10}}
	h.flow.follow(context.Background())
	if h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s", h.store.stage(id))
	}
}

// A run delivered while a press waits on it: forget comes from the one
// that holds the run, and the press must still wait for it to let go.
func TestARunsLockHoldsUntilItIsLetGo(t *testing.T) {
	h := newHarness(t)
	unlock := h.flow.lock(1)
	h.flow.forget(1)
	if _, ok := h.flow.tryLock(1); ok {
		t.Fatal("the run was held twice at once")
	}
	got := make(chan struct{})
	go func() {
		defer close(got)
		h.flow.lock(1)()
	}()
	select {
	case <-got:
		t.Fatal("a press went past the run's holder")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	<-got
	h.flow.locksMu.Lock()
	defer h.flow.locksMu.Unlock()
	if len(h.flow.locks) != 0 {
		t.Fatalf("%d locks stay after every press let go", len(h.flow.locks))
	}
}

func TestPressesOnOneRunGoOneAtATime(t *testing.T) {
	h := newHarness(t)
	var (
		in, most int
		mu       sync.Mutex
		wg       sync.WaitGroup
	)
	for i := range 50 {
		wg.Go(func() {
			unlock := h.flow.lock(7)
			mu.Lock()
			in++
			most = max(most, in)
			mu.Unlock()
			if i%5 == 0 {
				h.flow.forget(7)
			}
			time.Sleep(100 * time.Microsecond)
			mu.Lock()
			in--
			mu.Unlock()
			unlock()
		})
	}
	wg.Wait()
	if most != 1 {
		t.Fatalf("%d presses worked the run at once", most)
	}
	if len(h.flow.locks) != 0 {
		t.Fatalf("%d locks stay", len(h.flow.locks))
	}
}

func TestCallbackData(t *testing.T) {
	for data, ok := range map[string]bool{"j:1:go": true, "j:123456789012:stop": true, "j:0:go": false, "j:x:go": false, "j:1:": false, "m:1:home": false} {
		_, _, got := parseCallback(data)
		if got != ok {
			t.Errorf("%q: %v", data, got)
		}
	}
	if d := callback(123456789012, actStop); len(d) > 64 {
		t.Fatalf("%q is over Telegram's 64 bytes", d)
	}
}

func TestPriceLabels(t *testing.T) {
	q := fx.Quote{Code: fx.UAH, PerUSD: 41.5}
	for _, tc := range []struct {
		e    price.Estimate
		lang string
		want string
	}{
		{price.Estimate{Kind: price.Exact, USD: 0.034, MaxUSD: 0.034}, "en", "$0.034 · ≈ ₴1.42"},
		{price.Estimate{Kind: price.About, USD: 0.034, MaxUSD: 0.04}, "uk", i18n.T("uk", "run.about", "price", "0,034\u00a0$ · 1,42\u00a0₴")},
		{price.Estimate{Kind: price.UpTo, USD: 0.02, MaxUSD: 0.05}, "en", i18n.T("en", "run.up_to", "price", "$0.05 · ₴2.08")},
		{price.Estimate{Kind: price.Unknown}, "en", i18n.T("en", "run.price_unknown")},
	} {
		if got := priceLabel(tc.lang, tc.e, q); got != tc.want {
			t.Errorf("%s %v: %q, want %q", tc.lang, tc.e.Kind, got, tc.want)
		}
	}
	if got := priceLabel("en", price.Estimate{Kind: price.Exact, USD: 0.034}, fx.Dollar); got != "$0.034" {
		t.Errorf("in dollars: %q", got)
	}
}

func TestPromptQuote(t *testing.T) {
	if got := promptQuote(strings.Repeat("a", 700)); !strings.HasPrefix(got, "<blockquote expandable>") || !strings.Contains(got, "…") {
		t.Fatalf("long prompt = %q", got[:40])
	}
	if got := promptQuote("<b>x</b>"); got != "<blockquote>&lt;b&gt;x&lt;/b&gt;</blockquote>" {
		t.Fatalf("prompt not escaped: %q", got)
	}
}

// The wait is rounded up: a free run is never promised before it is there.
func TestTheWaitIsInHoursAndMinutes(t *testing.T) {
	for _, c := range []struct {
		lang string
		d    time.Duration
		want string
	}{
		{"en", 0, "1 min"},
		{"en", 59*time.Minute + time.Second, "1 h 0 min"},
		{"en", 23*time.Hour + 59*time.Minute, "23 h 59 min"},
		// Past a day, days and hours, rounded up to the hour.
		{"en", 24*time.Hour + time.Minute, "1 d 1 h"},
		{"en", 6*24*time.Hour + 23*time.Hour + 30*time.Minute, "7 d 0 h"},
		{"ru", 50 * time.Hour, "2 д 2 ч"},
		{"ru", 3*time.Hour + 5*time.Minute, "3 ч 5 мин"},
		{"uk", 12 * time.Minute, "12 хв"},
	} {
		if got := waitText(c.lang, c.d); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.lang, c.d, got, c.want)
		}
	}
}

// One person's DM with each bot has the same chat id and message ids counted
// apart, so a stack other than production keys its jobs with its name, and
// production keeps its keys (docs/design.md, Stacks).
func TestTheTestBotKeysItsJobsApart(t *testing.T) {
	h := newHarness(t)
	h.flow.cfg.Stack = "test"
	if err := h.command(command("/img a fox"), CommandImage, "a fox"); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job == nil || job.n.DedupeKey != "tg:test:-100:10" {
		t.Fatalf("job = %+v", job)
	}
	for _, c := range []struct{ stack, key, want string }{
		{"", "tg:7:10", "tg:7:10"},
		{"", "tg:cb:42", "tg:cb:42"},
		{"test", "tg:cb:42", "tg:test:cb:42"},
		{"test", "tg:g:q1", "tg:test:g:q1"},
	} {
		if got := stackKey(c.stack, c.key); got != c.want {
			t.Errorf("stackKey(%q, %q) = %q, want %q", c.stack, c.key, got, c.want)
		}
	}
}
