// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/price"
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
	// Estimate is the price shown before the run, in the currency it was
	// shown in; Cost is what the provider charged, once it is known.
	Estimate *estimateJSON `json:"estimate,omitempty"`
	Cost     *moneyJSON    `json:"cost,omitempty"`
	// Error says why the job failed, or why a queued one is waiting to go
	// again (a refusal before the run started, with RetryAt).
	Error      *errorJSON   `json:"error,omitempty"`
	RetryAt    *time.Time   `json:"retry_at,omitempty"`
	Results    []resultJSON `json:"results,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
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
	// URL is a presigned link, for the owner only, good for minutes. Absent
	// when the file is gone or framy keeps none.
	URL string `json:"url,omitempty"`
}

// render makes the client's view of a state. lang writes the amounts; a done
// job's results get their links here.
func (a *API) render(ctx context.Context, userID int64, s State, lang string) jobJSON {
	v := jobJSON{
		ID: s.ID, Seq: s.Seq, Status: string(s.Status), Kind: string(s.Kind), Offer: s.Offer, Free: s.Free,
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
		v.Results = make([]resultJSON, 0, len(s.Results))
		for _, r := range s.Results {
			res := resultJSON{ID: r.ID, ContentType: r.ContentType, Bytes: r.Bytes, Width: r.Width, Height: r.Height}
			if a.cfg.Links != nil {
				if link, err := a.cfg.Links.Link(ctx, userID, r.ID, ""); err == nil {
					res.URL = link
				} else {
					// Never the link or the key, which the error may name.
					a.log.Warn("result link not made", "job", s.ID, "asset", r.ID)
				}
			}
			v.Results = append(v.Results, res)
		}
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
	usd := *s.EstimateUSD
	maxUSD := usd
	kind := price.Exact
	if s.Estimate != nil {
		kind, maxUSD = s.Estimate.Kind, max(s.Estimate.MaxUSD, usd)
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
	if code != fx.USD && s.FXDate != nil {
		e.RateDate = s.FXDate.Format(time.DateOnly)
	}
	return e
}

// showable are the error classes whose message was written for a person to
// read: framy's own words, or the provider's reason. The rest (a Go error
// from an internal failure) says nothing to a person and may name a table or
// a path, so only its class goes out.
var showable = map[jobs.Class]bool{
	jobs.RateLimited: true, jobs.Unavailable: true, jobs.Money: true, jobs.Moderation: true,
	jobs.Refused: true, jobs.Key: true, jobs.Timeout: true, jobs.Interrupted: true,
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
