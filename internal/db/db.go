// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/tg"
)

// Bot is framy's name in core.bot, core.presence and the language hub.
const Bot = "framy"

type Store struct {
	pool *pgxpool.Pool
}

func Connect(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// pgx quotes the URL in some parse errors, password included.
		return nil, fmt.Errorf("parse database url: invalid")
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "framy"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Touch records the person, and the chat when it is not a DM, in core, with
// the person's Telegram language as a hint for the language hub. It runs for
// every update with a sender, before any domain write that references
// core.person.
func (s *Store) Touch(ctx context.Context, user tg.User, chat *tg.Chat) error {
	var chatID, chatType, chatTitle, chatUname any
	if chat != nil && chat.Type != "private" && chat.ID != 0 {
		chatID = chat.ID
		chatType = chat.Type
		chatTitle = nullString(chat.Title)
		chatUname = nullString(chat.Username)
	}
	_, err := s.pool.Exec(ctx,
		`SELECT core.touch($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		Bot, user.ID, nullString(user.Username), nullString(user.FirstName),
		nullString(user.LastName), nullString(user.LanguageCode), chatID, chatType, chatTitle, chatUname, user.IsBot)
	return err
}

func (s *Store) Offset(ctx context.Context) (int64, error) {
	var off int64
	err := s.pool.QueryRow(ctx, `SELECT telegram_offset FROM runtime_state WHERE singleton`).Scan(&off)
	return off, err
}

func (s *Store) AdvanceOffset(ctx context.Context, offset int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE runtime_state SET telegram_offset=GREATEST(telegram_offset,$1), updated_at=now() WHERE singleton`, offset)
	return err
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
