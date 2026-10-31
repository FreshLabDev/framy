-- SPDX-License-Identifier: Apache-2.0
-- Two bots on one schema (docs/design.md, Stacks): production and the test
-- bot share framy's tables, and each row that only one bot can act on says
-- which. A stack is FRAMY_STACK: '' for production, 'test' for the test bot.
-- Every row before this migration is production's.
--
-- job.stack is the stack that made the job; its River job is in that stack's
-- queues, and a result made on that stack's site goes to the DM from that
-- stack's bot. chat_run.stack is the bot whose chat the run is in: Telegram's
-- message ids are counted per chat and per bot, and a file_id works only for
-- the bot that got it. album_item is that bot's cache, so the stack is part
-- of its key: the DM of one person with each bot has the same chat id.

ALTER TABLE job
  ADD COLUMN stack TEXT NOT NULL DEFAULT '' CHECK (stack ~ '^([a-z][a-z0-9]{0,15})?$');

ALTER TABLE chat_run
  ADD COLUMN stack TEXT NOT NULL DEFAULT '' CHECK (stack ~ '^([a-z][a-z0-9]{0,15})?$');

-- v0.1.0-alpha.1 writes album items by the old key, and fails to after this;
-- go back to it only together with this migration's reverse.
ALTER TABLE album_item
  ADD COLUMN stack TEXT NOT NULL DEFAULT '' CHECK (stack ~ '^([a-z][a-z0-9]{0,15})?$'),
  DROP CONSTRAINT album_item_pkey,
  ADD PRIMARY KEY (stack, chat_id, message_id);
DROP INDEX album_item_group;
CREATE INDEX album_item_group ON album_item (stack, chat_id, media_group_id);

-- Where getUpdates stopped, for every stack but production, which keeps
-- runtime_state.telegram_offset: a release before this one goes on reading
-- it there.
CREATE TABLE stack_state (
  stack           TEXT PRIMARY KEY CHECK (stack ~ '^[a-z][a-z0-9]{0,15}$'),
  telegram_offset BIGINT NOT NULL DEFAULT 0,
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
