-- SPDX-License-Identifier: Apache-2.0
-- Sign-in (docs/design.md, Accounts). Telegram Login gives no refresh token,
-- so the site keeps its own sessions, and the Mini App's bearer tokens live
-- in the same table. The secret itself never sits here: `token_hash` is the
-- SHA-256 of the random value the browser holds (a cookie) or the Mini App
-- holds (a bearer token), so a copy of this table signs nobody in.
--
-- A session ends at `expires_at` or when its row is deleted: logout deletes
-- one, "sign out everywhere" deletes a person's, and a person's deletion
-- takes theirs with it.

CREATE TABLE session (
  token_hash BYTEA PRIMARY KEY CHECK (octet_length(token_hash) = 32),
  user_id    BIGINT NOT NULL REFERENCES core.person(telegram_user_id) ON DELETE CASCADE,
  -- A cookie is read only as 'site' and a bearer token only as 'miniapp',
  -- so one cannot stand in for the other.
  kind       TEXT NOT NULL CHECK (kind IN ('site', 'miniapp')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL
);

-- "Sign out everywhere" and a person's deletion.
CREATE INDEX session_by_person ON session (user_id);
-- The sweep of expired rows.
CREATE INDEX session_by_expiry ON session (expires_at);
