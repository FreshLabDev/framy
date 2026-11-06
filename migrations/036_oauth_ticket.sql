-- SPDX-License-Identifier: Apache-2.0
-- One-time tickets to connect OpenRouter from the Mini App (#489). The Mini
-- App opens the connect in the person's browser, where nobody may be signed
-- in to framy; a ticket stands in for that second sign-in. It is made for
-- the person signed in in the Mini App, lives five minutes and is spent by
-- the first start that brings it, whatever that start decides.
--
-- `ticket_hash` is the SHA-256 of the ticket, which only the link the Mini
-- App opens holds. The ticket itself is never stored or logged.

CREATE TABLE oauth_ticket (
  ticket_hash BYTEA PRIMARY KEY CHECK (octet_length(ticket_hash) = 32),
  user_id     BIGINT NOT NULL REFERENCES core.person(telegram_user_id) ON DELETE CASCADE,
  provider    TEXT NOT NULL CHECK (provider IN ('openrouter')),
  expires_at  TIMESTAMPTZ NOT NULL,
  -- One ticket per person and provider: asking again replaces it.
  UNIQUE (user_id, provider)
);

-- Making a ticket sweeps what expired.
CREATE INDEX oauth_ticket_expires_at ON oauth_ticket (expires_at);

-- A flow a ticket started: its callback may come to a browser nobody is
-- signed in on, as long as nobody else is.
ALTER TABLE oauth_state ADD COLUMN ticketed BOOLEAN NOT NULL DEFAULT false;
