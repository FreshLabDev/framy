-- SPDX-License-Identifier: Apache-2.0
-- The clip a chat run edits, as Telegram has it (docs/design.md, `/edit`).
-- The preview offers another length of it: as much as the model takes on the
-- person's key, or the first seconds the free video takes. framy reads the
-- clip from Telegram again for that, since only the part the run takes is
-- kept in the bucket.

ALTER TABLE chat_run
  -- The clip's file_id and its size as the message gave it; empty and 0 for
  -- a run on pictures.
  ADD COLUMN clip_file_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN clip_bytes BIGINT NOT NULL DEFAULT 0 CHECK (clip_bytes >= 0),
  -- The whole clip's length in seconds, as ffprobe read it; 0 for none.
  ADD COLUMN clip_seconds DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (clip_seconds >= 0);
