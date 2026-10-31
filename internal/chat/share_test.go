// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
)

const shareSite = "https://framy.test"

// shareHarness has person 7's result src as asset src.AssetID, and the site
// Telegram fetches from.
func shareHarness(t *testing.T, src db.ShareSource) *harness {
	t.Helper()
	h := newHarness(t)
	h.flow.cfg.AppURL = shareSite
	h.store.shares = map[int64]db.ShareSource{src.AssetID: src}
	h.store.shareOwner = map[int64]int64{src.AssetID: 7}
	h.files.blobs = map[string][]byte{}
	return h
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+3] = 200, 255
	}
	// The left tenth is see-through.
	for y := range h {
		for x := range w / 10 {
			img.SetNRGBA(x, y, color.NRGBA{})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fetch is Telegram's GET of a link of a shared result.
func (h *harness) fetch(t *testing.T, link string) *httptest.ResponseRecorder {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil || !strings.HasPrefix(link, shareSite+SharePath) {
		t.Fatalf("not a link of the site: %q", link)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+SharePath+"{token}", h.flow.ServeShare)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u.Path, nil))
	return rec
}

func jpegSize(t *testing.T, rec *httptest.ResponseRecorder) image.Image {
	t.Helper()
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("%d %q: %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	img, err := jpeg.Decode(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// A result that went out in a chat goes again by its file_id, as what it
// went as, with the caption it had there.
func TestASentResultIsSharedByItsFileID(t *testing.T) {
	caption := resultCaption("a fox", "Nano Banana")
	for fileType, want := range map[string]tg.InlineQueryResult{
		db.FilePhoto:     tg.InlineQueryResultCachedPhoto{ID: "f50", PhotoFileID: "file-1", Caption: caption},
		db.FileVideo:     tg.InlineQueryResultCachedVideo{ID: "f50", VideoFileID: "file-1", Title: "a fox", Caption: caption},
		db.FileAnimation: tg.InlineQueryResultCachedMpeg4Gif{ID: "f50", Mpeg4FileID: "file-1", Caption: caption},
	} {
		h := shareHarness(t, db.ShareSource{AssetID: 50, JobID: 9, ContentType: "image/png", Prompt: "a fox", Model: "Nano Banana",
			FileID: "file-1", FileType: fileType})
		h.flow.cfg.AppURL = "" // nothing is fetched
		got, err := h.flow.Share(context.Background(), 7, 50)
		if err != nil {
			t.Fatalf("%s: %v", fileType, err)
		}
		if got != (Shared{ID: "prep-1", Expires: clock.Add(24 * time.Hour)}) || !reflect.DeepEqual(h.tg.prepared, []tg.InlineQueryResult{want}) {
			t.Errorf("%s: %+v, %+v", fileType, got, h.tg.prepared)
		}
	}
}

// A picture that never went out goes as a JPEG framy makes when Telegram
// fetches it: 2560 px on the long side at most, see-through parts white,
// with a thumbnail of 320. The picture is a strip, so the test scales few
// pixels.
func TestAPictureIsSharedAsAJPEGTelegramFetches(t *testing.T) {
	h := shareHarness(t, db.ShareSource{AssetID: 50, JobID: 9, Key: "u/7/r/50.png", ContentType: "image/png",
		Width: 3000, Height: 50, Prompt: "a fox", Model: "Nano Banana", FileType: db.FileDocument})
	h.files.blobs["u/7/r/50.png"] = pngOf(t, 3000, 50)
	if _, err := h.flow.Share(context.Background(), 7, 50); err != nil {
		t.Fatal(err)
	}
	photo, ok := h.tg.prepared[0].(tg.InlineQueryResultPhoto)
	if !ok || photo.ID != "f50" || photo.PhotoWidth != 2560 || photo.PhotoHeight != 42 || photo.Caption != resultCaption("a fox", "Nano Banana") ||
		!strings.HasSuffix(photo.PhotoURL, ".jpg") || !strings.HasSuffix(photo.ThumbnailURL, ".jpg") || photo.PhotoURL == photo.ThumbnailURL {
		t.Fatalf("result = %+v", h.tg.prepared[0])
	}
	img := jpegSize(t, h.fetch(t, photo.PhotoURL))
	if b := img.Bounds(); b.Dx() != 2560 || b.Dy() != 42 {
		t.Fatalf("photo %v", b)
	}
	if r, g, b, _ := img.At(20, 20).RGBA(); r>>8 < 200 || g>>8 < 200 || b>>8 < 200 {
		t.Errorf("the see-through corner is %d %d %d, not white", r>>8, g>>8, b>>8)
	}
	if b := jpegSize(t, h.fetch(t, photo.ThumbnailURL)).Bounds(); b.Dx() != 320 || b.Dy() != 5 {
		t.Fatalf("thumbnail %v", b)
	}
}

// A video that never went out goes by a link to it as it is kept, with a
// cover framy makes of its first frame.
func TestAVideoIsSharedWithItsCover(t *testing.T) {
	h := shareHarness(t, db.ShareSource{AssetID: 60, JobID: 9, Key: "u/7/r/60.mp4", ContentType: "video/mp4", Bytes: 11,
		Width: 1280, Height: 720, Seconds: 4.6, Prompt: "rain", Model: "HeyGen Video"})
	h.files.blobs["u/7/r/60.mp4"] = []byte("video bytes")
	h.clips.poster = image.Pt(1280, 720)
	if _, err := h.flow.Share(context.Background(), 7, 60); err != nil {
		t.Fatal(err)
	}
	v, ok := h.tg.prepared[0].(tg.InlineQueryResultVideo)
	if !ok || v.ID != "f60" || v.Title != "rain" || v.VideoWidth != 1280 || v.VideoHeight != 720 || v.VideoDuration != 5 ||
		!strings.HasSuffix(v.VideoURL, ".mp4") || !strings.HasSuffix(v.ThumbnailURL, ".jpg") {
		t.Fatalf("result = %+v", h.tg.prepared[0])
	}
	rec := h.fetch(t, v.VideoURL)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "video/mp4" || rec.Body.String() != "video bytes" ||
		rec.Header().Get("Content-Length") != "11" {
		t.Fatalf("video: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	if b := jpegSize(t, h.fetch(t, v.ThumbnailURL)).Bounds(); b.Dx() != 320 || b.Dy() != 180 {
		t.Fatalf("cover %v", b)
	}
}

// A link framy did not sign, one that ended, one for a part the result has
// not, and one of a result deleted since serve nothing.
func TestALinkServesOnlyWhatFramySignedWhileItLasts(t *testing.T) {
	h := shareHarness(t, db.ShareSource{AssetID: 50, JobID: 9, Key: "u/7/r/50.png", ContentType: "image/png", Width: 8, Height: 8})
	h.files.blobs["u/7/r/50.png"] = pngOf(t, 8, 8)
	if _, err := h.flow.Share(context.Background(), 7, 50); err != nil {
		t.Fatal(err)
	}
	link := h.tg.prepared[0].(tg.InlineQueryResultPhoto).PhotoURL
	jpegSize(t, h.fetch(t, link))

	token := strings.TrimSuffix(strings.TrimPrefix(link, shareSite+SharePath), ".jpg")
	forged := []byte(token)
	forged[3] ^= 1
	video := h.flow.shareURL(7, 50, shareVideo)
	for name, l := range map[string]string{
		"forged":            shareSite + SharePath + string(forged) + ".jpg",
		"cut":               shareSite + SharePath + token[:20] + ".jpg",
		"not base64":        shareSite + SharePath + "!!!.jpg",
		"a picture's video": video,
	} {
		if rec := h.fetch(t, l); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	h.now = h.now.Add(shareTTL)
	if rec := h.fetch(t, link); rec.Code != http.StatusNotFound {
		t.Errorf("an ended link: %d", rec.Code)
	}
	h.now = clock
	delete(h.store.shares, 50)
	if rec := h.fetch(t, link); rec.Code != http.StatusNotFound {
		t.Errorf("a deleted result: %d", rec.Code)
	}
}

// What Telegram cannot be given is refused before anything is saved, and
// a file that is not the person's result is not found.
func TestWhatCannotGoToTelegramIsNotShared(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		src  db.ShareSource
		site string
	}{
		"a video over 20 MB": {db.ShareSource{AssetID: 60, ContentType: "video/mp4", Bytes: 21 << 20}, shareSite},
		"a WebM video":       {db.ShareSource{AssetID: 60, ContentType: "video/webm", Bytes: 1 << 20}, shareSite},
		"an AVIF picture":    {db.ShareSource{AssetID: 60, ContentType: "image/avif", Bytes: 1 << 20}, shareSite},
		"no https site":      {db.ShareSource{AssetID: 60, ContentType: "image/png", Bytes: 1 << 20}, ""},
	} {
		h := shareHarness(t, c.src)
		h.flow.cfg.AppURL = c.site
		if _, err := h.flow.Share(ctx, 7, 60); !errors.Is(err, ErrNotShareable) || len(h.tg.calls) != 0 {
			t.Errorf("%s: %v, calls %+v", name, err, h.tg.calls)
		}
	}
	h := shareHarness(t, db.ShareSource{AssetID: 50, ContentType: "image/png"})
	if _, err := h.flow.Share(ctx, 8, 50); !errors.Is(err, db.ErrNoResult) || len(h.tg.calls) != 0 {
		t.Errorf("someone else's: %v", err)
	}
}
