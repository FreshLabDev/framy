// SPDX-License-Identifier: Apache-2.0
package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"strconv"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/price"
)

// The most frames one press makes, as the composer offers them.
const (
	maxImageFrames = 6
	maxVideoFrames = 4
)

// batchRequest is the body of POST /api/batches: the frames of one press of
// Create (docs/design.md, Frames).
type batchRequest struct {
	// Prompt and Inputs are every frame's, unless a frame has its own.
	Prompt string  `json:"prompt"`
	Inputs []int64 `json:"inputs"`
	// Surface is "site" (the default) or "mini_app".
	Surface string `json:"surface"`
	// ShownUSD is the price of the whole press the person saw, as in POST
	// /api/jobs: the sum of the frames that are not free.
	ShownUSD *float64     `json:"shown_usd"`
	Frames   []batchFrame `json:"frames"`
}

// batchFrame is one result the press makes, with its own model, payer and
// settings. framy sets n itself, so a frame has none.
type batchFrame struct {
	Offer    string         `json:"offer"`
	Pay      string         `json:"pay"`
	Settings map[string]any `json:"settings"`
	// Prompt and Inputs, when present, replace the press's: a frame whose
	// model takes fewer files sends the ones it takes. "inputs": [] is none.
	Prompt    *string `json:"prompt"`
	Inputs    []int64 `json:"inputs"`
	Continues int64   `json:"continues"`
}

// batchJSON is the answer of POST /api/batches.
type batchJSON struct {
	Batch string    `json:"batch"`
	Jobs  []jobJSON `json:"jobs"`
}

// createBatch answers POST /api/batches: 202 with the jobs of the press at
// once. Frames that are the same run go as one job with n, as many as the
// offer makes at once, so five at four at once are two jobs, four and one.
// Every job is queued in one transaction, or none is.
func (a *API) createBatch(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	key := r.Header.Get("Idempotency-Key")
	if !idempotencyKey.MatchString(key) {
		badRequest("Idempotency-Key", "send an Idempotency-Key header of 1 to 128 letters, digits and . _ : -; a repeated request with the same key starts nothing").write(w)
		return
	}
	// "#" is not a character of a key, so no job of POST /api/jobs takes one
	// of these.
	dedupe := "web:" + strconv.FormatInt(user, 10) + ":" + key + "#"
	if id, found, err := a.data.ByKey(ctx, dedupe+"0"); err != nil {
		a.internal(w, r, err)
		return
	} else if found {
		a.batchAccepted(w, r, user, []int64{id}, false)
		return
	}

	req, aerr := decodeBatch(r)
	if aerr != nil {
		aerr.write(w)
		return
	}
	conns := &connList{}
	runs := make([]prepared, len(req.Frames))
	for i, f := range req.Frames {
		run, aerr, err := a.prepare(r, user, frameRun(req, f), conns)
		if err != nil {
			a.internal(w, r, err)
			return
		}
		if aerr != nil {
			aerr.with("frame", i).write(w)
			return
		}
		runs[i] = run
	}
	if aerr := checkFrames(runs); aerr != nil {
		aerr.write(w)
		return
	}

	lang := a.language(ctx, r, user)
	quote, err := a.quote(r, user, lang)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	batch, err := newBatchID()
	if err != nil {
		a.internal(w, r, err)
		return
	}
	keys, most := make([]string, len(runs)), make([]int, len(runs))
	for i, run := range runs {
		keys[i] = sameRun(run.n.Offer.ID, payOfRun(run.n), run.n.Settings, run.n.Prompt, run.n.Inputs, nil, req.Frames[i].Continues)
		most[i] = atOnce(run.n)
	}
	var (
		ns   []jobs.New
		paid []price.Estimate
	)
	for j, frames := range splitFrames(keys, most) {
		run := runs[frames[0]]
		n := run.n
		if len(frames) > 1 {
			n.Settings = maps.Clone(n.Settings)
			n.Settings["n"] = float64(len(frames))
			e := run.priceWith(n.Settings)
			n.Estimate = &e
		}
		n.DedupeKey, n.Batch = dedupe+strconv.Itoa(j), batch
		for _, f := range frames {
			n.BatchFrames = append(n.BatchFrames, int32(f))
		}
		if quote.Code != fx.USD {
			n.FX = &quote
		}
		if !n.Free {
			paid = append(paid, *n.Estimate)
		}
		ns = append(ns, n)
	}
	if total := price.Sum(paid...); len(paid) > 0 && req.ShownUSD != nil && dearer(total, *req.ShownUSD, quote, lang) {
		priceChanged(total, quote, lang).write(w)
		return
	}
	ids, created, err := a.cfg.Queue.EnqueueBatch(ctx, ns)
	if err != nil {
		a.refused(w, r, err)
		return
	}
	a.batchAccepted(w, r, user, ids, created)
}

