-- SPDX-License-Identifier: Apache-2.0
-- Job progress over SSE (docs/design.md, Jobs). A stream follows a job by
-- the number below: it grows with every change a person can see, so an
-- event's id is monotonic per job, a stream that reconnects says the last id
-- it saw (Last-Event-ID) and gets nothing it has already got.
--
-- The change and its notice are one step. The triggers run in the
-- transaction that changes the row, wherever in the service that is: the
-- queue, the sweep, a provider adapter. NOTIFY is delivered when the
-- transaction commits and never if it rolls back, so a listener cannot hear
-- of a state that was not kept. The payload is small on purpose (NOTIFY
-- allows 8000 bytes): the stream rereads the row, which is always the
-- truth, and the payload only says which stream to wake.

ALTER TABLE job ADD COLUMN event_seq BIGINT NOT NULL DEFAULT 1;

-- Bumped when something a person sees changes: the status, the error that
-- says why a job waits or failed, the number of results, the cost. A write
-- of the provider's job id or of updated_at alone is not an event.
CREATE FUNCTION job_event_seq() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.event_seq := OLD.event_seq + 1;
  RETURN NEW;
END $$;

CREATE TRIGGER job_event_seq BEFORE UPDATE ON job
  FOR EACH ROW
  WHEN (OLD.status IS DISTINCT FROM NEW.status
     OR OLD.error_class IS DISTINCT FROM NEW.error_class
     OR OLD.results IS DISTINCT FROM NEW.results
     OR OLD.actual_usd IS DISTINCT FROM NEW.actual_usd)
  EXECUTE FUNCTION job_event_seq();

-- One channel, framy_events: the job, its owner, the number and the status.
CREATE FUNCTION job_event_notify() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('framy_events', json_build_object(
    'job', NEW.id, 'user', NEW.user_id, 'seq', NEW.event_seq, 'status', NEW.status)::text);
  RETURN NULL;
END $$;

CREATE TRIGGER job_event_insert AFTER INSERT ON job
  FOR EACH ROW EXECUTE FUNCTION job_event_notify();

CREATE TRIGGER job_event_update AFTER UPDATE ON job
  FOR EACH ROW
  WHEN (OLD.event_seq IS DISTINCT FROM NEW.event_seq)
  EXECUTE FUNCTION job_event_notify();
