// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"fmt"
	"strings"
	"time"

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

// The connections of one process (docs/design.md, Security and operations).
// core-postgres is shared by the family's bots and by both stacks, so framy
// keeps few and lets the idle ones go. DATABASE_URL may say otherwise:
// pool_max_conns, pool_max_conn_idle_time, statement_timeout,
// idle_in_transaction_session_timeout and application_name in it win.
const (
	// maxConns: the jobs mostly wait on providers, not on the database.
	maxConns = 8
	// maxConnIdle closes a connection nobody used for that long.
	maxConnIdle = 5 * time.Minute
	// statementTimeout ends a statement that hangs, and the locks it waits
	// on or holds. The migrations set their own (Migrate).
	statementTimeout = "30s"
	// idleInTransaction ends a transaction left open with nothing running
	// in it, which holds its locks and keeps vacuum back. The longest framy
	// means to keep one is while an object is deleted from R2.
	idleInTransaction = "10min"
)

// AppName is how the process's connections are named in pg_stat_activity:
// framy for production, framy-<stack> for another stack.
func AppName(stack string) string {
	if stack == "" {
		return Bot
	}
	return Bot + "-" + stack
}

func Connect(ctx context.Context, databaseURL string, opts ...Option) (*Store, error) {
	s := &Store{}
	for _, o := range opts {
		o(s)
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// pgx quotes the URL in some parse errors, password included.
		return nil, fmt.Errorf("parse database url: invalid")
	}
	if !strings.Contains(databaseURL, "pool_max_conns") {
		cfg.MaxConns = maxConns
	}
	if !strings.Contains(databaseURL, "pool_max_conn_idle_time") {
		cfg.MaxConnIdleTime = maxConnIdle
	}
	params := cfg.ConnConfig.RuntimeParams
	if params == nil {
		params = map[string]string{}
		cfg.ConnConfig.RuntimeParams = params
	}
	params["search_path"] = "framy"
	for name, value := range map[string]string{
		"application_name":                    AppName(s.stack),
		"statement_timeout":                   statementTimeout,
		"idle_in_transaction_session_timeout": idleInTransaction,
	} {
		if params[name] == "" {
			params[name] = value
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	s.pool = pool
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
