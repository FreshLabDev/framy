// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"net"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/storage"
)

// siteJob is job 40 of person 7, made on the site and done.
const siteJob = 40

// siteResult makes job 40 a done site job with the results.
func siteResult(t *testing.T, h *harness, kind string, results ...storage.Asset) {
	t.Helper()
	_, offer, err := h.store.Offer(context.Background(), free.ImageMAI.Offer)
	if err != nil {
		t.Fatal(err)
	}
	h.store.jobs[siteJob] = &fakeJob{n: jobs.New{UserID: 7, Surface: jobs.Site, Prompt: "a fox", Offer: offer, Free: true}, status: jobs.Done}
	h.store.site = append(h.store.site, db.SiteResult{JobID: siteJob, UserID: 7, Prompt: "a fox", Kind: kind, Model: "MAI-Image-2.6 Flash", Deliver: true})
	h.store.userLang[7] = "en"
	h.files.results[siteJob] = results
	if h.files.assets == nil {
		h.files.assets = map[int64]storage.Asset{}
	}
	for _, a := range results {
		h.files.assets[a.ID] = a
	}
}

func telegramError(code int, text string) *tg.APIError {
	return &tg.APIError{Method: "sendChatAction", StatusCode: code, ErrorCode: code, Description: text}
}

// An image made on the site arrives in the DM as a new message, with the
// prompt, the model and Animate, once.
func TestASiteImageArrivesInTheDM(t *testing.T) {
	h := newHarness(t)
	siteResult(t, h, "image", picture)
	h.flow.sendSite(context.Background())

	if want := clock.Add(-siteWithin); !h.store.siteSince.Equal(want) {
		t.Errorf("since %v, want %v", h.store.siteSince, want)
	}
	if a := h.tg.methods("sendChatAction"); len(a) != 1 || a[0].chatID != 7 || a[0].action != "upload_photo" {
		t.Fatalf("action = %+v", a)
	}
	sent := h.tg.methods("sendPhoto")
	if len(sent) != 1 || sent[0].chatID != 7 || sent[0].messageID != 0 || sent[0].file != "upload:framy.png" {
		t.Fatalf("sent = %+v", sent)
	}
	if !strings.Contains(sent[0].text, "a fox") || !strings.Contains(sent[0].text, "MAI-Image-2.6 Flash") {
		t.Errorf("caption = %q", sent[0].text)
	}
	if kb := sent[0].markup; kb == nil || !reflect.DeepEqual(kb.InlineKeyboard[0][0].CallbackData, callback(siteJob, actAnimate)) {
		t.Fatalf("keyboard = %+v", kb)
	}
	if h.store.delivery(siteJob) != db.DeliverySent {
		t.Fatalf("delivery %q", h.store.delivery(siteJob))
	}
	r := h.store.runs[siteJob]
	if r == nil || r.Stage != db.StageDone || r.ChatID != 7 || r.Lang != "en" || r.ResultMessageID != h.tg.next {
		t.Fatalf("run = %+v", r)
	}

	h.tg.reset()
	h.flow.sendSite(context.Background())
	if len(h.tg.calls) != 0 {
		t.Fatalf("sent again: %+v", h.tg.calls)
	}
}

