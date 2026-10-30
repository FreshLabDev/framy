// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/storage"
)

// withFlux puts FLUX Video Edit in the catalog as the overlay has it: 0.7
// to 15 s, 50 MiB, the result as long as the clip.
func withFlux(h *harness) {
	of := catalog.Offer{
		ID: free.VideoEdit.Offer, ModelID: free.VideoEdit.Model, Provider: catalog.OpenRouter, ProviderModelID: free.VideoEdit.Model,
		Kind: catalog.Video, Name: "FLUX Video Edit", InputModalities: []string{"text", "video"},
		Routes: []catalog.Route{{SKUs: map[string]string{"duration_seconds": "0.04"}}},
	}
	h.store.models = append(h.store.models, catalog.Model{
		ID: of.ModelID, Kind: catalog.Video, Name: "FLUX Video Edit", Maker: "Black Forest Labs", Free: true, Offers: []catalog.Offer{of},
		Traits: catalog.Traits{SourceVideo: &catalog.SourceVideo{Required: true, MinSeconds: 0.7, MaxSeconds: 15, MaxMiB: 50, KeepsLength: true}},
	})
}

func videoHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	withFlux(h)
	return h
}

// videoReply is /edit in reply to a video.
func videoReply(text string) *tg.Message {
	m := command(text)
	m.ReplyToMessage = &tg.Message{MessageID: 9, Chat: group, Video: &tg.Video{FileID: "clip", Width: 1280, Height: 720, Duration: 4, FileSize: 3 << 20}}
	return m
}

func TestEditOnAVideoShowsAPreviewOfTheFreeEdit(t *testing.T) {
	h := videoHarness(t)
	if err := h.command(videoReply("/edit make it night"), CommandVideo, "make it night"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	if job == nil || !job.n.Free || job.n.Offer.ID != free.VideoEdit.Offer || job.n.Prompt != "make it night" ||
		len(job.n.Inputs) != 1 || len(job.n.Settings) != 0 || job.n.Estimate == nil || job.n.DedupeKey != "tg:-100:10" {
		t.Fatalf("job = %+v", job)
	}
	if a := h.files.assets[job.n.Inputs[0]]; a.ContentType != "video/mp4" || a.Seconds != 4 {
		t.Errorf("the clip kept: %+v", a)
	}
	// A clip within the 5 s goes whole.
	if len(h.files.cuts) != 1 || h.files.cuts[0] != nil {
		t.Errorf("cuts = %v", h.files.cuts)
	}
	if got := h.tg.methods("sendChatAction"); len(got) != 1 || got[0].action != "upload_video" {
		t.Errorf("chat action = %+v", got)
	}
	if got := h.tg.methods("getFile"); len(got) != 1 || got[0].file != "clip" {
		t.Errorf("getFile = %+v", got)
	}
	// The card holds the clip's first frame, its length and the free video.
	p := h.renderer.previews[0]
	if !p.Free || p.Left != 1 || p.Limit != 1 || p.Model != "FLUX Video Edit" || len(p.Chips) != 1 || p.Chips[0] != "0:04" {
		t.Errorf("card = %+v", p)
	}
	if paths := h.renderer.albums[0]; len(paths) != 1 || filepath.Base(paths[0]) != "poster.png" {
		t.Errorf("the card's picture: %v", paths)
	}
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 || sent[0].messageID != 10 || sent[0].text != "<blockquote>make it night</blockquote>" {
		t.Fatalf("preview = %+v", sent)
	}
	// Generate and No: a video run has no Model or Settings yet.
	kb := sent[0].markup.InlineKeyboard
	if len(kb) != 1 || len(kb[0]) != 2 || kb[0][0].CallbackData != callback(1, actGenerate) || kb[0][1].CallbackData != callback(1, actNo) {
		t.Errorf("buttons = %+v", kb)
	}
	run, err := h.store.ChatRun(context.Background(), 1)
	if err != nil || run.Kind != string(catalog.Video) || run.Taken != 1 || run.Note != "" {
		t.Errorf("run = %+v, %v", run, err)
	}
}

// Every way Telegram carries a clip is taken: a video, a GIF, a round video
// and a video document, in reply or as the command's own caption.
func TestEveryKindOfClipIsTaken(t *testing.T) {
	for name, m := range map[string]*tg.Message{
		"caption":  {Video: &tg.Video{FileID: "own"}},
		"gif":      {ReplyToMessage: &tg.Message{MessageID: 9, Animation: &tg.Animation{FileID: "gif"}, Document: &tg.Document{FileID: "gif-doc", MimeType: "video/mp4"}}},
		"round":    {ReplyToMessage: &tg.Message{MessageID: 9, VideoNote: &tg.VideoNote{FileID: "round"}}},
		"document": {ReplyToMessage: &tg.Message{MessageID: 9, Document: &tg.Document{FileID: "doc", MimeType: "video/quicktime"}}},
	} {
		h := videoHarness(t)
		cmd := command("/edit rain")
		cmd.Video, cmd.ReplyToMessage = m.Video, m.ReplyToMessage
		if err := h.command(cmd, CommandVideo, "rain"); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"caption": "own", "gif": "gif", "round": "round", "document": "doc"}[name]
		if got := h.tg.methods("getFile"); len(got) != 1 || got[0].file != want || h.store.jobs[1] == nil {
			t.Errorf("%s: getFile %+v, jobs %d", name, got, len(h.store.jobs))
		}
	}
}

