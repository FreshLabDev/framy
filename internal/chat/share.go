// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // results come as PNG, JPEG or WebP
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/storage"
)

// Send to Telegram from the Mini App (docs/design.md, Mini App): framy saves
// the result as a prepared inline message for the person, the Mini App hands
// its id to shareMessage, and the person picks the chat. A result that went
// out in a chat goes by its file_id, as in inline mode. Any other goes by
// URL, which Telegram fetches from framy: a photo has to be a JPEG of at most
// 5 MB with a JPEG thumbnail, a video an MP4 with a JPEG cover. The links are
// signed with a key made at start and live an hour, so a share does not
// outlive a restart of the process.

const (
	// shareTTL is how long the links of a prepared message work.
	shareTTL = time.Hour
	// sharePhotoSide is the long side of a shared photo; Telegram takes a
	// photo of up to 10000 px across both sides.
	sharePhotoSide = 2560
	// sharePhotoBytes is Telegram's limit on a photo by URL.
	sharePhotoBytes = 5 << 20
	// shareThumbSide is the long side of a thumbnail and a video's cover.
	shareThumbSide = 320
	// shareVideoBytes is Telegram's limit on a file it fetches by URL.
	shareVideoBytes = 20 << 20
	// shareReadBytes caps a picture read into memory to convert.
	shareReadBytes = 64 << 20
	// shareSlots is how many files are converted at once, shareWait how
	// long a fetch waits for one.
	shareSlots = 2
	shareWait  = 20 * time.Second
)

// ErrNotShareable: the result cannot go to Telegram from framy: a video over
// the 20 MB Telegram fetches, a file that is no picture or video, or no
// https site to fetch it from.
var ErrNotShareable = errors.New("this result cannot be sent to Telegram")

// The parts a link serves.
const (
	sharePhoto byte = 'p'
	shareThumb byte = 't'
	shareVideo byte = 'v'
	shareCover byte = 'c'
)

// Shared is a prepared message: the id the Mini App passes to
// shareMessage, and when it stops working.
type Shared struct {
	ID      string
	Expires time.Time
}

// Share prepares result assetID of userID's for shareMessage. It answers
// db.ErrNoResult for a file that is not a done result of theirs, and
// ErrNotShareable for one Telegram cannot be given.
func (f *Flow) Share(ctx context.Context, userID, assetID int64) (Shared, error) {
	src, err := f.cfg.Store.ShareSource(ctx, userID, assetID)
	if err != nil {
		return Shared{}, err
	}
	result, err := f.shareResult(userID, src)
	if err != nil {
		return Shared{}, err
	}
	p, err := f.cfg.Telegram.SavePreparedInlineMessage(ctx, userID, result, tg.AnyChat)
	if err != nil {
		return Shared{}, err
	}
	return Shared{ID: p.ID, Expires: time.Unix(p.ExpirationDate, 0).UTC()}, nil
}

// shareResult is the inline result src goes as.
func (f *Flow) shareResult(userID int64, src db.ShareSource) (tg.InlineQueryResult, error) {
	id := "f" + strconv.FormatInt(src.AssetID, 10)
	caption := resultCaption(src.Prompt, src.Model)
	title := cmp.Or(inlineTitleOf(src.Prompt), src.Model)
	video := strings.HasPrefix(src.ContentType, "video/")
	switch {
	case src.FileID != "" && src.FileType == db.FilePhoto:
		return tg.InlineQueryResultCachedPhoto{ID: id, PhotoFileID: src.FileID, Caption: caption}, nil
	case src.FileID != "" && src.FileType == db.FileVideo:
		return tg.InlineQueryResultCachedVideo{ID: id, VideoFileID: src.FileID, Title: title, Caption: caption}, nil
	case src.FileID != "" && src.FileType == db.FileAnimation:
		return tg.InlineQueryResultCachedMpeg4Gif{ID: id, Mpeg4FileID: src.FileID, Caption: caption}, nil
	case f.cfg.AppURL == "":
		return nil, fmt.Errorf("%w: no https site for Telegram to fetch it from", ErrNotShareable)
	case video && src.ContentType == "video/mp4":
		if src.Bytes > shareVideoBytes {
			return nil, fmt.Errorf("%w: a video of %d bytes", ErrNotShareable, src.Bytes)
		}
		w, h := fit(src.Width, src.Height, 0)
		return tg.InlineQueryResultVideo{
			ID: id, VideoURL: f.shareURL(userID, src.AssetID, shareVideo), ThumbnailURL: f.shareURL(userID, src.AssetID, shareCover),
			Title: title, Caption: caption, VideoWidth: w, VideoHeight: h, VideoDuration: int(src.Seconds + 0.5),
		}, nil
	case shareable(src.ContentType):
		w, h := fit(src.Width, src.Height, sharePhotoSide)
		return tg.InlineQueryResultPhoto{
			ID: id, PhotoURL: f.shareURL(userID, src.AssetID, sharePhoto), ThumbnailURL: f.shareURL(userID, src.AssetID, shareThumb),
			PhotoWidth: w, PhotoHeight: h, Caption: caption,
		}, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotShareable, src.ContentType)
}

