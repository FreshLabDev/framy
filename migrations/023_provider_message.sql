-- SPDX-License-Identifier: Apache-2.0
-- What the provider said when a job failed or waits, in its own words and
-- without framy's wrapping (docs/design.md, Jobs): the message the site
-- shows. job.error keeps the whole line, for the logs and for support.

ALTER TABLE job ADD COLUMN provider_message TEXT;
