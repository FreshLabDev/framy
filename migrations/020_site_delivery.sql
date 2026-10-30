-- SPDX-License-Identifier: Apache-2.0
-- A result made on the site, sent to the person's DM (docs/design.md, DM).
-- The row is written before anything goes out, so a result is sent at most
-- once: a send a restart cut short stays 'sending' and is not tried again.
--
--   sending  framy is sending it
--   sent     it is in the DM, in message_id; its chat_run is done
--   closed   the bot may not write to the person: no telegram:bot_access at
--            sign-in and no /start, or the bot is blocked
--   failed   Telegram refused the result
CREATE TABLE site_delivery (
  job_id     BIGINT PRIMARY KEY REFERENCES job(id) ON DELETE CASCADE,
  state      TEXT NOT NULL CHECK (state IN ('sending', 'sent', 'closed', 'failed')),
  message_id BIGINT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The site's results framy reads every few seconds, newest at the end.
CREATE INDEX job_site_done ON job (finished_at) WHERE surface = 'site' AND status = 'done' AND deleted_at IS NULL;
