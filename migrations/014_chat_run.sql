-- SPDX-License-Identifier: Apache-2.0
-- A job started from a chat with /img (docs/design.md, `/img` and `/edit` in
-- a group, DM):
-- where its messages are and how far the chat has been told. The job itself
-- is a row of job, which the queue works; this is only what the chat shows
-- of it. The preview becomes the generating loop and then the result or an
-- error card, so one message carries the run from start to end.

CREATE TABLE chat_run (
  job_id             BIGINT PRIMARY KEY REFERENCES job(id) ON DELETE CASCADE,
  chat_id            BIGINT NOT NULL,
  -- The forum topic the command was sent in; 0 for none.
  thread_id          BIGINT NOT NULL DEFAULT 0,
  -- The /img message: the preview replies to it and the reactions go on it.
  command_message_id BIGINT NOT NULL,
  preview_message_id BIGINT,
  -- The language the preview and everything after it are written in.
  lang               TEXT NOT NULL,
  -- What was taken: photos, and how many the album had when framy saw more
  -- than it took (or saw only one of them).
  taken              INTEGER NOT NULL DEFAULT 0 CHECK (taken >= 0),
  album              INTEGER NOT NULL DEFAULT 0 CHECK (album >= 0),
  -- An i18n key of a line about what was taken, such as album.not_admin.
  note               TEXT NOT NULL DEFAULT '',
  --   preview     waiting for Generate or No
  --   generating  the loop is showing; the job is queued or running
  --   done        the result is in the chat
  --   failed      the error card is in the chat
  --   gone        the preview was deleted: No, Stop, or ten minutes passed
  stage              TEXT NOT NULL DEFAULT 'preview'
                     CHECK (stage IN ('preview', 'generating', 'done', 'failed', 'gone')),
  -- When Generate was pressed: a result ready within about 25 s replaces
  -- the loop in place, a later one comes as a new reply.
  generate_at        TIMESTAMPTZ,
  -- Stop was pressed on a run that had started: its result goes to the
  -- person's history and not to the chat.
  stopped            BOOLEAN NOT NULL DEFAULT false,
  result_message_id  BIGINT,
  result_file_id     TEXT,
  result_file_unique_id TEXT,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The runs the chat still has to follow.
CREATE INDEX chat_run_open ON chat_run (job_id) WHERE stage IN ('preview', 'generating');
-- A reply to a result finds its run.
CREATE INDEX chat_run_result ON chat_run (chat_id, result_message_id) WHERE result_message_id IS NOT NULL;
