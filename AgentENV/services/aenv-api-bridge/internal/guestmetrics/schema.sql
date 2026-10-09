CREATE TABLE aenv_bridge.guest_metrics (
 tenant text NOT NULL,
 external_id text NOT NULL,
 actor_uid text NOT NULL,
 instance_id text NOT NULL,
 generation bigint NOT NULL CHECK(generation > 0),
 observed_ms bigint NOT NULL CHECK(observed_ms > 0),
 collected_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 sample jsonb NOT NULL,
 PRIMARY KEY(tenant,external_id,actor_uid,instance_id,generation,observed_ms),
 FOREIGN KEY(tenant,external_id) REFERENCES aenv_bridge.sandboxes(tenant,external_id)
);
CREATE INDEX guest_metrics_retention ON aenv_bridge.guest_metrics(collected_at);