func TestALongClipIsCutToTheFreeFiveSeconds(t *testing.T) {
	h := videoHarness(t)
	h.clips.seconds = 12.4
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	if len(h.files.cuts) != 1 || h.files.cuts[0] == nil || *h.files.cuts[0] != (media.Cut{Seconds: 5}) {
		t.Fatalf("cuts = %v", h.files.cuts)
	}
	job := h.store.jobs[1]
	if job == nil || !job.n.Free {
		t.Fatalf("job = %+v", job)
	}
	note := "<i>" + i18n.T("en", noteClipCut, "max", "5", "n", "12") + "</i>"
	if sent := h.tg.methods("sendPhoto"); len(sent) != 1 || !strings.Contains(sent[0].text, note) {
		t.Fatalf("preview = %+v, want %q", sent, note)
	}
	if p := h.renderer.previews[0]; len(p.Chips) != 1 || p.Chips[0] != "0:05" {
		t.Errorf("chips = %v", p.Chips)
	}
	if run := h.store.runs[1]; run.Note != noteClipCut {
		t.Errorf("note = %q", run.Note)
	}
}

func TestPastTheFreeVideoTheKeyTakesUpToFifteenSeconds(t *testing.T) {
	h := videoHarness(t)
	h.flow.cfg.Allowance = fakeAllowance{left: 5, videosUsed: 1}
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	h.clips.seconds = 20
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	if len(h.files.cuts) != 1 || h.files.cuts[0] == nil || *h.files.cuts[0] != (media.Cut{Seconds: 15}) {
		t.Fatalf("cuts = %v", h.files.cuts)
	}
	job := h.store.jobs[1]
	if job == nil || job.n.Free || job.n.Estimate == nil || job.n.Estimate.USD <= 0 {
		t.Fatalf("job = %+v", job)
	}
	p := h.renderer.previews[0]
	if p.Free || p.Price == "" || p.Label != i18n.T("en", "card.key", "provider", "OpenRouter") || p.Chips[0] != "0:15" {
		t.Errorf("card = %+v", p)
	}
	note := i18n.T("en", noteClipCut, "max", "15", "n", "20")
	if sent := h.tg.methods("sendPhoto"); len(sent) != 1 || !strings.Contains(sent[0].text, note) {
		t.Fatalf("preview = %+v", sent)
	}
}

