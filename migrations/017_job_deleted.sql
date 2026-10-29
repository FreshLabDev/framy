-- SPDX-License-Identifier: Apache-2.0
-- A job the person deleted, alone or with the whole account (docs/design.md,
-- Storage and retention). Its files go, and its prompt, settings, error,
-- grounding and chat are cleared, but the row stays: the free allowance and
-- its daily budget are counted from job rows, and a deleted run must not
-- give its free slot back.
ALTER TABLE job ADD COLUMN deleted_at TIMESTAMPTZ;

-- The history: a person's jobs that were queued, newest first.
CREATE INDEX job_user_history ON job (user_id, id DESC) WHERE deleted_at IS NULL AND queued_at IS NOT NULL;
