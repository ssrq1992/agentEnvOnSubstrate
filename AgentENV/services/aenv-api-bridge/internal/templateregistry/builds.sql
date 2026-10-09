CREATE TABLE aenv_bridge.template_build_jobs (
 tenant text NOT NULL,
 build_id text NOT NULL,
 request_key text NOT NULL,
 payload json NOT NULL,
 digest text NOT NULL,
 state text NOT NULL CHECK(state IN ('queued','allocating','executing','capturing','publishing','ready','failed','uncertain','cancelled')),
 generation bigint NOT NULL DEFAULT 0,
 owner text NOT NULL DEFAULT '',
 lease_until timestamptz,
 actor_name text NOT NULL DEFAULT '',
 cancel_requested boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant,build_id),
 UNIQUE(tenant,request_key)
);
CREATE TABLE aenv_bridge.template_build_logs (
 tenant text NOT NULL,
 build_id text NOT NULL,
 sequence bigint NOT NULL,
 event_id text NOT NULL,
 message text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant,build_id,sequence),
 UNIQUE(tenant,build_id,event_id),
 FOREIGN KEY(tenant,build_id) REFERENCES aenv_bridge.template_build_jobs(tenant,build_id)
);
CREATE INDEX template_build_jobs_queue ON aenv_bridge.template_build_jobs(created_at) WHERE state='queued';
