CREATE TABLE aenv_bridge.creation_jobs (
 tenant text NOT NULL,
 external_id text NOT NULL,
 request_id text NOT NULL,
 prepared bytea NOT NULL,
 retry_after timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant,external_id),
 FOREIGN KEY(tenant,external_id) REFERENCES aenv_bridge.sandboxes(tenant,external_id),
 FOREIGN KEY(tenant,request_id) REFERENCES aenv_bridge.requests(tenant,request_id)
);
CREATE TABLE aenv_bridge.sandbox_profiles (
 tenant text NOT NULL,
 external_id text NOT NULL,
 profile jsonb NOT NULL,
 PRIMARY KEY(tenant,external_id),
 FOREIGN KEY(tenant,external_id) REFERENCES aenv_bridge.sandboxes(tenant,external_id)
);