// Without a key the week's limit is the answer, and nothing is downloaded.
func TestPastTheFreeVideoWithoutAKeyTheReasonIsTheAnswer(t *testing.T) {
	h := videoHarness(t)
	h.flow.cfg.Allowance = fakeAllowance{left: 5, videosUsed: 1}
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	want := i18n.T("en", "edit.limit", "limit", "1", "wait", "2 d 2 h")
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != want {
		t.Fatalf("answer = %+v, want %q", got, want)
	}
	if len(h.tg.methods("getFile")) != 0 || len(h.store.jobs) != 0 {
		t.Errorf("downloads %d, jobs %d", len(h.tg.methods("getFile")), len(h.store.jobs))
	}
}

// The meter may say a video is left while the queue refuses it: another run
// took it, or the free tier is paused.
func TestTheQueueHasTheLastWordOnTheFreeVideo(t *testing.T) {
	for _, c := range []struct {
		refuse error
		want   string
	}{
		{&free.LimitError{Kind: catalog.Video, Limit: 1, FreesAt: clock.Add(3 * time.Hour)}, i18n.T("en", "edit.limit", "limit", "1", "wait", "3 h 0 min")},
		{&free.ClosedError{}, i18n.T("en", "edit.closed")},
	} {
		h := videoHarness(t)
		h.queue.refuse = c.refuse
		if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
			t.Fatal(err)
		}
		if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != c.want {
			t.Errorf("%T: %+v", c.refuse, got)
		}
	}
	// With a key the same clip runs on it.
	h := videoHarness(t)
	h.queue.refuse = &free.ClosedError{}
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job == nil || job.n.Free || len(h.files.cuts) != 1 {
		t.Fatalf("job = %+v", job)
	}
}

func TestEditAnswersWhatItCannotTake(t *testing.T) {
	photo := photoReply("/edit rain")
	short := videoReply("/edit rain")
	huge := videoReply("/edit rain")
	huge.ReplyToMessage.Video.FileSize = storage.DefaultMaxVideo + 1
	img := videoReply("/img rain")
	for _, c := range []struct {
		name    string
		m       *tg.Message
		command string
		prompt  string
		setup   func(*harness)
		want    string
	}{
		{"no text", videoReply("/edit"), CommandVideo, "", nil, i18n.T("en", "edit.empty")},
		{"no video", command("/edit rain"), CommandVideo, "rain", nil, i18n.T("en", "edit.no_video")},
		{"a photo", photo, CommandVideo, "rain", nil, i18n.T("en", "edit.photo")},
		{"/img on a video", img, CommandImage, "rain", nil, i18n.T("en", "run.video_edit")},
		{"too short", short, CommandVideo, "rain", func(h *harness) { h.clips.seconds = 0.5 }, i18n.T("en", "edit.too_short", "min", "1")},
		{"over the cap", huge, CommandVideo, "rain", nil, i18n.T("en", "run.too_big")},
		{"over Telegram's cap", videoReply("/edit rain"), CommandVideo, "rain", func(h *harness) {
			h.tg.fail["getFile"] = &tg.APIError{ErrorCode: 400, Description: "Bad Request: file is too big"}
		}, i18n.T("en", "run.too_big")},
		{"not a clip", videoReply("/edit rain"), CommandVideo, "rain", func(h *harness) { h.clips.probeErr = media.ErrUnsupportedVideo }, i18n.T("en", "edit.unsupported")},
	} {
		h := videoHarness(t)
		if c.setup != nil {
			c.setup(h)
		}
		_ = h.command(c.m, c.command, c.prompt)
		if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != c.want {
			t.Errorf("%s: %+v, want %q", c.name, got, c.want)
		}
		if len(h.store.jobs) != 0 {
			t.Errorf("%s: %d jobs", c.name, len(h.store.jobs))
		}
	}
}

// generatingVideo is a video run after Generate.
func (h *harness) generatingVideo(t *testing.T) int64 {
	t.Helper()
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	h.files.results[1] = []storage.Asset{{ID: 50, Kind: "result", ContentType: "video/mp4", Width: 1280, Height: 720, Seconds: 4.2, Bytes: 2 << 20}}
	return 1
}

