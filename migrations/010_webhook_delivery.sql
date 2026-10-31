-- SPDX-License-Identifier: Apache-2.0
-- Webhook deliveries already acted on (docs/design.md, Jobs). OpenRouter
-- sends X-OpenRouter-Idempotency-Key with every video webhook and may send
-- one more than once, and a captured request can be replayed; a delivery
-- whose key is here wakes nothing a second time.
--
-- A key is the SHA-256 of the header (or of the signature), so the table
-- holds nothing a request could be rebuilt from. There is no foreign key to
-- job: tests and cleanups truncate job on its own, and a row of a job that
-- is gone is swept with the rest after a week, which is longer than any
-- sender retries.

CREATE TABLE webhook_delivery (
  provider    TEXT NOT NULL,
  job_id      BIGINT NOT NULL,
  key         TEXT NOT NULL CHECK (char_length(key) <= 100),
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (provider, job_id, key)
);

CREATE INDEX webhook_delivery_received ON webhook_delivery (received_at);
