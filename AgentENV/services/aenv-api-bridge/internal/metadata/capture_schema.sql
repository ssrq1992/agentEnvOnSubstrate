CREATE TABLE aenv_bridge.capture_jobs (
 tenant text NOT NULL,
 request_id text NOT NULL,
 external_id text NOT NULL,
 actor_uid text NOT NULL,
 prepared bytea NOT NULL,
 retry_after timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant,request_id),
 FOREIGN KEY(tenant,request_id) REFERENCES aenv_bridge.requests(tenant,request_id)
);
CREATE TABLE aenv_bridge.snapshot_records (
 tenant text NOT NULL,
 snapshot_id text NOT NULL,
 request_id text NOT NULL,
 tag_uid text NOT NULL,
 record jsonb NOT NULL,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(tenant,snapshot_id),
 UNIQUE(tenant,request_id),
 FOREIGN KEY(tenant,request_id) REFERENCES aenv_bridge.requests(tenant,request_id)
);
CREATE TABLE aenv_bridge.snapshot_names (
 tenant text NOT NULL,
 name text NOT NULL,
 request_id text NOT NULL,
 PRIMARY KEY(tenant,name),
 FOREIGN KEY(tenant,request_id) REFERENCES aenv_bridge.requests(tenant,request_id)
);
CREATE INDEX snapshot_records_page ON aenv_bridge.snapshot_records(tenant,created_at DESC,snapshot_id DESC);