// shareable: a picture framy can turn into a JPEG.
func shareable(contentType string) bool {
	switch contentType {
	case "image/jpeg", "image/png", "image/webp":
		return true
	}
	return false
}

// fit is w×h brought down to a long side of side, or as it is for 0.
func fit(w, h, side int) (int, int) {
	if side <= 0 || w <= 0 || h <= 0 || max(w, h) <= side {
		return w, h
	}
	if w >= h {
		return side, max(1, h*side/w)
	}
	return max(1, w*side/h), side
}

// ---- the links ----

// shareToken is the person, the file, the part and when the link ends,
// with their MAC.
const shareTokenBytes = 8 + 8 + 1 + 8 + 16

func (f *Flow) shareURL(userID, assetID int64, part byte) string {
	b := make([]byte, 0, shareTokenBytes)
	b = binary.BigEndian.AppendUint64(b, uint64(userID))
	b = binary.BigEndian.AppendUint64(b, uint64(assetID))
	b = append(b, part)
	b = binary.BigEndian.AppendUint64(b, uint64(f.now().Add(shareTTL).Unix()))
	b = append(b, f.shareMAC(b)...)
	ext := ".jpg"
	if part == shareVideo || part == shareLoop {
		ext = ".mp4"
	}
	return strings.TrimRight(f.cfg.AppURL, "/") + SharePath + base64.RawURLEncoding.EncodeToString(b) + ext
}

func (f *Flow) shareMAC(b []byte) []byte {
	m := hmac.New(sha256.New, f.shareKey[:])
	m.Write(b)
	return m.Sum(nil)[:16]
}

// openShareToken reads a link's token: ok is false for one framy did not
// sign or that has ended.
func (f *Flow) openShareToken(token string) (userID, assetID int64, part byte, ok bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != shareTokenBytes {
		return 0, 0, 0, false
	}
	body, mac := b[:shareTokenBytes-16], b[shareTokenBytes-16:]
	if !hmac.Equal(mac, f.shareMAC(body)) {
		return 0, 0, 0, false
	}
	userID = int64(binary.BigEndian.Uint64(body[0:8]))
	assetID = int64(binary.BigEndian.Uint64(body[8:16]))
	part = body[16]
	ends := time.Unix(int64(binary.BigEndian.Uint64(body[17:25])), 0)
	if !f.now().Before(ends) {
		return 0, 0, 0, false
	}
	return userID, assetID, part, true
}

// ---- serving them ----

// SharePath is where the links of shared results are served: GET
// SharePath+"{token}".
const SharePath = "/share/"

