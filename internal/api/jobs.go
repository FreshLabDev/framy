// SPDX-License-Identifier: Apache-2.0
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/web"
)

// Limits on what a request may carry. They keep a request from being a way
// to fill the database; the providers have limits of their own, and the
// adapters refuse what a provider would, before anything is paid.
const (
	maxPrompt   = 32000 // characters
	maxInputs   = 16
	maxSettings = 32
	maxSetting  = 1000 // bytes of a setting's name or text value
	// maxListSetting is the most values a list setting may hold.
	maxListSetting = 8
)

// Payers of a run: the free allowance, or the person's own connection.
const (
	payFree = "free"
	payKey  = "key"
)

// idempotencyKey is what a client may send as Idempotency-Key: a UUID, a
// random string or a counter, in the characters of those.
var idempotencyKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// createRequest is the body of POST /api/jobs.
type createRequest struct {
	// Offer is the id of an offer in the catalog: "openrouter:<model>".
	Offer  string `json:"offer"`
	Prompt string `json:"prompt"`
	// Settings carries a value for every control the composer shows. A run
	// on the free allowance gets the allowance's own settings added.
	Settings map[string]any `json:"settings"`
	// Inputs are ids of the person's files (POST /api/uploads, or an earlier
	// result), in the order the run takes them.
	Inputs []int64 `json:"inputs"`
	// Pay is "free" for the free allowance, or "key" for the person's own
	// connection to the offer's provider. It has no default: the person sees
	// which before the run starts, and so does the request.
	Pay string `json:"pay"`
	// Surface is "site" (the default) or "mini_app".
	Surface string `json:"surface"`
	// ShownUSD is the price the person saw before pressing Generate: the
	// figure of the quote's label, usd, or max_usd for "up to". A run on
	// their own key whose price would now show higher is refused with 409
	// price_changed and the new estimate, and waits for another press. A
	// lower price runs as it is. Absent: no check.
	ShownUSD *float64 `json:"shown_usd"`
}

// create answers POST /api/jobs: 202 with the job at once. The run happens
// later, in a worker, and the client follows it.
func (a *API) create(w http.ResponseWriter, r *http.Request) {
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
	// The key is the person's own: two people who send the same one start
	// two jobs.
	dedupe := "web:" + strconv.FormatInt(user, 10) + ":" + key

	// A request that started a job already is answered with that job, before
	// anything else is checked: what it was (an offer, a connection) may have
	// changed since.
	if id, found, err := a.data.ByKey(ctx, dedupe); err != nil {
		a.internal(w, r, err)
		return
	} else if found {
		a.accepted(w, r, user, id, false)
		return
	}

	req, aerr := decode(r)
	if aerr != nil {
		aerr.write(w)
		return
	}
	m, offer, err := a.cfg.Store.Offer(ctx, req.Offer)
	if errors.Is(err, db.ErrNoOffer) {
		newError(http.StatusUnprocessableEntity, codeUnknownOffer, "no such offer, or it is not offered now").write(w)
		return
	}
	if err != nil {
		a.internal(w, r, err)
		return
	}
	settings := req.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	isFree := req.Pay == payFree
	if isFree {
		plan, ok := free.PlanOf(offer.ID)
		if !ok {
			a.refused(w, r, &free.NotFreeError{Reason: offer.ID + " is not a model of the free allowance"})
			return
		}
		// The allowance's settings are not the client's to know: a run that
		// leaves one out gets it, and one that sets it to something else is
		// refused by the allowance. A video from a start frame says so.
		fillFree(settings, plan, len(req.Inputs))
	} else if conns, err := a.connections(r, user); err != nil {
		a.internal(w, r, err)
		return
	} else if aerr := checkConnection(conns, offer.Provider); aerr != nil {
		aerr.write(w)
		return
	}
	inputs, err := a.data.Inputs(ctx, user, req.Inputs)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	if len(inputs) != len(req.Inputs) {
		newError(http.StatusUnprocessableEntity, codeUnknownInput, "an input is not one of your files").write(w)
		return
	}
	if !isFree {
		fillDefaults(settings, offer, shapeOf(assetSizes(inputs)...))
	}

	estimate := price.Of(m, offer, priceRequest(m, req.Prompt, settings, inputs))
	lang := a.language(ctx, r, user)
	quote, err := a.quote(r, user, lang)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	if !isFree && req.ShownUSD != nil && dearer(estimate, *req.ShownUSD, quote, lang) {
		e := estimateJSONOf(estimate.USD, &estimate, quote.Code, quote.PerUSD, lang)
		if quote.Code != fx.USD {
			e.RateDate = quote.Date.Format(time.DateOnly)
		}
		newError(http.StatusConflict, codePriceChanged, "the price is higher than the one shown; show the new one and ask again").
			with("estimate", e).write(w)
		return
	}
	n := jobs.New{
		UserID: user, Surface: jobs.Site, DedupeKey: dedupe, Offer: offer, Prompt: req.Prompt,
		Settings: settings, Free: isFree, Estimate: &estimate, Inputs: req.Inputs,
	}
	if req.Surface == string(jobs.MiniApp) {
		n.Surface = jobs.MiniApp
	}
	// A price shown in dollars alone keeps no rate.
	if quote.Code != fx.USD {
		n.FX = &quote
	}
	id, created, err := a.cfg.Queue.Enqueue(ctx, n)
	if err != nil {
		a.refused(w, r, err)
		return
	}
	a.accepted(w, r, user, id, created)
}

