// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The interface language belongs to core, not to framy: a language picked in
// any bot of the family is the same row there, and framy keeps no copy of it.

// UserLang is what core resolves for the person, empty when it has nothing.
func (s *Store) UserLang(ctx context.Context, userID int64) (string, error) {
	var lang *string
	if err := s.pool.QueryRow(ctx, `SELECT core.effective_language($1, NULL, 'user')`, userID).Scan(&lang); err != nil {
		return "", fmt.Errorf("user language of %d: %w", userID, err)
	}
	if lang == nil {
		return "", nil
	}
	return *lang, nil
}

// ChatLang is the group's language in core, empty when core has none. It asks
// for no member's language: user 0 matches no person.
func (s *Store) ChatLang(ctx context.Context, chatID int64) (string, error) {
	var lang *string
	if err := s.pool.QueryRow(ctx, `SELECT core.effective_language(0, $1, 'chat')`, chatID).Scan(&lang); err != nil {
		return "", fmt.Errorf("chat language of %d: %w", chatID, err)
	}
	if lang == nil {
		return "", nil
	}
	return *lang, nil
}

// SetUserLang records a language the person picked by hand. Manual outranks
// every automatic source in core, for every bot of the family. framy notes
// its claim in profile (language_picked), since it may not read core's
// observations.
func (s *Store) SetUserLang(ctx context.Context, userID int64, lang string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT core.set_language($1, 'user'::core.pref_scope, $2, $3, 'manual')`,
			Bot, userID, lang); err != nil {
			return fmt.Errorf("set user language of %d: %w", userID, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO profile (user_id, language_picked) VALUES ($1, true)
			ON CONFLICT (user_id) DO UPDATE SET language_picked = true, updated_at = now()`, userID); err != nil {
			return fmt.Errorf("note the language pick of %d: %w", userID, err)
		}
		return nil
	})
}

// ClearUserLang withdraws framy's claim, so the next source in core decides.
// A sibling's manual pick survives it and then wins.
func (s *Store) ClearUserLang(ctx context.Context, userID int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT core.clear_language($1, 'user'::core.pref_scope, $2)`, Bot, userID); err != nil {
			return fmt.Errorf("clear user language of %d: %w", userID, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE profile SET language_picked = false, updated_at = now()
			WHERE user_id = $1 AND language_picked`, userID); err != nil {
			return fmt.Errorf("note the language of %d is not picked: %w", userID, err)
		}
		return nil
	})
}

// SetChatLang records the language a group admin picked.
func (s *Store) SetChatLang(ctx context.Context, chatID int64, lang string) error {
	return s.setLang(ctx, "chat", chatID, lang)
}

// ClearChatLang withdraws framy's claim on the group's language.
func (s *Store) ClearChatLang(ctx context.Context, chatID int64) error {
	return s.clearLang(ctx, "chat", chatID)
}

func (s *Store) setLang(ctx context.Context, scope string, subject int64, lang string) error {
	if _, err := s.pool.Exec(ctx, `SELECT core.set_language($1, $2::core.pref_scope, $3, $4, 'manual')`,
		Bot, scope, subject, lang); err != nil {
		return fmt.Errorf("set %s language of %d: %w", scope, subject, err)
	}
	return nil
}

func (s *Store) clearLang(ctx context.Context, scope string, subject int64) error {
	if _, err := s.pool.Exec(ctx, `SELECT core.clear_language($1, $2::core.pref_scope, $3)`,
		Bot, scope, subject); err != nil {
		return fmt.Errorf("clear %s language of %d: %w", scope, subject, err)
	}
	return nil
}