// ServeShare answers Telegram's fetch of a shared result's part: the JPEG
// of a picture or its thumbnail, a video or its cover. A link framy did not
// sign, one that ended, and a result deleted since are 404.
func (f *Flow) ServeShare(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	token = strings.TrimSuffix(strings.TrimSuffix(token, ".jpg"), ".mp4")
	userID, assetID, part, ok := f.openShareToken(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if guestPart(part) {
		f.serveGuest(w, r, userID, assetID, part)
		return
	}
	ctx := r.Context()
	src, err := f.cfg.Store.ShareSource(ctx, userID, assetID)
	if errors.Is(err, db.ErrNoResult) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		f.log.Warn("shared result unread", "asset", assetID, "error", err)
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	video := strings.HasPrefix(src.ContentType, "video/")
	if video != (part == shareVideo || part == shareCover) {
		http.NotFound(w, r)
		return
	}
	asset := storage.Asset{ID: src.AssetID, UserID: userID, Kind: "result", JobID: src.JobID, Key: src.Key, ContentType: src.ContentType, Bytes: src.Bytes}
	if part == shareVideo {
		f.serveSharedVideo(w, r, asset)
		return
	}
	if !f.takeShareSlot(w, r) {
		return
	}
	defer func() { <-f.shareSlots }()
	var out []byte
	switch part {
	case sharePhoto:
		out, err = f.sharedPicture(ctx, asset, sharePhotoSide)
	case shareThumb:
		out, err = f.sharedPicture(ctx, asset, shareThumbSide)
	case shareCover:
		out, err = f.sharedCover(ctx, asset)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		if ctx.Err() == nil {
			f.log.Warn("shared result not made", "asset", assetID, "part", string(part), "error", err)
			http.Error(w, "not made", http.StatusUnprocessableEntity)
		}
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	_, _ = w.Write(out)
}

// takeShareSlot waits for a turn to make a picture, at most shareWait, and
// answers the request itself when it gets none. The caller gives the slot
// back.
func (f *Flow) takeShareSlot(w http.ResponseWriter, r *http.Request) bool {
	wait := time.NewTimer(shareWait)
	defer wait.Stop()
	select {
	case f.shareSlots <- struct{}{}:
		return true
	case <-r.Context().Done():
		return false
	case <-wait.C:
		http.Error(w, "busy", http.StatusServiceUnavailable)
		return false
	}
}

// serveSharedVideo passes the video on from the bucket as it is.
func (f *Flow) serveSharedVideo(w http.ResponseWriter, r *http.Request, a storage.Asset) {
	body, err := f.cfg.Files.Open(r.Context(), a)
	if err != nil {
		if r.Context().Err() == nil {
			f.log.Warn("shared video unread", "asset", a.ID, "error", err)
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", a.ContentType)
	if a.Bytes > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(a.Bytes, 10))
	}
	if _, err := io.Copy(w, body); err != nil && r.Context().Err() == nil {
		f.log.Debug("shared video cut short", "asset", a.ID, "error", err)
	}
}

// sharedPicture is the picture a as a JPEG with a long side of at most
// side, its transparency on white, under Telegram's 5 MB.
func (f *Flow) sharedPicture(ctx context.Context, a storage.Asset, side int) ([]byte, error) {
	body, err := f.cfg.Files.Open(ctx, a)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, shareReadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > shareReadBytes {
		return nil, fmt.Errorf("a picture of over %d bytes", shareReadBytes)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("read the picture: %w", err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > media.DefaultMaxPixels {
		return nil, fmt.Errorf("a %d×%d picture", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("read the picture: %w", err)
	}
	return jpegOf(img, side)
}

// sharedCover is the cover of the video a, as the chat sends one: the
// first frame that is not one colour, as a thumbnail.
func (f *Flow) sharedCover(ctx context.Context, a storage.Asset) ([]byte, error) {
	dir, done, err := f.cfg.Work.Dir("share")
	if err != nil {
		return nil, err
	}
	defer done()
	body, err := f.cfg.Files.Open(ctx, a)
	if err != nil {
		return nil, err
	}
	err = writeFile(filepath.Join(dir, media.In), body)
	body.Close()
	if err != nil {
		return nil, err
	}
	poster, err := f.cfg.Clips.Poster(ctx, dir)
	if err != nil {
		return nil, err
	}
	pf, err := os.Open(poster)
	if err != nil {
		return nil, err
	}
	defer pf.Close()
	img, _, err := image.Decode(pf)
	if err != nil {
		return nil, fmt.Errorf("read the poster: %w", err)
	}
	return jpegOf(img, shareThumbSide)
}

func writeFile(path string, r io.Reader) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// jpegOf is img brought down to a long side of side, on white, as a JPEG
// under Telegram's 5 MB, at the best of a few qualities that fits.
func jpegOf(img image.Image, side int) ([]byte, error) {
	b := img.Bounds()
	w, h := fit(b.Dx(), b.Dy(), side)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, xdraw.Src)
	if w == b.Dx() && h == b.Dy() {
		xdraw.Draw(dst, dst.Bounds(), img, b.Min, xdraw.Over)
	} else {
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	}
	var out bytes.Buffer
	for _, q := range []int{88, 80, 70, 60} {
		out.Reset()
		if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: q}); err != nil {
			return nil, err
		}
		if out.Len() <= sharePhotoBytes {
			return out.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("a JPEG of %d bytes", out.Len())
}

// newShareKey is the key of the links of this process.
func newShareKey() (k [32]byte) {
	_, _ = rand.Read(k[:]) // it never fails since Go 1.24
	return k
}
