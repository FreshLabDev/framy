-- SPDX-License-Identifier: Apache-2.0
-- Every generation is a job (docs/design.md, Jobs). River's own tables live
-- in the same schema and are created by rivermigrate at start.

CREATE TABLE job (
  id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id       BIGINT NOT NULL REFERENCES core.person(telegram_user_id),
  surface       TEXT NOT NULL CHECK (surface IN ('site', 'mini_app', 'group', 'dm')),
  -- Where the result goes in Telegram, and the preview message it replaces.
  chat_id       BIGINT,
  message_id    BIGINT,
  -- What started the job, so a repeated update or request starts nothing:
  -- "tg:<update_id>" for a Telegram update, the request's key on the site.
  dedupe_key    TEXT UNIQUE,
  offer_id      TEXT NOT NULL REFERENCES catalog_offer(id),
  model_id      TEXT NOT NULL,
  provider      TEXT NOT NULL,
  kind          TEXT NOT NULL CHECK (kind IN ('image', 'video')),
  prompt        TEXT NOT NULL DEFAULT '',
  settings      JSONB NOT NULL DEFAULT '{}',
  -- The free allowance is counted from these rows.
  free          BOOLEAN NOT NULL DEFAULT false,
  status        TEXT NOT NULL CHECK (status IN ('draft', 'queued', 'running', 'done', 'failed', 'cancelled', 'expired')),
  -- Sent to providers that take one.
  idempotency_key UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
  river_job_id  BIGINT,
  -- Paid requests sent. submitted_at is written before the request goes
  -- out and cleared only when the provider refused it before the run
  -- started, so a run cut short is never sent again.
  submits       INTEGER NOT NULL DEFAULT 0,
  submitted_at  TIMESTAMPTZ,
  -- A request refused with a 429 or a 5xx waits until then, and the
  -- person's other jobs may go ahead of it.
  retry_at      TIMESTAMPTZ,
  provider_job_id TEXT,
  -- The estimate shown before the run (internal/price), and what the
  -- provider charged.
  estimate_usd  NUMERIC(12, 6),
  estimate      JSONB,
  actual_usd    NUMERIC(12, 6),
  results       INTEGER NOT NULL DEFAULT 0,
  error_class   TEXT,
  error         TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  queued_at     TIMESTAMPTZ,
  started_at    TIMESTAMPTZ,
  finished_at   TIMESTAMPTZ,
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((status IN ('done', 'failed', 'cancelled', 'expired')) = (finished_at IS NOT NULL))
);

CREATE INDEX job_user_created ON job (user_id, created_at DESC);
-- The per-person limit counts these.
CREATE INDEX job_user_active ON job (user_id, status) WHERE status IN ('queued', 'running');
CREATE INDEX job_draft ON job (created_at) WHERE status = 'draft';
