// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/price"
	"github.com/FreshLabDev/framy/internal/storage"
)

// jobJSON is a job as the client sees it: the answer of GET /api/jobs/{id},
// the data of every event, and the body of the 202 that starts one.
type jobJSON struct {
	ID int64 `json:"id"`
	// Seq is the id of the event that reports this state. It grows with every
	// change; two reads of an unchanged job give the same number.
	Seq    int64  `json:"seq"`
	Status string `json:"status"`
	Kind   string `json:"kind"`
	Offer  string `json:"offer"`
	Free   bool   `json:"free"`
	// Model, Prompt and Settings are what was asked for, for the result
	// page and Again; Surface is where: site, mini_app, group or dm.
	Model    string         `json:"model"`
	Prompt   string         `json:"prompt"`
	Settings map[string]any `json:"settings"`
	Surface  string         `json:"surface"`
	// Inputs are the files the job took that are still kept, in order, with
	// links like the results'.
	Inputs []resultJSON `json:"inputs"`
	// Estimate is the price shown before the run, in the currency it was
	// shown in; Cost is what the provider charged, once it is known.
	Estimate *estimateJSON `json:"estimate,omitempty"`
	Cost     *moneyJSON    `json:"cost,omitempty"`
	// Error says why the job failed, or why a queued one is waiting to go
	// again (a refusal before the run started, with RetryAt).
	Error   *errorJSON   `json:"error,omitempty"`
	RetryAt *time.Time   `json:"retry_at,omitempty"`
	Results []resultJSON `json:"results,omitempty"`
	// Joined is the clip the run went on from with the run's video after it,
	// one file, when Continue started the run from that clip's last frame.
	// The run's own video stays in Results.
	Joined *resultJSON `json:"joined,omitempty"`
	// Grounding comes with the results of a run grounded in Google Search.
	// Google's terms ask that Grounding.Suggestions, HTML Google wrote, be
	// shown beside the result, in a sandboxed frame (docs/design.md, Search
	// grounding).
	Grounding  *jobs.Grounding `json:"grounding,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// moneyJSON is an amount in the person's currency.
type moneyJSON struct {
	USD      float64 `json:"usd"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Label    string  `json:"label"`
}

// estimateJSON is a price with how far to trust it. Kind is one of exact,
// about, up_to, from and unknown, and the client puts the word of the kind
// before Label: "about $0.02", "up to $0.50", "from $0.04". Label is of the
// figure the kind speaks of: USD, or MaxUSD for up_to, and empty for
// unknown, whose price is known after the run. Amounts never round down.
type estimateJSON struct {
	Kind      string  `json:"kind"`
	USD       float64 `json:"usd"`
	MaxUSD    float64 `json:"max_usd"`
	Amount    float64 `json:"amount"`
	MaxAmount float64 `json:"max_amount"`
	Currency  string  `json:"currency"`
	Label     string  `json:"label"`
	// RateDate is the day of the rate; absent for USD.
	RateDate string `json:"rate_date,omitempty"`
	// Discount is the share OpenRouter takes off the listed price, 0.15 for
	// 15%, and the figures above are after it. ListUSD, ListAmount and
	// ListLabel are the listed price of the figure Label speaks of, for a
	// price shown struck through. All absent without a discount.
	Discount   float64 `json:"discount,omitempty"`
	ListUSD    float64 `json:"list_usd,omitempty"`
	ListAmount float64 `json:"list_amount,omitempty"`
	ListLabel  string  `json:"list_label,omitempty"`
}

type errorJSON struct {
	Class   string `json:"class"`
	Message string `json:"message,omitempty"`
}

