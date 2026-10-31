-- SPDX-License-Identifier: Apache-2.0
-- What kind of file a result went out as (docs/design.md, Inline mode). An
-- inline answer resends a result by its file_id, and Telegram takes a photo's
-- id only as a photo and a video's only as a video. A run that ended before
-- this has none and is not offered inline. The person's results are read
-- newest first through job_user_history.

ALTER TABLE chat_run
  ADD COLUMN result_file_type TEXT
    CHECK (result_file_type IN ('photo', 'video', 'animation', 'document'));
