// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Card is what the site shows of a person: the name and the photo Telegram
// gave at their last sign-in. Empty fields are unknown.
type Card struct {
	Name     string
	PhotoURL string
	At       time.Time // of the sign-in that gave them; zero for no card
}

// SetCard writes the person's name and photo over what was there. A name
// longer than 256 letters is cut; a photo that is not an https link on a host
// the site's CSP lets images come from (t.me and Telegram's CDN, *.telesco.pe)
// is left out. The person must be in core already (Touch).
func (s *Store) SetCard(ctx context.Context, userID int64, name, photoURL string) error {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > 256 {
		name = string([]rune(name)[:256])
	}
	if !telegramPhoto(photoURL) {
		photoURL = ""
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO profile (user_id, display_name, photo_url, card_at)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), now())
		ON CONFLICT (user_id) DO UPDATE SET
		  display_name = EXCLUDED.display_name, photo_url = EXCLUDED.photo_url,
		  card_at = EXCLUDED.card_at, updated_at = now()`,
		userID, name, photoURL)
	if err != nil {
		return fmt.Errorf("set the card of %d: %w", userID, err)
	}
	return nil
}

// Card is the person's name and photo; a zero Card when they never signed
// in.
func (s *Store) Card(ctx context.Context, userID int64) (Card, error) {
	var name, photo *string
	var at *time.Time
	err := s.pool.QueryRow(ctx, `SELECT display_name, photo_url, card_at FROM profile WHERE user_id = $1`, userID).
		Scan(&name, &photo, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return Card{}, nil
	}
	if err != nil {
		return Card{}, fmt.Errorf("card of %d: %w", userID, err)
	}
	var c Card
	if name != nil {
		c.Name = *name
	}
	if photo != nil {
		c.PhotoURL = *photo
	}
	if at != nil {
		c.At = *at
	}
	return c, nil
}

// telegramPhoto: an https link on t.me, where Telegram puts a person's photo
// for the Login and the Mini App, or on telesco.pe, its CDN, where t.me
// redirects. internal/web's img-src names the same two.
func telegramPhoto(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "t.me" || strings.HasSuffix(host, ".telesco.pe")
}
