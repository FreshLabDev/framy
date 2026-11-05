-- SPDX-License-Identifier: Apache-2.0
-- The jobs of a deleted account (docs/design.md, Storage and retention) keep
-- the person's id only while the free allowance still counts them: a person
-- who comes back must not find the free runs of the last day or week given
-- back. After that the row keeps what the day's budget and the books need,
-- its kind, cost and times, and no longer says whose it was.
--
-- forget_at is when that is, set when the account is deleted; user_id
-- becomes NULL then, and only a deleted job may be without one.
--
-- not additive: the check holds for every row the released binary writes,
-- since it always sets user_id, and only production's sweep of a release
-- with this migration sets one to NULL. NOT VALID skips the scan of the
-- rows already there, which all have a user_id.
ALTER TABLE job
  ALTER COLUMN user_id DROP NOT NULL,
  ADD COLUMN forget_at TIMESTAMPTZ,
  ADD CONSTRAINT job_owner CHECK (user_id IS NOT NULL OR deleted_at IS NOT NULL) NOT VALID;

CREATE INDEX job_forget ON job (forget_at) WHERE forget_at IS NOT NULL;
