-- SPDX-License-Identifier: Apache-2.0
-- The items of albums that passed through a chat (docs/design.md, `/img` and
-- `/edit` in a group, Albums). Telegram sends every photo of an album as a message of its
-- own and a reply names only one of them, so framy keeps what it sees and
-- reads the whole album back when someone answers one photo with /img. A bot
-- sees every message of a group only as an admin, and always in a DM.
--
-- This is a cache, not a record: rows go after 48 hours (the bot deletes them
-- by created_at every hour), and the text or caption of a message is never
-- stored, only the ids Telegram needs to fetch the file again.
--
-- No foreign keys. A DM is not a chat in core (core.touch leaves it out), and
-- the author is touched before the message is handled but a failed touch is
-- logged and the update goes on, so a key on core.person could turn that
-- failure into a lost item. user_id is the author for the preview to name and
-- is NULL when a message has no sender.

CREATE TABLE album_item (
  chat_id        BIGINT NOT NULL,
  media_group_id TEXT   NOT NULL CHECK (media_group_id <> ''),
  message_id     BIGINT NOT NULL,
  user_id        BIGINT,
  -- A live photo is a photo here: the file is its still frame. A document is
  -- kept only when its MIME type is an image or a video.
  kind           TEXT   NOT NULL CHECK (kind IN ('photo', 'video', 'document')),
  file_id        TEXT   NOT NULL CHECK (file_id <> ''),
  -- Stable across the rotations of file_id; what a job names a source by.
  file_unique_id TEXT   NOT NULL CHECK (file_unique_id <> ''),
  -- A document has no size in pixels, and Telegram leaves file_size out when
  -- it does not know it.
  width          INTEGER,
  height         INTEGER,
  file_size      BIGINT,
  -- The message's own date, which orders nothing (the order is message_id)
  -- and tells how old the photo is.
  sent_at        TIMESTAMPTZ NOT NULL,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (chat_id, message_id)
);

-- Reading an album back.
CREATE INDEX album_item_group ON album_item (chat_id, media_group_id);
-- The hourly delete of what is older than 48 hours.
CREATE INDEX album_item_created ON album_item (created_at);
