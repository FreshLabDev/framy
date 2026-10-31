-- SPDX-License-Identifier: Apache-2.0
-- The file a chat run's result message carries (docs/design.md, DM): a
-- result of the site's that has a joined clip goes to the DM as that clip,
-- not as the run's own video. Send to Telegram reuses the message's file
-- only for this asset. NULL is the job's first result, as before.
ALTER TABLE chat_run
  ADD COLUMN result_asset_id BIGINT REFERENCES asset(id) ON DELETE SET NULL;
