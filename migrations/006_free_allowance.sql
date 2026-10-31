-- SPDX-License-Identifier: Apache-2.0
-- The free allowance (docs/design.md, Free tier) is counted from job rows,
-- not stored apart. These indexes serve its counts, which only look at free
-- jobs, and the one column remembers when the operator chat was last told.

-- A person's free runs of a kind in the window.
CREATE INDEX job_free_person ON job (user_id, kind, queued_at) WHERE free;
-- The day's free runs, for the budget.
CREATE INDEX job_free_day ON job (queued_at) WHERE free;
-- A free run that failed for lack of money closes the tier for a while; such
-- failures are rare.
CREATE INDEX job_free_money ON job (finished_at) WHERE free AND error_class = 'money';

-- The last UTC day the operator chat was told each fuse had closed the free
-- tier, so it hears once a day.
ALTER TABLE runtime_state
  ADD COLUMN free_budget_alert_day DATE,
  ADD COLUMN free_key_alert_day DATE;