// A video comes as a reply, which notifies, however soon it is ready, and
// the loop goes.
func TestAVideoResultIsAReply(t *testing.T) {
	for _, after := range []time.Duration{10 * time.Second, 40 * time.Second} {
		h := videoHarness(t)
		id := h.generatingVideo(t)
		h.store.setStatus(id, jobs.Done, "")
		h.now = h.now.Add(after)
		h.flow.step(context.Background(), id)
		sent := h.tg.methods("sendVideo")
		if len(sent) != 1 || sent[0].messageID != 10 || sent[0].file != "upload:framy.mp4" || sent[0].video != "1080x1920 5s streams" {
			t.Fatalf("%s: sent = %+v", after, sent)
		}
		if !strings.Contains(sent[0].text, "rain") || !strings.Contains(sent[0].text, "FLUX Video Edit") {
			t.Errorf("%s: caption = %q", after, sent[0].text)
		}
		if len(h.tg.methods("editMessageMedia")) != 0 || h.clips.normalized != 1 {
			t.Errorf("%s: calls %+v", after, h.tg.calls)
		}
		if d := h.tg.methods("deleteMessage"); len(d) != 1 || d[0].messageID != h.store.runs[id].PreviewMessageID {
			t.Fatalf("%s: the loop stayed: %+v", after, d)
		}
		if h.store.stage(id) != db.StageDone {
			t.Fatalf("%s: stage %s", after, h.store.stage(id))
		}
	}
}

// A result the pipeline cannot read goes as it came, with the size framy
// read when it kept it, and is not said to stream.
func TestAVideoResultThePipelineCannotReadGoesAsItCame(t *testing.T) {
	h := videoHarness(t)
	id := h.generatingVideo(t)
	h.store.setStatus(id, jobs.Done, "")
	h.clips.normErr = media.ErrUnreadableVideo
	h.now = h.now.Add(40 * time.Second)
	h.flow.step(context.Background(), id)
	if sent := h.tg.methods("sendVideo"); len(sent) != 1 || sent[0].video != "1280x720 5s" {
		t.Fatalf("sent = %+v", sent)
	}
	if h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s", h.store.stage(id))
	}
}

func TestAVideoRunSaysItUploadsAVideo(t *testing.T) {
	h := videoHarness(t)
	id := h.generatingVideo(t)
	h.store.setStatus(id, jobs.Running, "")
	h.now = h.now.Add(5 * time.Second)
	h.flow.step(context.Background(), id)
	if got := h.tg.methods("sendChatAction"); len(got) != 1 || got[0].action != "upload_video" {
		t.Fatalf("chat action = %+v", got)
	}
}

// A video run has no panel: old or forged data for one changes nothing.
func TestAVideoRunHasNoPanel(t *testing.T) {
	h := videoHarness(t)
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	for _, action := range []string{actModel, actSettings, actBack, actPickModel + "0123abcd", actPickSetting + "0.0"} {
		if err := h.press(1, 7, action); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.tg.calls) != 0 || len(h.queue.revised) != 0 {
		t.Fatalf("calls %+v, revised %d", h.tg.calls, len(h.queue.revised))
	}
	for _, a := range h.tg.answers {
		if a != "" {
			t.Errorf("answer %q", a)
		}
	}
	// Generate still works.
	if err := h.press(1, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	if h.store.jobs[1].status != jobs.Queued {
		t.Fatalf("status %s", h.store.jobs[1].status)
	}
}

func TestADownloadThatFailsIsAFailure(t *testing.T) {
	h := videoHarness(t)
	h.tg.fail["download"] = errBoom
	if err := h.command(videoReply("/edit rain"), CommandVideo, "rain"); !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	if got := h.tg.methods("sendMessage"); len(got) != 1 || got[0].text != i18n.T("en", "run.failed") {
		t.Fatalf("answer = %+v", got)
	}
}