// Animate on a result from the site makes a video of it, as on any result
// in the DM.
func TestAnimateOnASiteResult(t *testing.T) {
	h := newHarness(t)
	withHeyGen(h)
	siteResult(t, h, "image", picture)
	h.flow.sendSite(context.Background())
	result := h.store.runs[siteJob].ResultMessageID
	h.tg.reset()

	if err := h.animate(siteJob, 7, dm, "cq-1"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	if job == nil || job.n.Surface != jobs.DM || job.n.Offer.ID != free.VideoHeyGen.Offer || !reflect.DeepEqual(job.n.Inputs, []int64{picture.ID}) ||
		job.n.Prompt != "a fox" {
		t.Fatalf("job = %+v", job)
	}
	if sent := h.tg.methods("sendRichMessage"); len(sent) != 1 || sent[0].chatID != 7 || sent[0].messageID != result || sent[0].ephemeral != nil {
		t.Fatalf("preview = %+v", sent)
	}
}

// A video made on the site arrives with its cover, and with no keyboard.
func TestASiteVideoArrivesWithItsCover(t *testing.T) {
	h := newHarness(t)
	siteResult(t, h, "video", storage.Asset{ID: 60, UserID: 7, Kind: "result", ContentType: "video/mp4", Width: 1280, Height: 720, Seconds: 4.2, Bytes: 2 << 20})
	h.flow.sendSite(context.Background())
	if a := h.tg.methods("sendChatAction"); len(a) != 1 || a[0].action != "upload_video" {
		t.Fatalf("action = %+v", a)
	}
	sent := h.tg.methods("sendVideo")
	if len(sent) != 1 || sent[0].chatID != 7 || sent[0].cover != "upload:cover.jpg" || sent[0].video != "1080x1920 5s streams" || sent[0].markup != nil {
		t.Fatalf("sent = %+v", sent)
	}
	if h.store.delivery(siteJob) != db.DeliverySent || h.store.runs[siteJob].ResultMessageID == 0 {
		t.Fatalf("delivery %q, run %+v", h.store.delivery(siteJob), h.store.runs[siteJob])
	}
	if res := h.store.results[siteJob]; res.FileType != db.FileVideo || res.FileID != "video-file" {
		t.Fatalf("result = %+v", res)
	}
}

// A run Continue joined to its clip arrives as the whole clip, and the run
// remembers that its message is the joined clip, so Send to Telegram does not
// take it for the run's own video.
func TestASiteContinuationArrivesJoined(t *testing.T) {
	h := newHarness(t)
	siteResult(t, h, "video", storage.Asset{ID: 60, UserID: 7, Kind: "result", Key: "u/7/out/40-1.mp4", ContentType: "video/mp4",
		Width: 1280, Height: 720, Seconds: 5, Bytes: 2 << 20})
	h.files.joined = map[int64]storage.Asset{siteJob: {ID: 61, UserID: 7, Kind: "joined", JobID: siteJob, Key: "u/7/out/40-joined.mp4",
		ContentType: "video/mp4", Width: 1280, Height: 720, Seconds: 9.96, Bytes: 4 << 20}}
	h.flow.sendSite(context.Background())

	if sent := h.tg.methods("sendVideo"); len(sent) != 1 || sent[0].chatID != 7 {
		t.Fatalf("sent = %+v", sent)
	}
	if !reflect.DeepEqual(h.files.opened, []string{"u/7/out/40-joined.mp4"}) {
		t.Fatalf("read %q, want the joined clip only", h.files.opened)
	}
	if res := h.store.results[siteJob]; res.FileID != "video-file" || res.AssetID != 61 {
		t.Fatalf("result = %+v", res)
	}
	if h.store.delivery(siteJob) != db.DeliverySent {
		t.Fatalf("delivery %q", h.store.delivery(siteJob))
	}
}

// Several pictures go as one album, the caption on the first; one that is
// no photo makes them all documents.
func TestASiteResultOfSeveralPicturesIsAnAlbum(t *testing.T) {
	second := picture
	second.ID = 51
	big := picture
	big.ID, big.Bytes = 52, 12<<20
	for name, c := range map[string]struct {
		results []storage.Asset
		files   string
	}{
		"photos":    {[]storage.Asset{picture, second}, "photo:upload:framy.png,photo:upload:framy.png"},
		"documents": {[]storage.Asset{picture, big}, "document:upload:framy.png,document:upload:framy.png"},
	} {
		h := newHarness(t)
		siteResult(t, h, "image", c.results...)
		h.flow.sendSite(context.Background())
		sent := h.tg.methods("sendMediaGroup")
		if len(sent) != 1 || sent[0].chatID != 7 || sent[0].file != c.files || !strings.Contains(sent[0].text, "a fox") {
			t.Fatalf("%s: sent = %+v", name, sent)
		}
		if r := h.store.runs[siteJob]; r == nil || r.ResultMessageID != h.tg.next-1 {
			t.Fatalf("%s: run = %+v, next %d", name, r, h.tg.next)
		}
	}
}

// A person who did not allow the bot to write gets nothing, and is not
// asked again.
func TestASiteResultIsNotSentWhereTheBotMayNotWrite(t *testing.T) {
	for _, err := range []*tg.APIError{
		telegramError(403, "Forbidden: bot can't initiate conversation with a user"),
		telegramError(403, "Forbidden: bot was blocked by the user"),
		telegramError(400, "Bad Request: chat not found"),
	} {
		h := newHarness(t)
		siteResult(t, h, "image", picture)
		h.tg.fail["sendChatAction"] = err
		h.flow.sendSite(context.Background())
		if h.store.delivery(siteJob) != db.DeliveryClosed || len(h.tg.calls) != 0 || h.store.runs[siteJob] != nil {
			t.Fatalf("%s: delivery %q, calls %+v", err.Description, h.store.delivery(siteJob), h.tg.calls)
		}
		delete(h.tg.fail, "sendChatAction")
		h.flow.sendSite(context.Background())
		if len(h.tg.calls) != 0 {
			t.Fatalf("%s: asked again: %+v", err.Description, h.tg.calls)
		}
	}
}

// A person who turned the DM off in the settings gets nothing, and the
// result is not sent later either.
func TestASiteResultIsNotSentWhenTheDMIsOff(t *testing.T) {
	h := newHarness(t)
	siteResult(t, h, "image", picture)
	h.store.site[0].Deliver = false
	h.flow.sendSite(context.Background())
	if h.store.delivery(siteJob) != db.DeliveryOff || len(h.tg.calls) != 0 || h.store.runs[siteJob] != nil {
		t.Fatalf("delivery %q, calls %+v", h.store.delivery(siteJob), h.tg.calls)
	}
	h.store.site[0].Deliver = true
	h.flow.sendSite(context.Background())
	if len(h.tg.calls) != 0 {
		t.Fatalf("sent after the DM was turned on: %+v", h.tg.calls)
	}
}

// A chat action that fails for another reason sent nothing: the result is
// tried again, 2 s later.
func TestASiteResultIsTriedAgainAfterAHiccup(t *testing.T) {
	h := newHarness(t)
	siteResult(t, h, "image", picture)
	h.tg.fail["sendChatAction"] = telegramError(502, "Bad Gateway")
	h.flow.sendSite(context.Background())
	if h.store.delivery(siteJob) != "" || len(h.tg.calls) != 0 {
		t.Fatalf("delivery %q, calls %+v", h.store.delivery(siteJob), h.tg.calls)
	}
	delete(h.tg.fail, "sendChatAction")
	h.flow.sendSite(context.Background())
	if len(h.tg.calls) != 0 {
		t.Fatalf("tried again at once: %+v", h.tg.calls)
	}
	h.now = h.now.Add(retryFirst)
	h.flow.sendSite(context.Background())
	if h.store.delivery(siteJob) != db.DeliverySent || len(h.tg.methods("sendPhoto")) != 1 {
		t.Fatalf("delivery %q, calls %+v", h.store.delivery(siteJob), h.tg.calls)
	}
	if _, ok := h.flow.siteRetries.Load(int64(siteJob)); ok {
		t.Fatal("the retry is kept after the result went")
	}
}

// A send that never reached Telegram took nothing to the DM: the file was
// not read from the bucket, or the Bot API server was not there to connect
// to, as while it restarts. The result is given back and tried again, less
// often each time, and goes once the hiccup is over. Before, it was marked
// failed and the person never got it.
func TestASiteResultNothingOfWhichWentIsTriedAgain(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	for name, c := range map[string]struct {
		fail func(h *harness)
		heal func(h *harness)
	}{
		"bucket": {func(h *harness) { h.files.openErr = errBoom }, func(h *harness) { h.files.openErr = nil }},
		"server": {func(h *harness) { h.tg.fail["sendPhoto"] = refused }, func(h *harness) { delete(h.tg.fail, "sendPhoto") }},
	} {
		h := newHarness(t)
		siteResult(t, h, "image", picture)
		c.fail(h)
		h.flow.sendSite(context.Background())
		if h.store.delivery(siteJob) != "" || len(h.tg.methods("sendPhoto")) != 0 {
			t.Fatalf("%s: delivery %q, calls %+v", name, h.store.delivery(siteJob), h.tg.calls)
		}
		// The second failure waits twice as long.
		h.now = h.now.Add(retryFirst)
		h.flow.sendSite(context.Background())
		h.tg.reset()
		c.heal(h)
		h.now = h.now.Add(2*retryFirst - time.Second)
		h.flow.sendSite(context.Background())
		if len(h.tg.calls) != 0 {
			t.Fatalf("%s: tried before its time: %+v", name, h.tg.calls)
		}
		h.now = h.now.Add(time.Second)
		h.flow.sendSite(context.Background())
		if h.store.delivery(siteJob) != db.DeliverySent || len(h.tg.methods("sendPhoto")) != 1 {
			t.Fatalf("%s: delivery %q, calls %+v", name, h.store.delivery(siteJob), h.tg.calls)
		}
	}
}

// A send Telegram refused, or one whose answer was lost and that may have
// reached the DM, is not tried again: a result goes there at most once.
func TestASiteResultTelegramRefusedIsNotSentAgain(t *testing.T) {
	for _, err := range []error{
		errBoom,
		telegramError(502, "Bad Gateway"),
		telegramError(400, "Bad Request: wrong file identifier/HTTP URL specified"),
	} {
		h := newHarness(t)
		siteResult(t, h, "image", picture)
		h.tg.fail["sendPhoto"] = err
		h.flow.sendSite(context.Background())
		if h.store.delivery(siteJob) != db.DeliveryFailed || h.store.runs[siteJob] != nil {
			t.Fatalf("%v: delivery %q, run %+v", err, h.store.delivery(siteJob), h.store.runs[siteJob])
		}
		delete(h.tg.fail, "sendPhoto")
		h.tg.reset()
		h.now = h.now.Add(time.Hour)
		h.flow.sendSite(context.Background())
		if len(h.tg.calls) != 0 {
			t.Fatalf("%v: sent again: %+v", err, h.tg.calls)
		}
	}
}

// A result that kept failing until its hour was over is no longer read, and
// the flow forgets when it was to be tried again.
func TestSiteRetriesAreForgotten(t *testing.T) {
	h := newHarness(t)
	siteResult(t, h, "image", picture)
	h.files.openErr = errBoom
	h.flow.sendSite(context.Background())
	if _, ok := h.flow.siteRetries.Load(int64(siteJob)); !ok {
		t.Fatal("no retry kept")
	}
	h.store.site = nil // its hour is over
	h.now = h.now.Add(siteWithin + retryFirst + time.Second)
	h.flow.sendSite(context.Background())
	if _, ok := h.flow.siteRetries.Load(int64(siteJob)); ok {
		t.Fatal("the retry is kept after the result's hour")
	}
}

// A done job with no file has nothing to send.
func TestASiteResultWithNoFileFails(t *testing.T) {
	h := newHarness(t)
	siteResult(t, h, "image")
	h.flow.sendSite(context.Background())
	if h.store.delivery(siteJob) != db.DeliveryFailed || len(h.tg.calls) != 0 {
		t.Fatalf("delivery %q, calls %+v", h.store.delivery(siteJob), h.tg.calls)
	}
}

// Run sends the site's results beside the chat runs, and both stop with
// ctx.
func TestRunSendsTheSiteResults(t *testing.T) {
	h := newHarness(t)
	siteResult(t, h, "image", picture)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.flow.Run(ctx)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for h.store.delivery(siteJob) != db.DeliverySent {
		select {
		case <-done:
			t.Fatal("Run ended")
		case <-deadline:
			t.Fatal("nothing sent")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}
