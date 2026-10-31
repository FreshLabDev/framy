-- SPDX-License-Identifier: Apache-2.0
-- What a run grounded in Google Search brought with its result: Google's
-- search suggestions, the searches the model made and the pages it cited
-- (internal/jobs Grounding). Google's terms ask that the suggestions be shown
-- beside the result, so they are kept with the job.

ALTER TABLE job ADD COLUMN grounding JSONB;
