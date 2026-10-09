ALTER TABLE aenv_bridge.sandboxes DROP CONSTRAINT sandboxes_timeout_seconds_check;
ALTER TABLE aenv_bridge.sandboxes ALTER COLUMN timeout_seconds TYPE bigint;
ALTER TABLE aenv_bridge.sandboxes ADD CONSTRAINT sandboxes_timeout_seconds_check CHECK(timeout_seconds >= 0 AND timeout_seconds <= 4294967295);
CREATE TABLE aenv_bridge.resume_intents (
 tenant text NOT NULL,
 external_id text NOT NULL,
 request_id text NOT NULL,
 source_snapshot_uri text NOT NULL,
 timeout_seconds bigint NOT NULL CHECK(timeout_seconds>=0 AND timeout_seconds<=4294967295),
 extend_only boolean NOT NULL,
 retry_after timestamptz NOT NULL,
 PRIMARY KEY(tenant,external_id),
 FOREIGN KEY(tenant,external_id) REFERENCES aenv_bridge.sandboxes(tenant,external_id),
 FOREIGN KEY(tenant,request_id) REFERENCES aenv_bridge.requests(tenant,request_id)
);