// batchAccepted answers 202 with the jobs of the batch the first of ids is
// part of, as they are now.
func (a *API) batchAccepted(w http.ResponseWriter, r *http.Request, user int64, ids []int64, created bool) {
	if len(ids) == 0 {
		a.internal(w, r, errors.New("a batch with no jobs"))
		return
	}
	states, err := a.data.Batch(r.Context(), user, ids[0])
	switch {
	case err != nil:
		a.internal(w, r, err)
		return
	case len(states) == 0:
		// The jobs this key started were deleted since; the key starts no
		// others.
		newError(http.StatusNotFound, codeNotFound, "the jobs this request started were deleted").write(w)
		return
	}
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, http.StatusAccepted, batchJSON{
		Batch: states[0].Batch,
		Jobs:  a.renderAll(r.Context(), user, states, a.language(r.Context(), r, user)),
	})
}

// decodeBatch reads and checks the body of POST /api/batches.
func decodeBatch(r *http.Request) (batchRequest, *apiError) {
	var req batchRequest
	if e := readJSON(r, &req); e != nil {
		return req, e
	}
	switch {
	case len(req.Frames) == 0:
		return req, badRequest("frames", "frames are needed")
	case len(req.Frames) > maxImageFrames:
		return req, badRequest("frames", fmt.Sprintf("at most %d pictures or %d videos at once", maxImageFrames, maxVideoFrames))
	case req.ShownUSD != nil && (math.IsNaN(*req.ShownUSD) || *req.ShownUSD < 0):
		return req, badRequest("shown_usd", "shown_usd is the price shown, in dollars")
	}
	for i, f := range req.Frames {
		prefix := "frames[" + strconv.Itoa(i) + "]."
		if _, set := f.Settings["n"]; set {
			return req, badRequest(prefix+"settings", "framy sets n itself: send a frame for each result")
		}
		if e := checkCreate(prefix, frameRun(req, f)); e != nil {
			return req, e
		}
	}
	return req, nil
}

// frameRun is the run of one frame, as POST /api/jobs would take it.
func frameRun(req batchRequest, f batchFrame) createRequest {
	run := createRequest{
		Offer: f.Offer, Prompt: req.Prompt, Settings: f.Settings, Inputs: req.Inputs,
		Pay: f.Pay, Surface: req.Surface, Continues: f.Continues,
	}
	if f.Prompt != nil {
		run.Prompt = *f.Prompt
	}
	if f.Inputs != nil {
		run.Inputs = f.Inputs
	}
	return run
}

// checkFrames refuses a press of pictures and videos together, and more
// videos than a press makes.
func checkFrames(runs []prepared) *apiError {
	kind := runs[0].n.Offer.Kind
	for i, run := range runs {
		if run.n.Offer.Kind != kind {
			return badRequest("frames", "the frames of a press are all pictures or all videos").with("frame", i)
		}
	}
	if kind == catalog.Video && len(runs) > maxVideoFrames {
		return badRequest("frames", fmt.Sprintf("at most %d videos at once", maxVideoFrames))
	}
	return nil
}

// atOnce is how many frames of the same run one job of it makes: one on the
// free allowance, whose plan sets n, else what the offer makes at once.
func atOnce(n jobs.New) int {
	if n.Free {
		return 1
	}
	return catalog.AtOnce(n.Offer)
}

func payOfRun(n jobs.New) string {
	if n.Free {
		return payFree
	}
	return payKey
}

// sameRun is a key equal for frames that are the same run but for n: the
// offer, the payer, the settings, the prompt, the files and the video
// continued. files are what a quote knows of the files still in the
// browser.
func sameRun(offer, pay string, settings map[string]any, prompt string, inputs []int64, files []fileMeta, continues int64) string {
	s := make(map[string]any, len(settings))
	for k, v := range settings {
		if k != "n" {
			s[k] = v
		}
	}
	// Settings came from JSON or the catalog, so they always encode.
	b, _ := json.Marshal(struct {
		Offer     string         `json:"o"`
		Pay       string         `json:"p"`
		Settings  map[string]any `json:"s"`
		Prompt    string         `json:"t"`
		Inputs    []int64        `json:"i"`
		Files     []fileMeta     `json:"f"`
		Continues int64          `json:"c"`
	}{offer, pay, s, prompt, inputs, files, continues})
	return string(b)
}

// splitFrames puts the frames of a press into jobs. Frames with the same key
// are the same run: they go together, as many in one job as most of the
// first of them says, and the rest on to the next job of that run (five at
// four at once are four and one). Each job is its frames in order, and the
// jobs follow their first frames.
func splitFrames(keys []string, most []int) [][]int {
	var out [][]int
	filling := map[string]int{} // key -> the job of that run still taking frames
	for i, k := range keys {
		if j, ok := filling[k]; ok && len(out[j]) < most[out[j][0]] {
			out[j] = append(out[j], i)
			continue
		}
		filling[k] = len(out)
		out = append(out, []int{i})
	}
	return out
}

// newBatchID is a press nobody can guess: 16 random bytes.
func newBatchID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make a batch id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