// accepted answers 202 with the job as it is now.
func (a *API) accepted(w http.ResponseWriter, r *http.Request, user, id int64, created bool) {
	st, err := a.data.State(r.Context(), user, id)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/jobs/"+strconv.FormatInt(id, 10))
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, http.StatusAccepted, a.render(r.Context(), user, st, a.language(r.Context(), r, user)))
}

// refused answers a job the queue would not make.
func (a *API) refused(w http.ResponseWriter, r *http.Request, err error) {
	if e := a.refusal(err); e != nil {
		e.write(w)
		return
	}
	a.internal(w, r, err)
}

// refusal is the answer to a refusal of the queue or the allowance, or nil
// for an error that is not one.
func (a *API) refusal(err error) *apiError {
	var (
		limit  *free.LimitError
		closed *free.ClosedError
		notFr  *free.NotFreeError
	)
	switch {
	case errors.As(err, &limit):
		e := newError(http.StatusTooManyRequests, codeFreeLimit, limit.Error())
		e.retryAfter = limit.FreesAt.Sub(a.now())
		return e.with("kind", string(limit.Kind)).with("limit", limit.Limit).
			with("window_seconds", int64(limit.Window.Seconds())).with("frees_at", limit.FreesAt.UTC())
	case errors.As(err, &closed):
		e := newError(http.StatusServiceUnavailable, codeFreeClosed, "the free tier is closed for now")
		if !closed.Until.IsZero() {
			e.retryAfter = closed.Until.Sub(a.now())
			e.with("until", closed.Until.UTC())
		}
		return e
	case errors.As(err, &notFr):
		return newError(http.StatusUnprocessableEntity, codeNotFree, notFr.Error()).with("reason", notFr.Reason)
	case errors.Is(err, jobs.ErrNoInput):
		return newError(http.StatusUnprocessableEntity, codeUnknownInput, "an input is not one of your files")
	}
	return nil
}

// fillFree puts the allowance's settings for a run that takes the given
// number of files into settings, where the run left them out.
func fillFree(settings map[string]any, plan free.Plan, inputs int) {
	for name, v := range plan.SettingsFor(inputs) {
		if cur, set := settings[name]; !set || cur == nil {
			settings[name] = v
		}
	}
}

