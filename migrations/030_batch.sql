-- SPDX-License-Identifier: Apache-2.0
-- Frames (docs/design.md, Frames): one press of the site's Create makes
-- every frame, and framy splits them into jobs (POST /api/batches). batch
-- is the press, the same on each of its jobs; batch_frames is the frame of
-- each result the job makes, in order. Both NULL for any other job, so a
-- binary that knows neither keeps working.
ALTER TABLE job
  ADD COLUMN batch TEXT,
  ADD COLUMN batch_frames INTEGER[];

CREATE INDEX job_batch ON job (user_id, batch) WHERE batch IS NOT NULL;
