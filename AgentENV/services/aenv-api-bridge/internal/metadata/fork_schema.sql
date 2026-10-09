CREATE TABLE aenv_bridge.fork_jobs (
 tenant text NOT NULL,
 request_id text NOT NULL,
 external_id text NOT NULL,
 actor_uid text NOT NULL,
 prepared bytea NOT NULL,
 snapshot bytea,
 captured boolean NOT NULL DEFAULT false,
 retry_after timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant,request_id),
 FOREIGN KEY(tenant,request_id) REFERENCES aenv_bridge.requests(tenant,request_id),
 FOREIGN KEY(tenant,external_id) REFERENCES aenv_bridge.sandboxes(tenant,external_id)
);
CREATE TABLE aenv_bridge.fork_children (
 tenant text NOT NULL,
 request_id text NOT NULL,
 child_index integer NOT NULL CHECK(child_index>=0 AND child_index<100),
 child_id text NOT NULL,
 failed boolean NOT NULL,
 PRIMARY KEY(tenant,request_id,child_index),
 FOREIGN KEY(tenant,request_id) REFERENCES aenv_bridge.fork_jobs(tenant,request_id)
);
