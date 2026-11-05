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
	// NBUOn and CurrencyAPIOn read the rates of one day: a day missed, and
	// BYN of every day, which is read by date only.
	NBUOn(context.Context, time.Time) (NBUDay, error)
	CurrencyAPIOn(context.Context, time.Time) (USDDay, error)
}

// Store keeps the rates.
type Store interface {
	// SaveRates writes rows; a currency-api row never replaces the NBU's for
	// the same day and code.
	SaveRates(context.Context, []Rate) error
	// RatesDate is the latest day with a row of every code in codes, zero
	// before the first.
	RatesDate(ctx context.Context, codes []string) (time.Time, error)
	// RateDays are the days from from to to, both in, with a row of every
	// code in codes, oldest first.
	RateDays(ctx context.Context, codes []string, from, to time.Time) ([]time.Time, error)
}

// fillDays is how far back a missed day is filled. A price is quoted with
// the latest rate up to its day, so a hole left while framy was down would
// price that day, today included, with an older rate.
const fillDays = 31

// publishAt is when the NBU's rate for the next day is out: it sets it at
// 15:30 Kyiv, and the API has it a few minutes later.
const publishAt = 15*time.Hour + 35*time.Minute

// currencyAPIOut is how long after a day begins in UTC currency-api's file
// of it can first be out. The job that makes the file is set for midnight
// UTC, and GitHub often starts it hours late: from 05:00 to 06:00 in early
// November 2026.
const currencyAPIOut = 30 * time.Minute

// bynRetry is the wait between asks for a day's BYN in the first day its
// file can be out. After that the day is asked at each step, as a day
// missed.
const bynRetry = time.Hour

// errNotOut: currency-api's file of the day cannot be out yet, so it is not
// asked for.
var errNotOut = errors.New("currency-api: the day's file is not out yet")

// Want is the newest day of the NBU's rates framy should have at now:
// today's, and from publishAt on, tomorrow's. The afternoon fetch means a
// new day starts with its rate already saved. BYN comes later, once
// currency-api's file of the day is out, and until then a price in BYN
// takes the day before, the newest rate currency-api has.
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
// saved rates are behind or a day waits for a file that can be out, so a
// restart costs no request.
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

// Step brings the NBU's rates up to Want when they are behind, fills by
// date the days missing up to the newest, BYN included, and says how long
// to wait before the next step.
func (s *Syncer) Step(ctx context.Context) time.Duration {
	now := s.now()
	until := next(now).Sub(now)
	retry := s.Retry
	if retry <= 0 {
		retry = 15 * time.Minute
	}
	have, err := s.Store.RatesDate(ctx, NBUCodes)
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
			// The NBU has not published the day yet, or it failed and
			// currency-api, standing in, is a day behind.
			s.Log.Info("fx rates behind the day wanted; asking again in an hour", "want", need.Format(time.DateOnly), "have", got.Format(time.DateOnly))
			wait = min(time.Hour, until)
		}
		if got.After(have) {
			have = got
		}
	}
	if have.IsZero() {
		return wait
	}
	if byn := s.fill(ctx, now, have); byn > 0 {
		wait = min(wait, byn)
	}
	return wait
}

// fill reads by date the days missing up to have, the newest day with the
// NBU's rates: from the first day kept in the last fillDays, or from
// yesterday when none was, so a first start has a BYN rate before
// currency-api's file of today is out. A day that has the NBU's rows and
// waits for BYN asks only currency-api, and only once its file can be out.
// fill returns when such a day is next worth asking, zero when none is:
// when its file can be out, then every bynRetry for a day. An older day is
// asked at the next step, and once it is older than fillDays, never.
func (s *Syncer) fill(ctx context.Context, now, have time.Time) time.Duration {
	today := Day(now)
	from := today.AddDate(0, 0, -fillDays)
	if have.Before(from) {
		return 0
	}
	complete, err := s.Store.RateDays(ctx, Stored, from, have)
	if err == nil {
		var official []time.Time
		official, err = s.Store.RateDays(ctx, NBUCodes, from, have)
		if err == nil {
			return s.fillDays(ctx, now, have, complete, official)
		}
	}
	s.Log.Warn("fx days kept unknown; missed days not filled", "error", err)
	return 0
}

// fillDays fills the days from the first of official, or yesterday, up to
// have; complete are the days with every code, official those with the
// NBU's.
func (s *Syncer) fillDays(ctx context.Context, now, have time.Time, complete, official []time.Time) time.Duration {
	start := Day(now).AddDate(0, 0, -1)
	if len(official) > 0 && official[0].Before(start) {
		start = official[0]
	}
	var wait time.Duration
	for d := start; !d.After(have); d = d.AddDate(0, 0, 1) {
		if ctx.Err() != nil {
			return wait
		}
		if slices.ContainsFunc(complete, d.Equal) {
			continue
		}
		out := d.Add(currencyAPIOut)
		// A day with the NBU's rows misses only BYN: nothing to ask before
		// its file can be out.
		saved := slices.ContainsFunc(official, d.Equal)
		if (!saved || !now.Before(out)) && s.fillDay(ctx, now, d, saved) {
			continue
		}
		var w time.Duration
		switch {
		case now.Before(out):
			w = out.Sub(now)
		case now.Before(out.Add(24 * time.Hour)):
			w = bynRetry
		default:
			continue
		}
		if wait == 0 || w < wait {
			wait = w
		}
	}
	return wait
}

