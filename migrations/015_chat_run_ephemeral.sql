-- SPDX-License-Identifier: Apache-2.0
-- A preview only the requester sees (docs/design.md, `/img` and `/edit` in a
-- group). Telegram numbers ephemeral messages apart from the chat's, so
-- preview_message_id says which kind it is by preview_ephemeral.

ALTER TABLE chat_run
  -- The preview is an ephemeral message to the requester: framy is an admin
  -- of the group, or the command itself was ephemeral. Generate posts the
  -- loop in the chat and clears this.
  ADD COLUMN preview_ephemeral BOOLEAN NOT NULL DEFAULT false,
  -- The command was ephemeral: nothing in the chat can reply to it.
  ADD COLUMN command_ephemeral BOOLEAN NOT NULL DEFAULT false,
  -- The Model and Settings panel, an ephemeral message the requester sees
  -- in place of a preview the chat sees.
  ADD COLUMN panel_message_id BIGINT;

COMMENT ON COLUMN chat_run.command_message_id IS 'The /img message; the preview replies to it.';
