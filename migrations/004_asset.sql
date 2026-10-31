-- SPDX-License-Identifier: Apache-2.0
-- Files in R2 (docs/design.md, Storage and retention): the photos people
-- bring and what their runs make. The object is written before its row and
-- removed before it, so a row never names an object that was not written.
-- Nothing cascades into this table: a person or a job with files cannot go
-- before the files do.

CREATE TABLE asset (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id        BIGINT NOT NULL REFERENCES core.person(telegram_user_id),
  -- upload: a photo the person brought, normalized; result: what a run made.
  kind           TEXT NOT NULL CHECK (kind IN ('upload', 'result')),
  job_id         BIGINT REFERENCES job(id),
  object_key     TEXT NOT NULL UNIQUE,
  content_type   TEXT NOT NULL,
  bytes          BIGINT NOT NULL CHECK (bytes >= 0),
  width          INTEGER,
  height         INTEGER,
  sha256         TEXT NOT NULL,
  -- Telegram's ids of the file once it was sent there, to send it again
  -- for free; they go with the row.
  file_id        TEXT,
  file_unique_id TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((kind = 'result') = (job_id IS NOT NULL))
);

CREATE INDEX asset_user_created ON asset (user_id, created_at DESC);
CREATE INDEX asset_job ON asset (job_id) WHERE job_id IS NOT NULL;

-- The files a job takes, in order. A file deleted before the run leaves its
-- place empty, and the run fails as storage before anything is paid.
CREATE TABLE job_input (
  job_id   BIGINT NOT NULL REFERENCES job(id) ON DELETE CASCADE,
  position SMALLINT NOT NULL CHECK (position >= 0),
  asset_id BIGINT REFERENCES asset(id) ON DELETE SET NULL,
  PRIMARY KEY (job_id, position)
);

CREATE INDEX job_input_asset ON job_input (asset_id);

-- The last 10 GB step of the bucket the operator chat was told about.
ALTER TABLE runtime_state ADD COLUMN bucket_alert_step INTEGER NOT NULL DEFAULT 0;
