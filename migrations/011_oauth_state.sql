-- SPDX-License-Identifier: Apache-2.0
-- The OAuth flows in progress (docs/design.md, Providers and keys): one row
-- from the moment a person is sent to the provider until the callback comes
-- back, or ten minutes pass. The row is spent by the callback whatever the
-- callback decides, so a state cannot be used twice.
--
-- `state_hash` is the SHA-256 of the state, which only the person's browser
-- holds (in a cookie, and in the callback URL the provider builds). The
-- verifier is the PKCE code_verifier; it never leaves the server except in
-- the one request that exchanges the code. Neither is ever logged.

CREATE TABLE oauth_state (
  state_hash BYTEA PRIMARY KEY CHECK (octet_length(state_hash) = 32),
  -- The person the flow was started for. The callback must come from the same
  -- person. A deleted account takes its flow with it.
  user_id    BIGINT NOT NULL REFERENCES core.person(telegram_user_id) ON DELETE CASCADE,
  provider   TEXT NOT NULL CHECK (provider IN ('openrouter')),
  verifier   TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  -- One open flow per person and provider: starting again replaces it.
  UNIQUE (user_id, provider)
);

-- Starting a flow sweeps what expired.
CREATE INDEX oauth_state_expires_at ON oauth_state (expires_at);
