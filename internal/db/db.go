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
	// stack is the bot this process is (config.Config.Stack): the chat runs
	// and the album items it reads and writes are that bot's, and so is
	// where getUpdates stopped.
	stack string
}

// Option sets up a Store.
type Option func(*Store)

// WithStack makes the store the stack's (migrations/029_stack.sql). Without
// it the store is production's.
func WithStack(stack string) Option { return func(s *Store) { s.stack = stack } }

// Stack is the stack the store is.
func (s *Store) Stack() string { return s.stack }

func Connect(ctx context.Context, databaseURL string, opts ...Option) (*Store, error) {
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
	s := &Store{pool: pool}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Pool is the connection pool, for the job queue, whose River client and
// transactions need pgx itself.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

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

// Offset is where the stack's getUpdates stopped. Production keeps it in
// runtime_state, where every release has read it; any other stack in
// stack_state, from zero.
func (s *Store) Offset(ctx context.Context) (int64, error) {
	var off int64
	if s.stack == "" {
		err := s.pool.QueryRow(ctx, `SELECT telegram_offset FROM runtime_state WHERE singleton`).Scan(&off)
		return off, err
	}
	err := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT telegram_offset FROM stack_state WHERE stack = $1), 0)`, s.stack).Scan(&off)
	return off, err
}

// AdvanceOffset moves the stack's offset forward, never back.
func (s *Store) AdvanceOffset(ctx context.Context, offset int64) error {
	if s.stack == "" {
		_, err := s.pool.Exec(ctx, `UPDATE runtime_state SET telegram_offset=GREATEST(telegram_offset,$1), updated_at=now() WHERE singleton`, offset)
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO stack_state (stack, telegram_offset) VALUES ($1, $2)
		ON CONFLICT (stack) DO UPDATE SET telegram_offset = GREATEST(stack_state.telegram_offset, EXCLUDED.telegram_offset), updated_at = now()`,
		s.stack, offset)
	return err
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
