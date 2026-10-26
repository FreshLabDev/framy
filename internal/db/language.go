// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"fmt"
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
// every automatic source in core, for every bot of the family.
func (s *Store) SetUserLang(ctx context.Context, userID int64, lang string) error {
	return s.setLang(ctx, "user", userID, lang)
}

// ClearUserLang withdraws framy's claim, so the next source in core decides.
// A sibling's manual pick survives it and then wins.
func (s *Store) ClearUserLang(ctx context.Context, userID int64) error {
	return s.clearLang(ctx, "user", userID)
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
