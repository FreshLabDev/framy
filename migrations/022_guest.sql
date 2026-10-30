-- SPDX-License-Identifier: Apache-2.0
-- Guest mode (docs/design.md, Guest mode): @framyin_bot in a chat framy is
-- not a member of. framy answers the guest message once, and that answer is
-- an inline message: the run's preview, loop and result are edits of it by
-- inline_message_id, since framy cannot send anything else to that chat.

ALTER TABLE job DROP CONSTRAINT job_surface_check;
ALTER TABLE job ADD CONSTRAINT job_surface_check
  CHECK (surface IN ('site', 'mini_app', 'group', 'dm', 'guest'));

ALTER TABLE chat_run ADD COLUMN inline_message_id TEXT;
