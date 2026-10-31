// SPDX-License-Identifier: Apache-2.0
package fx

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// Source is where a sync reads the rates: Client, or a fake in tests.
type Source interface {
	NBU(context.Context) (NBUDay, error)
	CurrencyAPI(context.Context) (USDDay, error)
	// NBUOn and CurrencyAPIOn read the rates of one day, for a day missed.
	NBUOn(context.Context, time.Time) (NBUDay, error)
	CurrencyAPIOn(context.Context, time.Time) (USDDay, error)
}

// Store keeps the rates.
type Store interface {
	// SaveRates writes rows; a currency-api row never replaces the NBU's for
	// the same day and code.
	SaveRates(context.Context, []Rate) error
	// RatesDate is the latest day with a row for every stored code, zero
	// before the first.
	RatesDate(context.Context) (time.Time, error)
	// RateDays are the days from from to to, both in, with a row for every
	// stored code, oldest first.
	RateDays(ctx context.Context, from, to time.Time) ([]time.Time, error)
}

// fillDays is how far back a missed day is filled. A price is quoted with
// the latest rate up to its day, so a hole left while framy was down would
// price that day, today included, with an older rate.
const fillDays = 31

// publishAt is when the NBU's rate for the next day is out: it sets it at
// 15:30 Kyiv, and the API has it a few minutes later.
const publishAt = 15*time.Hour + 35*time.Minute

// Want is the newest day of rates framy should have at now: today's, and
// from publishAt on, tomorrow's. The afternoon fetch means a new day starts
// with its rate already saved.
func Want(now time.Time) time.Time {
	day := Day(now)
	k := now.In(Kyiv)
	y, m, d := k.Date()
	if !k.Before(time.Date(y, m, d, 0, 0, 0, 0, Kyiv).Add(publishAt)) {
		day = day.AddDate(0, 0, 1)
	}
	return day
}

// next is when Want changes after now: publishAt today, or midnight.
func next(now time.Time) time.Time {
	k := now.In(Kyiv)
	y, m, d := k.Date()
	if at := time.Date(y, m, d, 0, 0, 0, 0, Kyiv).Add(publishAt); k.Before(at) {
		return at
	}
	return time.Date(y, m, d+1, 0, 0, 0, 0, Kyiv)
}

// Syncer keeps the rates up to Want. It asks the sources only when the
// saved rates are behind, so a restart costs no request.
type Syncer struct {
	Source Source
	Store  Store
	Log    *slog.Logger
	// Retry is the wait after a failure, 15 minutes when zero. A source
	// that answers with an older day than wanted is asked again in an hour.
	Retry time.Duration
	Now   func() time.Time
}

// Run syncs until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	for {
		if !sleep(ctx, s.Step(ctx)) {
			return
		}
	}
}

// Step brings the rates up to Want when they are behind, fills the days
// missed before them, and says how long to wait before the next step.
func (s *Syncer) Step(ctx context.Context) time.Duration {
	now := s.now()
	until := next(now).Sub(now)
	retry := s.Retry
	if retry <= 0 {
		retry = 15 * time.Minute
	}
	have, err := s.Store.RatesDate(ctx)
	if err != nil {
		s.Log.Warn("fx rates date unknown", "error", err)
		return min(retry, until)
	}
	need := Want(now)
	wait := until
	if have.Before(need) {
		got, err := s.SyncOnce(ctx)
		switch {
		case err != nil:
			wait = min(retry, until)
		case got.Before(need):
			// The NBU has not published the day yet, or only currency-api
			// answered and it is a day behind.
			s.Log.Info("fx rates behind the day wanted; asking again in an hour", "want", need.Format(time.DateOnly), "have", got.Format(time.DateOnly))
			wait = min(time.Hour, until)
		}
		if got.IsZero() {
			return wait
		}
		have = got
	}
	s.fill(ctx, Day(now), have)
	return wait
}

