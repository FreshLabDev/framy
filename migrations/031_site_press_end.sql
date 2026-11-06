-- SPDX-License-Identifier: Apache-2.0
-- The presses of the site that ended lately, which the DM's sender reads
-- every few seconds (internal/db/site.go, SiteResults): a press is found by
-- its last end, of whatever status, and then every done job of it is sent,
-- however long before that it ended itself.
CREATE INDEX job_site_press_end ON job (finished_at) WHERE surface = 'site' AND batch IS NOT NULL;
