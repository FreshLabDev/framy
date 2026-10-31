// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/framy/internal/fx"
)

// SaveRates writes rates, one row per day and code. A currency-api row never
// replaces the NBU's: the NBU is the source, currency-api the stand-in.
func (s *Store) SaveRates(ctx context.Context, rates []fx.Rate) error {
	batch := &pgx.Batch{}
	for _, r := range rates {
		batch.Queue(`
			INSERT INTO fx_rate (date, code, uah_per_unit, source)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (date, code) DO UPDATE
			SET uah_per_unit = EXCLUDED.uah_per_unit, source = EXCLUDED.source, fetched_at = now()
			WHERE fx_rate.source <> 'nbu' OR EXCLUDED.source = 'nbu'`,
			r.Date, r.Code, r.UAHPerUnit, r.Source)
	}
	if err := s.pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("save fx rates: %w", err)
	}
	return nil
}

// RatesDate is the latest day with a rate of every stored code, zero before
// the first.
func (s *Store) RatesDate(ctx context.Context) (time.Time, error) {
	var day *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT max(date) FROM (
			SELECT date FROM fx_rate WHERE code = ANY($1) GROUP BY date HAVING count(*) = cardinality($1::text[])
		) complete`, fx.Stored).Scan(&day)
	if err != nil {
		return time.Time{}, fmt.Errorf("fx rates date: %w", err)
	}
	if day == nil {
		return time.Time{}, nil
	}
	return *day, nil
}

// RateDays are the days from from to to, both in, with a rate of every
// stored code, oldest first.
func (s *Store) RateDays(ctx context.Context, from, to time.Time) ([]time.Time, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT date FROM fx_rate WHERE code = ANY($1) AND date BETWEEN $2 AND $3
		GROUP BY date HAVING count(*) = cardinality($1::text[])
		ORDER BY date`, fx.Stored, from, to)
	if err != nil {
		return nil, fmt.Errorf("fx rate days: %w", err)
	}
	days, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return nil, fmt.Errorf("fx rate days: %w", err)
	}
	return days, nil
}

// Quote is the rate a price in code is shown with on day: the latest day
// up to it that has both the USD row and code's, else the first after it,
// as on a first start in the afternoon. fx.ErrNoRate when there is none.
func (s *Store) Quote(ctx context.Context, code string, day time.Time) (fx.Quote, error) {
	if code == fx.USD {
		return fx.Dollar, nil
	}
	if !fx.Valid(code) {
		return fx.Quote{}, fmt.Errorf("fx: unknown currency %q", code)
	}
	unit := code
	if code == fx.UAH {
		// The hryvnia needs only the USD row.
		unit = fx.USD
	}
	var usd, x fx.Rate
	err := s.pool.QueryRow(ctx, `
		SELECT u.date, u.uah_per_unit::float8, u.source, x.uah_per_unit::float8, x.source
		FROM fx_rate u JOIN fx_rate x ON x.date = u.date AND x.code = $1
		WHERE u.code = 'USD'
		ORDER BY u.date <= $2 DESC, CASE WHEN u.date <= $2 THEN u.date END DESC, u.date
		LIMIT 1`, unit, day).Scan(&usd.Date, &usd.UAHPerUnit, &usd.Source, &x.UAHPerUnit, &x.Source)
	if errors.Is(err, pgx.ErrNoRows) {
		return fx.Quote{}, fx.ErrNoRate
	}
	if err != nil {
		return fx.Quote{}, fmt.Errorf("fx quote of %s: %w", code, err)
	}
	x.Date, x.Code, usd.Code = usd.Date, unit, fx.USD
	return fx.Cross(code, usd, x), nil
}

// Currency is the currency the person chose in the settings, empty when
// they chose none and the default applies (fx.Default).
func (s *Store) Currency(ctx context.Context, userID int64) (string, error) {
	var code *string
	err := s.pool.QueryRow(ctx, `SELECT currency FROM profile WHERE user_id = $1`, userID).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && code == nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("currency of %d: %w", userID, err)
	}
	return *code, nil
}

// SetCurrency records the person's choice; empty goes back to the default.
// The person must be in core already (Touch).
func (s *Store) SetCurrency(ctx context.Context, userID int64, code string) error {
	if code != "" && !fx.Valid(code) {
		return fmt.Errorf("fx: unknown currency %q", code)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO profile (user_id, currency) VALUES ($1, NULLIF($2, ''))
		ON CONFLICT (user_id) DO UPDATE SET currency = EXCLUDED.currency, updated_at = now()`,
		userID, code)
	if err != nil {
		return fmt.Errorf("set currency of %d: %w", userID, err)
	}
	return nil
}
