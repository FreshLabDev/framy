// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/storage"
)

// withOutbox has the flow share a directory with a local Bot API server,
// and returns it.
func withOutbox(t *testing.T, h *harness) string {
	t.Helper()
	dir := t.TempDir()
	h.flow.cfg.Outbox = media.Work{Root: dir}
	return dir
}

// leftIn is what is left in the shared directory.
func leftIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// With a local server, a video result goes by its path: the server reads it
// from the shared directory, whole, and nothing of it stays there after.
// Before, tg held the whole video in memory for the upload.
func TestAVideoResultGoesByItsPath(t *testing.T) {
	h := videoHarness(t)
	dir := withOutbox(t, h)
	id := h.generatingVideo(t)
	h.store.setStatus(id, jobs.Done, "")
	h.flow.step(context.Background(), id)
	sent := h.tg.methods("sendVideo")
	if len(sent) != 1 || sent[0].file != "local:framy.mp4" || sent[0].cover != "local:cover.jpg" {
		t.Fatalf("sent = %+v", sent)
	}
	if h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s", h.store.stage(id))
	}
	if left := leftIn(t, dir); len(left) != 0 {
		t.Fatalf("left in the shared directory: %v", left)
	}
}

// A picture, and the site's results in the DM, go the same way.
func TestPicturesGoByTheirPath(t *testing.T) {
	h := newHarness(t)
	dir := withOutbox(t, h)
	id := h.doneSlow(t)
	h.flow.step(context.Background(), id)
	if sent := h.tg.methods("sendPhoto"); len(sent) != 1 || sent[0].file != "local:framy.png" {
		t.Fatalf("sent = %+v", sent)
	}
	var read string
	localRead.Range(func(path, content any) bool {
		if strings.HasPrefix(path.(string), dir) {
			read = content.(string)
		}
		return true
	})
	if read != "result" {
		t.Fatalf("the server read %q", read)
	}

	h = newHarness(t)
	dir = withOutbox(t, h)
	second := picture
	second.ID = 51
	siteResult(t, h, "image", picture, second)
	h.flow.sendSite(context.Background())
	if sent := h.tg.methods("sendMediaGroup"); len(sent) != 1 || sent[0].file != "photo:local:framy.png,photo:local:framy.png" {
		t.Fatalf("album = %+v", sent)
	}
	if left := leftIn(t, dir); len(left) != 0 {
		t.Fatalf("left in the shared directory: %v", left)
	}
}

// A server that refuses the first result sent by its path, but takes it
// uploaded, does not see the shared directory where framy does: the result
// goes, and so does every one after, uploaded at once.
func TestAServerThatCannotReadThePathGetsUploads(t *testing.T) {
	h := newHarness(t)
	withOutbox(t, h)
	h.tg.fail["local"] = &tg.APIError{Method: "sendPhoto", StatusCode: 400, ErrorCode: 400, Description: "Bad Request: file not found"}
	id := h.doneSlow(t)
	h.flow.step(context.Background(), id)
	if sent := h.tg.methods("sendPhoto"); len(sent) != 1 || sent[0].file != "upload:framy.png" || h.store.stage(id) != db.StageDone {
		t.Fatalf("stage %s, sent %+v", h.store.stage(id), sent)
	}
	if !h.flow.uploadsOnly.Load() {
		t.Fatal("the next result would be sent by its path again")
	}
	h.tg.reset()
	opened := len(h.files.opened)
	next := h.doneSlowAgain(t)
	h.flow.step(context.Background(), next)
	if sent := h.tg.methods("sendPhoto"); len(sent) != 1 || sent[0].file != "upload:framy.png" || len(h.files.opened) != opened+1 {
		t.Fatalf("sent %+v, read %d times", sent, len(h.files.opened)-opened)
	}
}

// A refusal of the upload as well is the send's own: the run ends as one
// Telegram refuses, and the next result still goes by its path. Once a
// result went by its path, a 400 is the send's own at once.
func TestARefusalOfASendByPathIsTheSendsOwn(t *testing.T) {
	h := newHarness(t)
	withOutbox(t, h)
	h.tg.fail["sendPhoto"] = &tg.APIError{Method: "sendPhoto", StatusCode: 400, ErrorCode: 400, Description: "Bad Request: message thread not found"}
	id := h.doneSlow(t)
	h.flow.step(context.Background(), id)
	if h.store.stage(id) != db.StageGone || h.flow.uploadsOnly.Load() {
		t.Fatalf("stage %s, uploads only %v", h.store.stage(id), h.flow.uploadsOnly.Load())
	}

	h = newHarness(t)
	withOutbox(t, h)
	id = h.doneSlow(t)
	h.flow.step(context.Background(), id)
	if sent := h.tg.methods("sendPhoto"); len(sent) != 1 || sent[0].file != "local:framy.png" {
		t.Fatalf("sent %+v", sent)
	}
	h.tg.fail["local"] = &tg.APIError{Method: "sendPhoto", StatusCode: 400, ErrorCode: 400, Description: "Bad Request: chat not found"}
	opened := len(h.files.opened)
	next := h.doneSlowAgain(t)
	h.flow.step(context.Background(), next)
	if len(h.files.opened) != opened+1 || h.store.stage(next) != db.StageGone || h.flow.uploadsOnly.Load() {
		t.Fatalf("read %d times, stage %s", len(h.files.opened)-opened, h.store.stage(next))
	}
}

// A file framy cannot write to the shared directory is a failure of the
// send, and its error names no path: the directory is named after the
// bot's token.
func TestAFileNotWrittenNamesNoPath(t *testing.T) {
	o := &outbox{work: media.Work{Root: "/nonexistent-root-of-the-bot-token-1234:ABCD"}}
	_, err := o.file(nopCloser{strings.NewReader("x")}, "framy.png")
	if err == nil || strings.Contains(err.Error(), "1234:ABCD") {
		t.Fatalf("err = %v", err)
	}
}

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

// doneSlowAgain is a second run of the chat, done as doneSlow's.
func (h *harness) doneSlowAgain(t *testing.T) int64 {
	t.Helper()
	m := photoReply("/img a second fox")
	m.MessageID = 20
	if err := h.command(m, CommandImage, "a second fox"); err != nil {
		t.Fatal(err)
	}
	id := int64(len(h.store.jobs))
	if err := h.press(id, 7, actGenerate); err != nil {
		t.Fatal(err)
	}
	h.store.setStatus(id, jobs.Done, "")
	h.files.results[id] = []storage.Asset{{ID: 60, ContentType: "image/png", Bytes: 1 << 20, Width: 1024, Height: 1024}}
	h.now = h.now.Add(40 * time.Second)
	return id
}
