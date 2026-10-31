// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// Settings are framy's settings of a person beside the currency
// (migrations/025_settings.sql). The zero Settings but Deliver is a person
// who changed nothing.
type Settings struct {
	// LanguagePicked: the language is the person's own pick in framy, not
	// core's default.
	LanguagePicked bool
	// DefaultImage and DefaultVideo are the models the composer opens on,
	// "" for framy's default.
	DefaultImage, DefaultVideo string
	// Deliver: the site's results go to the person's DM.
	Deliver bool
}

// Settings reads the person's settings.
func (s *Store) Settings(ctx context.Context, userID int64) (Settings, error) {
	var st Settings
	var image, video *string
	err := s.pool.QueryRow(ctx, `
		SELECT language_picked, default_image_model, default_video_model, deliver_to_telegram
		FROM profile WHERE user_id = $1`, userID).Scan(&st.LanguagePicked, &image, &video, &st.Deliver)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Settings{Deliver: true}, nil
	case err != nil:
		return Settings{}, fmt.Errorf("settings of %d: %w", userID, err)
	}
	if image != nil {
		st.DefaultImage = *image
	}
	if video != nil {
		st.DefaultVideo = *video
	}
	return st, nil
}

// SetDefaultModel keeps the model the composer opens on for a kind; "" goes
// back to framy's default. The id is not checked against the catalog here.
func (s *Store) SetDefaultModel(ctx context.Context, userID int64, kind catalog.Kind, modelID string) error {
	column := map[catalog.Kind]string{catalog.Image: "default_image_model", catalog.Video: "default_video_model"}[kind]
	if column == "" {
		return fmt.Errorf("default model of %d: no default for %q", userID, kind)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO profile (user_id, `+column+`) VALUES ($1, NULLIF($2, ''))
		ON CONFLICT (user_id) DO UPDATE SET `+column+` = EXCLUDED.`+column+`, updated_at = now()`, userID, modelID)
	if err != nil {
		return fmt.Errorf("set the default %s model of %d: %w", kind, userID, err)
	}
	return nil
}

// SetDelivery says whether the site's results go to the person's DM.
func (s *Store) SetDelivery(ctx context.Context, userID int64, on bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO profile (user_id, deliver_to_telegram) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET deliver_to_telegram = EXCLUDED.deliver_to_telegram, updated_at = now()`,
		userID, on)
	if err != nil {
		return fmt.Errorf("set the delivery of %d: %w", userID, err)
	}
	return nil
}

// Spent is what the runs on one of the person's keys cost, by the
// provider's bill: since the key was saved, and of those the last 30 days.
type Spent struct {
	USD, Last30DaysUSD float64
}

// Spent sums the actual cost of the person's runs on each of their keys, by
// provider: the jobs not on the free allowance, created since the key was
// saved, whose cost is known. A provider with no such run is absent.
func (s *Store) Spent(ctx context.Context, userID int64) (map[string]Spent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.provider, sum(j.actual_usd),
		       COALESCE(sum(j.actual_usd) FILTER (WHERE j.created_at > now() - interval '30 days'), 0)
		FROM connection c
		JOIN job j ON j.user_id = c.user_id AND j.provider = c.provider AND j.created_at >= c.created_at
		WHERE c.user_id = $1 AND NOT j.free AND j.actual_usd IS NOT NULL
		GROUP BY c.provider`, userID)
	if err != nil {
		return nil, fmt.Errorf("spent by %d: %w", userID, err)
	}
	out := map[string]Spent{}
	var provider string
	var sp Spent
	_, err = pgx.ForEachRow(rows, []any{&provider, &sp.USD, &sp.Last30DaysUSD}, func() error {
		out[provider] = sp
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("spent by %d: %w", userID, err)
	}
	return out, nil
}