// fillDefaults gives a run on the person's key framy's value of every
// setting it leaves out (catalog.Defaults), so the run is what was priced.
// A free run takes its plan's settings and nothing else.
func fillDefaults(settings map[string]any, offer catalog.Offer, shape float64) {
	for name, v := range catalog.Defaults(offer, shape) {
		if cur, set := settings[name]; !set || cur == nil {
			settings[name] = v
		}
	}
}

// shapeOf is the width over the height of the first file that has both,
// 0 when none does.
func shapeOf(sizes ...[2]int) float64 {
	for _, s := range sizes {
		if s[0] > 0 && s[1] > 0 {
			return float64(s[0]) / float64(s[1])
		}
	}
	return 0
}

func assetSizes(inputs []InputAsset) [][2]int {
	out := make([][2]int, len(inputs))
	for i, in := range inputs {
		out[i] = [2]int{in.Width, in.Height}
	}
	return out
}

// dearer reports whether the estimate would show a higher price than the one
// the person saw, in the currency it is shown in: a change too small to
// change the label is no change. An estimate of unknown kind has no figure
// to compare.
func dearer(e price.Estimate, shownUSD float64, q fx.Quote, lang string) bool {
	if e.Kind == price.Unknown || math.IsNaN(shownUSD) || shownUSD < 0 {
		return false
	}
	figure := e.USD
	if e.Kind == price.UpTo {
		figure = e.MaxUSD
	}
	if figure <= shownUSD {
		return false
	}
	return fx.Format(q.Convert(figure), q.Code, lang) != fx.Format(q.Convert(shownUSD), q.Code, lang)
}

// connections lists the person's connections; none without a KEK.
func (a *API) connections(r *http.Request, user int64) ([]keys.Connection, error) {
	if a.cfg.Keys == nil {
		return nil, nil
	}
	conns, err := a.cfg.Keys.List(r.Context(), user)
	if errors.Is(err, keys.ErrDisabled) {
		return nil, nil
	}
	return conns, err
}

// checkConnection says why a run on the person's own key cannot start: they
// have no connection to the provider, or the provider refused the key. The
// queue's adapter would fail the job as key later; this refuses it before it
// exists. A key that is merely short of billing, verification or credit
// still goes: that is how its status gets fixed.
func checkConnection(conns []keys.Connection, provider string) *apiError {
	for _, c := range conns {
		if c.Provider != provider {
			continue
		}
		if c.Status == keys.Invalid {
			return newError(http.StatusPaymentRequired, codeConnectionInvalid, "the key for this provider was refused by it; replace it").with("provider", provider)
		}
		return nil
	}
	return newError(http.StatusPaymentRequired, codeNoConnection, "connect a key for this provider, or run on the free allowance").with("provider", provider)
}

// quote picks the currency of the price and the rate to show it with. The
// person's own choice comes first, then the country Cloudflare saw, but only
// when the request came through the trusted proxy: anyone else could send
// the header, then, and pick a currency for another person's price. Then
// the language, then USD (fx.Default). A currency with no rate saved yet
// (a first start) is shown in USD rather than not at all. user 0 is nobody
// signed in, who has no choice of their own.
func (a *API) quote(r *http.Request, user int64, lang string) (fx.Quote, error) {
	ctx := r.Context()
	var chosen string
	if user != 0 {
		var err error
		if chosen, err = a.cfg.Store.Currency(ctx, user); err != nil {
			return fx.Quote{}, err
		}
	}
	country := ""
	if web.FromProxy(r) {
		country = r.Header.Get("CF-IPCountry")
	}
	code := fx.Default(chosen, country, lang)
	q, err := a.cfg.Store.Quote(ctx, code, fx.Day(a.now()))
	if errors.Is(err, fx.ErrNoRate) {
		a.log.Warn("no rate saved; the price is shown in USD", "currency", code)
		return fx.Dollar, nil
	}
	return q, err
}

