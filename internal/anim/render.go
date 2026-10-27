// SPDX-License-Identifier: Apache-2.0

package anim

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// loopVersion names the design of the generating loop and the error card.
// Change it with any change to how they look: the files cached under the old
// number are not used again, and the bot, which keeps a Telegram file_id per
// Key, sends the new picture once.
var loopVersion = 3

// Telegram's limits for what this package makes (docs/research/telegram.md,
// 3.2). An animation is H.264 in an MP4 with no sound track, up to 50 MB; a
// photo is up to 10 MB, its width and height add up to 10000 at most, and
// neither side is 20 times the other. Every picture here is far inside them.
// The tests hold the files to the numbers below, which are tighter.
const (
	// MaxAnimationBytes is the budget of a loop. An album of ten photos with
	// texture in them is 350 KB; a tenfold change in the encoding is a bug.
	MaxAnimationBytes = 1 << 20
	// MaxCardBytes is the budget of a card.
	MaxCardBytes = 256 << 10
)

var errNoCache = errors.New("anim: Renderer.Cache is not set")

// Media is a finished picture, ready to go to Telegram: an MP4 for
// sendAnimation, or a PNG for sendPhoto and InputMediaPhoto.
type Media struct {
	Path        string
	ContentType string        // "video/mp4" or "image/png"
	Width       int           // pixels
	Height      int           // pixels
	Duration    time.Duration // of one loop; 0 for a picture
	Bytes       int64
	// Key says which picture it is: it changes when the design changes. The
	// bot keeps the file_id Telegram gave the upload under it. The album
	// preview, made from a person's photos, has none.
	Key string
	// TextDrawn is for a card: whether its line is on it. It is not for the
	// languages whose letters the faces lack.
	TextDrawn bool
}

// Renderer makes the pictures. One renders at a time, since drawing a loop
// already uses every core; the rest wait their turn or their context.
type Renderer struct {
	// FFmpeg is the binary; "" is ffmpeg on PATH. It must have libx264.
	FFmpeg string
	// Cache is the directory the generating loop and the cards are kept in.
	// It is made when needed. Nothing in it is a person's.
	Cache string
	// Workers is how many frames are drawn at once; 0 is one per CPU, at
	// most 8.
	Workers int
	// Timeout bounds one loop's encoding; 0 is a minute.
	Timeout time.Duration

	once sync.Once
	slot chan struct{}
}

func (r *Renderer) options() Options {
	return Options{FFmpeg: r.FFmpeg, Workers: r.Workers, Timeout: r.Timeout}
}

// Check tells whether the ffmpeg can encode the loops: H.264 through libx264.
func (r *Renderer) Check(ctx context.Context) error {
	o := r.options().withDefaults()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.FFmpeg, "-hide_banner", "-encoders")
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%s -encoders: %w", o.FFmpeg, err)
	}
	if !strings.Contains(string(out), " libx264 ") {
		return fmt.Errorf("%s has no libx264, which the loops are encoded with", o.FFmpeg)
	}
	return nil
}

