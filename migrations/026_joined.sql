-- SPDX-License-Identifier: Apache-2.0
-- Continue joins the clip to what follows it (docs/design.md, Actions on a
-- result).
--
-- frame_of, frame_at: the clip a last frame was taken from, and where in it,
-- in seconds from its start. A run that starts from that frame goes on from
-- the clip there, so its video is joined to the clip up to that point.
ALTER TABLE asset
  ADD COLUMN frame_of BIGINT REFERENCES asset(id) ON DELETE SET NULL,
  ADD COLUMN frame_at DOUBLE PRECISION CHECK (frame_at >= 0);

CREATE INDEX asset_frame_of ON asset (frame_of) WHERE frame_of IS NOT NULL;

-- joined: the clip a run went on from with the run's video after it, one
-- file, kept beside the run's own result and deleted with it.
ALTER TABLE asset DROP CONSTRAINT asset_kind_check;
ALTER TABLE asset ADD CONSTRAINT asset_kind_check CHECK (kind IN ('upload', 'result', 'joined'));
ALTER TABLE asset DROP CONSTRAINT asset_check;
ALTER TABLE asset ADD CONSTRAINT asset_job_check CHECK ((kind IN ('result', 'joined')) = (job_id IS NOT NULL));

DROP INDEX asset_thumb_missing;
CREATE INDEX asset_thumb_missing ON asset (id) WHERE kind IN ('result', 'joined') AND thumb_key IS NULL;