// priceRequest is the estimate's input. Pictures are references; to a video
// model that takes no image references, a picture is the first frame.
func priceRequest(m catalog.Model, prompt string, settings map[string]any, inputs []InputAsset) price.Request {
	req := price.Request{Settings: settings, Prompt: prompt}
	frames := m.Kind == catalog.Video && (m.Traits.References == nil || m.Traits.References.Image == 0)
	for _, in := range inputs {
		if !strings.HasPrefix(in.ContentType, "image/") {
			continue
		}
		if frames {
			req.Frames++
		} else {
			req.Images = append(req.Images, price.Image{Width: in.Width, Height: in.Height})
		}
	}
	return req
}

// decode reads and checks the body of a create request.
func decode(r *http.Request) (createRequest, *apiError) {
	var req createRequest
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
	case req.Offer == "":
		return req, badRequest("offer", "offer is needed")
	case req.Pay != payFree && req.Pay != payKey:
		return req, badRequest("pay", `pay must be "free" or "key"`)
	case req.Surface != "" && req.Surface != string(jobs.Site) && req.Surface != string(jobs.MiniApp):
		return req, badRequest("surface", `surface must be "site" or "mini_app"`)
	case req.ShownUSD != nil && (math.IsNaN(*req.ShownUSD) || *req.ShownUSD < 0):
		return req, badRequest("shown_usd", "shown_usd is the price shown, in dollars")
	}
	if e := checkRun("", req.Prompt, req.Settings, req.Inputs); e != nil {
		return req, e
	}
	if strings.TrimSpace(req.Prompt) == "" && len(req.Inputs) == 0 {
		return req, badRequest("prompt", "send a prompt or an input file")
	}
	return req, nil
}

// checkRun checks the prompt, the settings and the inputs of a run, for a
// job or a quote; prefix goes before the field a refusal names.
func checkRun(prefix, prompt string, settings map[string]any, inputs []int64) *apiError {
	switch {
	case !utf8.ValidString(prompt) || strings.ContainsRune(prompt, 0):
		return badRequest(prefix+"prompt", "prompt must be text")
	case utf8.RuneCountInString(prompt) > maxPrompt:
		return badRequest(prefix+"prompt", fmt.Sprintf("prompt is over %d characters", maxPrompt))
	case len(inputs) > maxInputs:
		return badRequest(prefix+"inputs", fmt.Sprintf("at most %d input files", maxInputs))
	case len(settings) > maxSettings:
		return badRequest(prefix+"settings", fmt.Sprintf("at most %d settings", maxSettings))
	}
	for _, id := range inputs {
		if id <= 0 {
			return badRequest(prefix+"inputs", "inputs are ids of your files")
		}
	}
	for name, v := range settings {
		if name == "" || len(name) > maxSetting || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
			return badRequest(prefix+"settings", "a setting has no usable name")
		}
		switch v := v.(type) {
		case nil, bool, float64:
		case string:
			if len(v) > maxSetting || !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
				return badRequest(prefix+"settings."+name, "a setting's text is too long or not text")
			}
		case []any:
			// A list setting, such as frame_images: a few short words.
			if len(v) > maxListSetting {
				return badRequest(prefix+"settings."+name, "a list setting is too long")
			}
			for _, x := range v {
				if t, ok := x.(string); !ok || len(t) > maxSetting || !utf8.ValidString(t) || strings.ContainsRune(t, 0) {
					return badRequest(prefix+"settings."+name, "a list setting holds short texts")
				}
			}
		default:
			return badRequest(prefix+"settings."+name, "a setting is a string, a number, a boolean, a list of texts or null")
		}
	}
	return nil
}

// jsonProblem names what is wrong with a body without echoing it.
func jsonProblem(err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF):
		return "it is empty"
	case errors.As(err, &syn), errors.Is(err, io.ErrUnexpectedEOF):
		return "it is not valid JSON"
	case errors.As(err, &typ):
		return "field " + typ.Field + " has the wrong type"
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return strings.TrimPrefix(err.Error(), "json: ")
	}
	return "it could not be read"
}