// acquire waits for the renderer's turn.
func (r *Renderer) acquire(ctx context.Context) (release func(), err error) {
	r.once.Do(func() { r.slot = make(chan struct{}, 1) })
	select {
	case r.slot <- struct{}{}:
		return func() { <-r.slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Album draws the preview of an album, the photos at paths in the order they
// are numbered, and writes it as album.mp4 in dir, a directory of the
// caller's, who removes it with the rest of the job's working files. paths
// are JPEG or PNG files, as internal/media writes them; one to ten.
func (r *Renderer) Album(ctx context.Context, dir string, paths []string) (Media, error) {
	release, err := r.acquire(ctx)
	if err != nil {
		return Media{}, err
	}
	defer release()
	a, err := NewAlbum(ctx, paths)
	if err != nil {
		return Media{}, err
	}
	return r.encode(ctx, a, filepath.Join(dir, "album.mp4"), "")
}

// generatingKey names the loop of one design and one encoding.
func generatingKey(o Options) string {
	o = o.withDefaults()
	return digest("generating", loopVersion, Field, Width, Height, FPS, generatingDur, o.CRF, o.Preset)
}

// GeneratingKey is the key Generating gives its picture, without drawing it.
func (r *Renderer) GeneratingKey() string { return generatingKey(r.options()) }

// Generating returns the loop shown while a job runs. It is drawn the first
// time, and after a change of design, and kept in the cache directory; every
// other call returns the file. Telegram wants it uploaded once: the bot sends
// the file, keeps the file_id under Media.Key, and sends the id from then on.
func (r *Renderer) Generating(ctx context.Context) (Media, error) {
	if r.Cache == "" {
		return Media{}, errNoCache
	}
	key := r.GeneratingKey()
	path := filepath.Join(r.Cache, "generating-"+key+".mp4")
	if m, ok := cached(path, Generating{}, key); ok {
		return m, nil
	}
	release, err := r.acquire(ctx)
	if err != nil {
		return Media{}, err
	}
	defer release()
	// A call that waited may find the loop drawn by the one before it.
	if m, ok := cached(path, Generating{}, key); ok {
		return m, nil
	}
	if err := os.MkdirAll(r.Cache, 0o755); err != nil {
		return Media{}, err
	}
	m, err := r.encode(ctx, Generating{}, path, key)
	if err != nil {
		return Media{}, err
	}
	// The loops of older designs are of no use.
	old, _ := filepath.Glob(filepath.Join(r.Cache, "generating-*.mp4"))
	for _, f := range old {
		if f != path {
			_ = os.Remove(f)
		}
	}
	return m, nil
}

// ErrorCard returns the card for a failed job: text, the bot's line in the
// language lang, under the viewfinder with a cross. The card is a PNG kept in
// the cache directory under its key, so the same line is drawn once. The bot
// sends it and does not delete it.
func (r *Renderer) ErrorCard(ctx context.Context, text, lang string) (Media, error) {
	card := NewCard(text, lang)
	text = strings.Join(strings.Fields(text), " ")
	m, err := r.picture(ctx, card, "error", digest("error", loopVersion, Field, Width, Height, text, lang))
	m.TextDrawn = card.TextDrawn()
	return m, err
}

// blank is the generating loop held at its first frame.
type blank struct{}

func (blank) Size() (int, int)          { return Width, Height }
func (blank) Duration() float64         { return 0 }
func (blank) Draw(c *Canvas, _ float64) { Generating{}.Draw(c, 0) }

// Blank returns the preview of a run that takes no photo: the generating
// loop's first frame as a PNG, so that Generate sets the same dots moving.
func (r *Renderer) Blank(ctx context.Context) (Media, error) {
	return r.picture(ctx, blank{}, "blank", digest("blank", loopVersion, Field, Width, Height))
}

// picture writes the still of s as a PNG in the cache under key, once.
func (r *Renderer) picture(ctx context.Context, s Scene, name, key string) (Media, error) {
	if r.Cache == "" {
		return Media{}, errNoCache
	}
	path := filepath.Join(r.Cache, name+"-"+key+".png")
	if m, ok := cached(path, s, key); ok {
		return m, nil
	}
	if err := ctx.Err(); err != nil {
		return Media{}, err
	}
	if err := os.MkdirAll(r.Cache, 0o755); err != nil {
		return Media{}, err
	}
	part := path + ".part"
	defer os.Remove(part)
	f, err := os.Create(part)
	if err != nil {
		return Media{}, err
	}
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	err = enc.Encode(f, Still(s, 0))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Media{}, err
	}
	if err := os.Rename(part, path); err != nil {
		return Media{}, err
	}
	m, _ := cached(path, s, key)
	return m, nil
}

// encode draws the loop to path and describes it.
func (r *Renderer) encode(ctx context.Context, s Scene, path, key string) (Media, error) {
	if _, err := Encode(ctx, s, path, r.options()); err != nil {
		return Media{}, err
	}
	m, ok := cached(path, s, key)
	if !ok {
		return Media{}, errors.New("anim: ffmpeg wrote nothing")
	}
	return m, nil
}

// cached describes the file at path as the picture of scene s, if it is
// there and not empty.
func cached(path string, s Scene, key string) (Media, bool) {
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		return Media{}, false
	}
	w, h := s.Size()
	m := Media{Path: path, ContentType: "image/png", Width: w, Height: h, Bytes: st.Size(), Key: key}
	if s.Duration() > 0 {
		m.ContentType = "video/mp4"
		m.Duration = time.Duration(float64(Frames(s)) / FPS * float64(time.Second))
	}
	return m, true
}

// digest is a short name for the things a picture is made of.
func digest(parts ...any) string {
	var b bytes.Buffer
	for _, p := range parts {
		fmt.Fprintf(&b, "%v\x00", p)
	}
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:8])
}
