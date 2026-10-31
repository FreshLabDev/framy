-- SPDX-License-Identifier: Apache-2.0
-- The keys people paste (docs/design.md, Providers and keys). One connection
-- per person and provider. The key itself never sits here in the clear:
-- `ciphertext` is the key under a data key of its own, `wrapped_dek` is that
-- data key under the key-encryption key named by `kek_version`. Both are the
-- nonce, the ciphertext and the tag in one value. What binds them to this
-- row is their associated data (internal/keys/envelope.go), not a column.
--
-- Only `last4` is ever shown. A person's deletion takes the row with it.

CREATE TABLE connection (
  -- Chosen by the service before the insert, since it is part of the
  -- associated data. A replaced key is a new connection with a new id, so
  -- what a run reports about a key cannot land on the key that replaced it.
  id               UUID PRIMARY KEY,
  user_id          BIGINT NOT NULL REFERENCES core.person(telegram_user_id) ON DELETE CASCADE,
  provider         TEXT NOT NULL CHECK (provider IN ('openrouter', 'openai', 'gemini')),
  ciphertext       BYTEA NOT NULL,
  wrapped_dek      BYTEA NOT NULL,
  kek_version      INTEGER NOT NULL CHECK (kek_version > 0),
  last4            TEXT NOT NULL CHECK (char_length(last4) = 4),
  -- What framy can back up: valid and invalid come from the check on save
  -- and from a re-check; the rest from what a run showed.
  status           TEXT NOT NULL CHECK (status IN ('valid', 'verified', 'needs_billing', 'needs_verification', 'no_credits', 'invalid')),
  -- The last run that succeeded on this key.
  last_success_at  TIMESTAMPTZ,
  -- The class (internal/jobs) of the last run that failed on it; cleared
  -- when a run succeeds.
  last_error_class TEXT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, provider)
);
