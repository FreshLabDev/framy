// SPDX-License-Identifier: Apache-2.0
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/media"
	"github.com/FreshLabDev/framy/internal/price"
)

// Limits of a quote.
const (
	// maxQuoteItems covers every offer of the catalog at once, which the
	// picker asks about when the settings change.
	maxQuoteItems = 200
	// maxSide is the largest picture side a file may claim, and maxSeconds
	// the longest video.
	maxSide    = 20000
	maxSeconds = 3600
)

// quoteRequest is the body of POST /api/quote: one or more runs to price at
// once, each with its own offer, payer and files.
type quoteRequest struct {
	Items []quoteItem `json:"items"`
}

// quoteItem is one run as the composer has it now.
type quoteItem struct {
	Offer string `json:"offer"`
	// Pay is "free", "key" or empty. With a payer the answer also says
	// whether POST /api/jobs would refuse the run now.
	Pay      string         `json:"pay"`
	Prompt   string         `json:"prompt"`
	Settings map[string]any `json:"settings"`
	// Inputs are ids of the person's files kept already: a result, or an
	// earlier upload. Files are those still in the browser, by what the
	// browser read of them.
	Inputs []int64    `json:"inputs"`
	Files  []fileMeta `json:"files"`
}

// fileMeta is what the browser reads of a file before Generate
// (docs/design.md, Attachments stay in the browser until Generate). The
// server checks the real file on Generate and prices the run again.
type fileMeta struct {
	ContentType string  `json:"content_type"`
	Bytes       int64   `json:"bytes"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	Seconds     float64 `json:"seconds"`
	FPS         float64 `json:"fps"`
}

// quoteJSON is the answer of POST /api/quote.
type quoteJSON struct {
	// Currency is the one the amounts are in, and RateDate the day of its
	// rate; absent for USD.
	Currency string          `json:"currency"`
	RateDate string          `json:"rate_date,omitempty"`
	Items    []quoteItemJSON `json:"items"`
}

// quoteItemJSON is the price of one run, in the order of the request.
type quoteItemJSON struct {
	Offer    string        `json:"offer"`
	Estimate *estimateJSON `json:"estimate,omitempty"`
	// Settings are the ones the run would go with: the request's, then on
	// the free allowance its locked values, and on a key framy's default of
	// every setting left out (catalog.Defaults), the shape following the
	// first file.
	Settings map[string]any `json:"settings,omitempty"`
	// Refusal is why POST /api/jobs would refuse the run now, in the shape
	// of its error answer with the status it would have: {"status": 429,
	// "error": "free_limit", "frees_at": …}. Absent when it would go, when
	// the request named no payer, and for nobody signed in.
	Refusal map[string]any `json:"refusal,omitempty"`
}

// quoteRoute is POST /api/quote: what runs would cost, for anyone. A person
// who is signed in also learns whether each would go: the free allowance
// left, a connection to the provider. The price is the same price.Of that
// POST /api/jobs keeps, in the currency it would be shown in.
func (a *API) quoteRoute(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req, aerr := decodeQuote(r)
	if aerr != nil {
		aerr.write(w)
		return
	}
	user, signedIn := a.cfg.Person(r)
	if !signedIn || user == 0 {
		user, signedIn = 0, false
	}
	models, byOffer, _, err := a.catalog.get(ctx, a.cfg.Store, a.now())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	lang := a.language(ctx, r, user)
	q, err := a.quote(r, user, lang)
	if err != nil {
		a.internal(w, r, err)
		return
	}

	// What only a person has: their files, and their connections.
	var (
		assets map[int64]InputAsset
		conns  []keys.Connection
	)
	if signedIn {
		var ids []int64
		needKeys := false
		for _, it := range req.Items {
			ids = append(ids, it.Inputs...)
			needKeys = needKeys || it.Pay == payKey
		}
		if len(ids) > 0 {
			found, err := a.data.Inputs(ctx, user, ids)
			if err != nil {
				a.internal(w, r, err)
				return
			}
			assets = make(map[int64]InputAsset, len(found))
			for _, in := range found {
				assets[in.ID] = in
			}
		}
		if needKeys {
			if conns, err = a.connections(r, user); err != nil {
				a.internal(w, r, err)
				return
			}
		}
	}

	out := quoteJSON{Currency: q.Code, Items: make([]quoteItemJSON, 0, len(req.Items))}
	if q.Code != fx.USD {
		out.RateDate = q.Date.Format(time.DateOnly)
	}
	for _, it := range req.Items {
		item := quoteItemJSON{Offer: it.Offer}
		m, offer, ok := offerIn(models, byOffer, it.Offer)
		if !ok {
			item.Refusal = refusalJSON(newError(http.StatusUnprocessableEntity, codeUnknownOffer, "no such offer, or it is not offered now"))
			out.Items = append(out.Items, item)
			continue
		}
		var inputs []InputAsset
		missing := false
		for _, id := range it.Inputs {
			in, ok := assets[id]
			if !ok {
				missing = true
				continue
			}
			inputs = append(inputs, in)
		}
		settings := map[string]any{}
		for k, v := range it.Settings {
			settings[k] = v
		}
		files := len(it.Inputs) + len(it.Files)
		var plan free.Plan
		isFree := false
		if it.Pay == payFree {
			plan, isFree = free.PlanOf(offer.ID)
			if isFree {
				fillFree(settings, plan, files)
			}
		}
		if !isFree {
			// The kept files come first, then those still in the browser.
			sizes := assetSizes(inputs)
			for _, f := range it.Files {
				sizes = append(sizes, [2]int{f.Width, f.Height})
			}
			fillDefaults(settings, offer, shapeOf(sizes...))
		}
		preq := priceRequest(m, it.Prompt, settings, inputs)
		addFiles(&preq, m, it.Files)
		est := price.Of(m, offer, preq)
		item.Estimate = estimateJSONOf(est.USD, &est, q.Code, q.PerUSD, lang)
		item.Estimate.RateDate = out.RateDate
		item.Settings = settings

		switch {
		case !signedIn || it.Pay == "":
		case missing:
			item.Refusal = refusalJSON(newError(http.StatusUnprocessableEntity, codeUnknownInput, "an input is not one of your files"))
		case it.Pay == payKey:
			if e := checkConnection(conns, offer.Provider); e != nil {
				item.Refusal = refusalJSON(e)
			}
		case !isFree:
			item.Refusal = refusalJSON(a.refusal(&free.NotFreeError{Reason: offer.ID + " is not a model of the free allowance"}))
		case a.cfg.Allowance != nil:
			err := a.cfg.Allowance.Check(ctx, jobs.Admission{
				UserID: user, Offer: offer.ID, Kind: offer.Kind, Settings: settings, Inputs: files, EstimateUSD: est.USD,
			})
			if err != nil {
				e := a.refusal(err)
				if e == nil {
					a.internal(w, r, err)
					return
				}
				item.Refusal = refusalJSON(e)
			}
		}
		out.Items = append(out.Items, item)
	}
	writeJSON(w, http.StatusOK, out)
}

// refusalJSON is an error answer as a field of another answer.
func refusalJSON(e *apiError) map[string]any {
	out := map[string]any{"status": e.status, "error": e.code, "message": e.message}
	for k, v := range e.extra {
		out[k] = v
	}
	return out
}

// addFiles prices the files still in the browser as the upload pipeline
// will make them: a photo is brought down to the long side it keeps.
func addFiles(req *price.Request, m catalog.Model, files []fileMeta) {
	frames := m.Kind == catalog.Video && (m.Traits.References == nil || m.Traits.References.Image == 0)
	for _, f := range files {
		switch {
		case strings.HasPrefix(f.ContentType, "image/"):
			if frames {
				req.Frames++
				continue
			}
			w, h := fitLongSide(f.Width, f.Height, media.DefaultLongSide)
			req.Images = append(req.Images, price.Image{Width: w, Height: h})
		case strings.HasPrefix(f.ContentType, "video/"):
			if req.Video == nil {
				req.Video = &price.Video{Seconds: f.Seconds, Width: f.Width, Height: f.Height}
			}
		}
	}
}

// fitLongSide scales a picture down so its long side is at most side, as
// internal/media does; a smaller one stays.
func fitLongSide(w, h, side int) (int, int) {
	long := max(w, h)
	if long <= side || long == 0 {
		return w, h
	}
	k := float64(side) / float64(long)
	return max(1, int(math.Round(float64(w)*k))), max(1, int(math.Round(float64(h)*k)))
}

// decodeQuote reads and checks the body of a quote.
func decodeQuote(r *http.Request) (quoteRequest, *apiError) {
	var req quoteRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return req, newError(http.StatusRequestEntityTooLarge, codeTooLarge, "the request is too big")
		}
		return req, badRequest("", "the body must be a JSON object: "+jsonProblem(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return req, badRequest("", "the body must be one JSON object")
	}
	switch {
	case len(req.Items) == 0:
		return req, badRequest("items", "items lists the runs to price")
	case len(req.Items) > maxQuoteItems:
		return req, badRequest("items", fmt.Sprintf("at most %d items", maxQuoteItems))
	}
	for i, it := range req.Items {
		prefix := fmt.Sprintf("items[%d].", i)
		switch {
		case it.Offer == "":
			return req, badRequest(prefix+"offer", "offer is needed")
		case it.Pay != "" && it.Pay != payFree && it.Pay != payKey:
			return req, badRequest(prefix+"pay", `pay is "free", "key" or absent`)
		case len(it.Inputs)+len(it.Files) > maxInputs:
			return req, badRequest(prefix+"files", fmt.Sprintf("at most %d input files", maxInputs))
		}
		if e := checkRun(prefix, it.Prompt, it.Settings, it.Inputs); e != nil {
			return req, e
		}
		for j, f := range it.Files {
			field := fmt.Sprintf("%sfiles[%d]", prefix, j)
			image, video := strings.HasPrefix(f.ContentType, "image/"), strings.HasPrefix(f.ContentType, "video/")
			switch {
			case !image && !video:
				return req, badRequest(field+".content_type", "a file is an image/… or a video/…")
			case f.Width < 0 || f.Height < 0 || f.Width > maxSide || f.Height > maxSide:
				return req, badRequest(field, fmt.Sprintf("a picture side is 0 to %d px", maxSide))
			case f.Bytes < 0 || badNumber(f.Seconds) || badNumber(f.FPS) || f.Seconds > maxSeconds:
				return req, badRequest(field, "bytes, seconds and fps are numbers of the file")
			}
		}
	}
	return req, nil
}

func badNumber(f float64) bool { return math.IsNaN(f) || math.IsInf(f, 0) || f < 0 }