type resultJSON struct {
	ID          int64  `json:"id"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	// Seconds is a clip's length; absent for a picture.
	Seconds float64 `json:"seconds,omitempty"`
	// Segments is where each part of a joined clip begins, in seconds from
	// its start, the first at 0: the clip it went on from, then each run
	// after it. The player marks them. Absent for any other file, and for a
	// clip joined before framy kept them.
	Segments []float64 `json:"segments,omitempty"`
	// URL is a presigned link, for the owner only. It is the same link for
	// an hour (docs/design.md, Storage and retention), lives 10 minutes past
	// that hour, and the browser may keep the file as long. Absent when the
	// file is gone or framy keeps none.
	URL string `json:"url,omitempty"`
	// Download is a link like URL whose answer saves the file, as
	// framy-<job>-<n>.<ext>, n counting the job's results from 1, or
	// framy-<job>-joined.mp4 for the joined clip. Results and the joined
	// clip only; absent where URL is.
	Download string `json:"download,omitempty"`
	// Thumb is a result's thumbnail for a grid: a WebP at most 480 px on the
	// long side, of a video its poster frame, linked as URL is. Absent until
	// it is made, which for a result kept before thumbnails is within a few
	// minutes, and for one framy could not read never.
	Thumb *thumbJSON `json:"thumb,omitempty"`
}

type thumbJSON struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// render makes the client's view of a state. lang writes the amounts; the
// files get their links here.
func (a *API) render(ctx context.Context, userID int64, s State, lang string) jobJSON {
	return a.renderAll(ctx, userID, []State{s}, lang)[0]
}

// renderAll renders a page of states, with the links of all their files
// made in one read.
func (a *API) renderAll(ctx context.Context, userID int64, states []State, lang string) []jobJSON {
	var ids []int64
	names := map[int64]string{}
	for _, s := range states {
		for _, f := range s.Inputs {
			ids = append(ids, f.ID)
		}
		if s.Status == jobs.Done {
			for i, f := range s.Results {
				ids = append(ids, f.ID)
				names[f.ID] = downloadName(s.ID, i+1, f.ContentType)
			}
			if f := s.Joined; f != nil {
				ids = append(ids, f.ID)
				names[f.ID] = joinedName(s.ID, f.ContentType)
			}
		}
	}
	var links map[int64]storage.FileLinks
	if a.cfg.Links != nil && len(ids) > 0 {
		var err error
		if links, err = a.cfg.Links.LinkAll(ctx, userID, ids, names); err != nil {
			// Never the link or the key, which the error may name.
			a.log.Warn("file links not made", "files", len(ids))
		}
	}
	out := make([]jobJSON, len(states))
	for i, s := range states {
		out[i] = a.renderOne(s, lang, links)
	}
	return out
}

// filesJSON lists files with their links; a download link only where
// download says, so an input that is also a result gets none as an input.
func filesJSON(files []Result, links map[int64]storage.FileLinks, download bool) []resultJSON {
	out := make([]resultJSON, 0, len(files))
	for _, r := range files {
		l := links[r.ID]
		v := resultJSON{ID: r.ID, ContentType: r.ContentType, Bytes: r.Bytes, Width: r.Width, Height: r.Height,
			Seconds: r.Seconds, Segments: r.Segments, URL: l.URL}
		if download {
			v.Download = l.Download
		}
		if t := l.Thumb; t != nil {
			v.Thumb = &thumbJSON{URL: t.URL, Width: t.Width, Height: t.Height}
		}
		out = append(out, v)
	}
	return out
}

// downloadName is what the n-th result of job saves as.
func downloadName(job int64, n int, contentType string) string {
	return "framy-" + strconv.FormatInt(job, 10) + "-" + strconv.Itoa(n) + storage.Ext(contentType)
}

// joinedName is what the joined clip of job saves as.
func joinedName(job int64, contentType string) string {
	return "framy-" + strconv.FormatInt(job, 10) + "-joined" + storage.Ext(contentType)
}

func (a *API) renderOne(s State, lang string, links map[int64]storage.FileLinks) jobJSON {
	settings := s.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	v := jobJSON{
		ID: s.ID, Seq: s.Seq, Status: string(s.Status), Kind: string(s.Kind), Offer: s.Offer, Free: s.Free,
		Model: s.Model, Prompt: s.Prompt, Settings: settings, Surface: s.Surface, Inputs: filesJSON(s.Inputs, links, false),
		CreatedAt: s.CreatedAt.UTC(), RetryAt: utc(s.RetryAt), FinishedAt: utc(s.FinishedAt),
	}
	code, perUSD := fx.USD, 1.0
	if s.Currency != "" && s.PerUSD > 0 {
		code, perUSD = s.Currency, s.PerUSD
	}
	if s.EstimateUSD != nil {
		v.Estimate = estimateOf(s, code, perUSD, lang)
	}
	if s.ActualUSD != nil {
		m := money(*s.ActualUSD, code, perUSD, lang)
		v.Cost = &m
	}
	if s.ErrorClass != "" {
		v.Error = &errorJSON{Class: string(s.ErrorClass), Message: publicMessage(s.ErrorClass, s.Error)}
	}
	if s.Status == jobs.Done {
		v.Results = filesJSON(s.Results, links, true)
		if s.Joined != nil {
			v.Joined = &filesJSON([]Result{*s.Joined}, links, true)[0]
		}
		v.Grounding = s.Grounding
	}
	return v
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func money(usd float64, code string, perUSD float64, lang string) moneyJSON {
	amount := usd * perUSD
	return moneyJSON{USD: usd, Amount: amount, Currency: code, Label: fx.Format(amount, code, lang)}
}

func estimateOf(s State, code string, perUSD float64, lang string) *estimateJSON {
	e := estimateJSONOf(*s.EstimateUSD, s.Estimate, code, perUSD, lang)
	if code != fx.USD && s.FXDate != nil {
		e.RateDate = s.FXDate.Format(time.DateOnly)
	}
	return e
}

// estimateJSONOf writes an estimate of usd, with what est says of it, in a
// currency. est may be nil: an exact price of usd.
func estimateJSONOf(usd float64, est *price.Estimate, code string, perUSD float64, lang string) *estimateJSON {
	maxUSD := usd
	kind := price.Exact
	if est != nil {
		kind, maxUSD = est.Kind, max(est.MaxUSD, usd)
	}
	e := &estimateJSON{
		Kind: string(kind), USD: usd, MaxUSD: maxUSD, Amount: usd * perUSD, MaxAmount: maxUSD * perUSD, Currency: code,
	}
	switch kind {
	case price.Unknown:
		// Nothing says what the run costs; a label would be a guess.
	case price.UpTo:
		e.Label = fx.Format(e.MaxAmount, code, lang)
	default:
		e.Label = fx.Format(e.Amount, code, lang)
	}
	if est != nil && est.Discount > 0 && kind != price.Unknown {
		e.Discount, e.ListUSD = est.Discount, est.ListUSD
		if kind == price.UpTo {
			e.ListUSD = est.ListMaxUSD
		}
		e.ListAmount = e.ListUSD * perUSD
		e.ListLabel = fx.Format(e.ListAmount, code, lang)
	}
	return e
}

// showable are the error classes whose message was written for a person to
// read: framy's own words, or the provider's reason. The rest (a Go error
// from an internal failure) says nothing to a person and may name a table or
// a path, so only its class goes out.
var showable = map[jobs.Class]bool{
	jobs.RateLimited: true, jobs.Unavailable: true, jobs.Money: true, jobs.Moderation: true,
	jobs.Refused: true, jobs.Faces: true, jobs.Sound: true, jobs.Invalid: true, jobs.Key: true, jobs.Timeout: true, jobs.Interrupted: true,
	jobs.Storage: true, jobs.NoAdapter: true, jobs.ProviderFailed: true,
}

// maxMessage is the longest error text sent, in characters.
const maxMessage = 300

var urlPattern = regexp.MustCompile(`https?://\S+`)

// publicMessage is the text of a job's error fit to show: none for a class
// that is not showable, no link (a provider may echo a presigned one, which
// opens a person's file), and short.
func publicMessage(class jobs.Class, msg string) string {
	if !showable[class] {
		return ""
	}
	msg = strings.TrimSpace(urlPattern.ReplaceAllString(msg, "<link>"))
	if utf8.RuneCountInString(msg) > maxMessage {
		msg = string([]rune(msg)[:maxMessage]) + "..."
	}
	return msg
}