// fill reads by date the days missing before have: from the first day
// kept in the last fillDays, or from today when none was. A day that fails
// is asked again at the next step; once it is older than fillDays, never.
func (s *Syncer) fill(ctx context.Context, today, have time.Time) {
	from := today.AddDate(0, 0, -fillDays)
	if !from.Before(have) {
		return
	}
	kept, err := s.Store.RateDays(ctx, from, have)
	if err != nil {
		s.Log.Warn("fx days kept unknown; missed days not filled", "error", err)
		return
	}
	start := today
	if len(kept) > 0 && kept[0].Before(start) {
		start = kept[0]
	}
	for d := start; d.Before(have); d = d.AddDate(0, 0, 1) {
		if ctx.Err() != nil {
			return
		}
		if !slices.ContainsFunc(kept, d.Equal) {
			s.fillDay(ctx, d)
		}
	}
}

// fillDay saves one missed day the way SyncOnce saves the newest, BYN from
// currency-api's file of the same day.
func (s *Syncer) fillDay(ctx context.Context, d time.Time) {
	nbu, nbuErr := s.Source.NBUOn(ctx, d)
	usd, usdErr := s.Source.CurrencyAPIOn(ctx, d)
	var rates []Rate
	switch {
	case nbuErr == nil && usdErr == nil:
		rates = FromNBU(nbu, &usd)
	case nbuErr == nil:
		rates = FromNBU(nbu, nil)
	case usdErr == nil:
		rates = FromCurrencyAPI(usd)
	default:
		if ctx.Err() == nil {
			s.Log.Warn("fx missed day not filled", "day", d.Format(time.DateOnly), "error", errors.Join(nbuErr, usdErr))
		}
		return
	}
	if err := s.Store.SaveRates(ctx, rates); err != nil {
		s.Log.Warn("fx missed day not saved", "day", d.Format(time.DateOnly), "error", err)
		return
	}
	s.Log.Info("fx missed day filled", "day", d.Format(time.DateOnly), "source", rates[0].Source,
		"rows", len(rates), "complete", len(rates) == len(Stored))
}

// SyncOnce reads both sources and saves what they give, and returns the
// latest day saved for every code. The NBU failing is a warning, not an
// error: currency-api stands in. currency-api failing saves the NBU's rows
// and returns its error, as the day still lacks BYN. Both failing leave the
// saved rates as they were.
func (s *Syncer) SyncOnce(ctx context.Context) (time.Time, error) {
	nbu, nbuErr := s.Source.NBU(ctx)
	usd, usdErr := s.Source.CurrencyAPI(ctx)
	var rates []Rate
	// partial: the NBU's rows are saved, but the day has no BYN, and the
	// next step comes as after a failure.
	var partial error
	switch {
	case nbuErr == nil && usdErr == nil:
		rates = FromNBU(nbu, &usd)
	case nbuErr == nil:
		s.Log.Warn("currency-api failed; no BYN rate for the day", "day", nbu.Date.Format(time.DateOnly), "error", usdErr)
		rates, partial = FromNBU(nbu, nil), usdErr
	case usdErr == nil:
		metrics.FXFallbacks.Inc()
		s.Log.Warn("nbu failed; rates from currency-api", "day", usd.Date.Format(time.DateOnly), "error", nbuErr)
		rates = FromCurrencyAPI(usd)
	default:
		metrics.FXSyncFailures.Inc()
		err := errors.Join(nbuErr, usdErr)
		if ctx.Err() == nil {
			s.Log.Error("fx rates unavailable; the saved ones stay", "error", err)
		}
		return time.Time{}, err
	}
	if err := s.Store.SaveRates(ctx, rates); err != nil {
		metrics.FXSyncFailures.Inc()
		s.Log.Error("fx rates save failed", "error", err)
		return time.Time{}, err
	}
	metrics.FXSyncs.Inc()
	have, err := s.Store.RatesDate(ctx)
	if err != nil {
		return time.Time{}, err
	}
	s.Log.Info("fx rates saved", "day", rates[0].Date.Format(time.DateOnly), "source", rates[0].Source,
		"rows", len(rates), "complete", have.Format(time.DateOnly))
	return have, partial
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