// fillDay saves one day by date: the NBU's rows, unless saved says they are
// there, and BYN from currency-api's file of the same day once it can be
// out. It says whether the day has every code now.
func (s *Syncer) fillDay(ctx context.Context, now, d time.Time, saved bool) bool {
	usd, usdErr := USDDay{}, errNotOut
	if !now.Before(d.Add(currencyAPIOut)) {
		usd, usdErr = s.Source.CurrencyAPIOn(ctx, d)
	}
	if saved && usdErr != nil {
		s.noBYN(ctx, now, d, usdErr)
		return false
	}
	nbu, nbuErr := s.Source.NBUOn(ctx, d)
	var rates []Rate
	switch {
	case nbuErr == nil && usdErr == nil:
		rates = FromNBU(nbu, &usd)
	case nbuErr == nil:
		rates = FromNBU(nbu, nil)
	case usdErr == nil && !saved:
		rates = FromCurrencyAPI(usd)
	default:
		// Both failed, or the NBU's rows are saved and its rate of the day
		// was not read: BYN waits for that rate to be anchored on rather
		// than take currency-api's own.
		if ctx.Err() == nil {
			s.Log.Warn("fx missed day not filled", "day", d.Format(time.DateOnly), "error", errors.Join(nbuErr, usdErr))
		}
		return false
	}
	if err := s.Store.SaveRates(ctx, rates); err != nil {
		s.Log.Warn("fx missed day not saved", "day", d.Format(time.DateOnly), "error", err)
		return false
	}
	complete := len(rates) == len(Stored)
	s.Log.Info("fx day filled by date", "day", d.Format(time.DateOnly), "source", rates[0].Source,
		"rows", len(rates), "complete", complete)
	if !complete && !errors.Is(usdErr, errNotOut) {
		s.noBYN(ctx, now, d, usdErr)
	}
	return complete
}

// noBYN logs a day whose BYN currency-api has not given: quietly in the
// first day its file can be out, as the job that makes it runs late, and as
// a warning after.
func (s *Syncer) noBYN(ctx context.Context, now, d time.Time, err error) {
	if ctx.Err() != nil {
		return
	}
	if now.Before(d.Add(currencyAPIOut + 24*time.Hour)) {
		s.Log.Debug("fx BYN of the day not out yet", "day", d.Format(time.DateOnly), "error", err)
		return
	}
	s.Log.Warn("fx day still without BYN", "day", d.Format(time.DateOnly), "error", err)
}

// SyncOnce reads the NBU's newest rates and saves them, and returns the
// newest day saved with the NBU's codes. BYN is not read here: fill reads
// it by date, from currency-api's file of the day itself, which comes out
// hours after the NBU's rate. The NBU failing is a warning, not an error:
// currency-api's newest day stands in, BYN and all. Both failing leave the
// saved rates as they were.
func (s *Syncer) SyncOnce(ctx context.Context) (time.Time, error) {
	var rates []Rate
	nbu, nbuErr := s.Source.NBU(ctx)
	if nbuErr == nil {
		rates = FromNBU(nbu, nil)
	} else {
		usd, usdErr := s.Source.CurrencyAPI(ctx)
		if usdErr != nil {
			metrics.FXSyncFailures.Inc()
			err := errors.Join(nbuErr, usdErr)
			if ctx.Err() == nil {
				s.Log.Error("fx rates unavailable; the saved ones stay", "error", err)
			}
			return time.Time{}, err
		}
		metrics.FXFallbacks.Inc()
		s.Log.Warn("nbu failed; rates from currency-api", "day", usd.Date.Format(time.DateOnly), "error", nbuErr)
		rates = FromCurrencyAPI(usd)
	}
	if err := s.Store.SaveRates(ctx, rates); err != nil {
		metrics.FXSyncFailures.Inc()
		s.Log.Error("fx rates save failed", "error", err)
		return time.Time{}, err
	}
	metrics.FXSyncs.Inc()
	have, err := s.Store.RatesDate(ctx, NBUCodes)
	if err != nil {
		return time.Time{}, err
	}
	s.Log.Info("fx rates saved", "day", rates[0].Date.Format(time.DateOnly), "source", rates[0].Source,
		"rows", len(rates), "newest", have.Format(time.DateOnly))
	return have, nil
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
