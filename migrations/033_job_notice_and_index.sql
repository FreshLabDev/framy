-- SPDX-License-Identifier: Apache-2.0
-- A job's notice names the job and nobody (docs/design.md, Jobs), and the
-- files a chat run points at are found by an index.
--
-- framy shares core-postgres with the family's other bots, and LISTEN asks
-- for no rights: any role connected to the database can hear framy_events.
-- Until now a notice also carried the job's owner, a Telegram user id, and
-- its status, so whoever listened could follow who ran what and when. A
-- stream rereads its job when woken and uses neither. The job's id wakes
-- the stream; the number tells one change's notice from another's.
CREATE OR REPLACE FUNCTION job_event_notify() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('framy_events', json_build_object('job', NEW.id, 'seq', NEW.event_seq)::text);
  RETURN NULL;
END $$;

-- A chat run's result_asset_id (migration 028) points at a file with
-- ON DELETE SET NULL, so deleting any file (a job, an account, the sweep of
-- uploads) looks for the runs that point at it. Without an index that is a
-- scan of every chat run for every file. Few runs point at a file at all:
-- only a result sent from the site that is not the job's first.
CREATE INDEX chat_run_result_asset ON chat_run (result_asset_id) WHERE result_asset_id IS NOT NULL;
