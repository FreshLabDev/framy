-- SPDX-License-Identifier: Apache-2.0
-- A smaller copy of each result for the site's library (docs/design.md,
-- Storage and retention): a WebP at most 480 px on the long side, of a video
-- its poster frame. It is made when the result is kept; a result kept before
-- this, or whose thumbnail failed, gets one later, tried a few times.

ALTER TABLE asset
  ADD COLUMN thumb_key TEXT,
  ADD COLUMN thumb_width INTEGER,
  ADD COLUMN thumb_height INTEGER,
  ADD COLUMN thumb_bytes BIGINT,
  ADD COLUMN thumb_tries SMALLINT NOT NULL DEFAULT 0;

CREATE INDEX asset_thumb_missing ON asset (id) WHERE kind = 'result' AND thumb_key IS NULL;
