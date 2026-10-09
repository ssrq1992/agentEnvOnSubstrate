CREATE SCHEMA IF NOT EXISTS aenv_bridge;
CREATE TABLE IF NOT EXISTS aenv_bridge.catalog_layers (
 digest text PRIMARY KEY CHECK (digest ~ '^[0-9a-f]{64}$'),
 size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
 uploaded boolean NOT NULL DEFAULT false,
 delete_after timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS aenv_bridge.catalog_operations (
 tenant text NOT NULL,
 operation_id text NOT NULL,
 payload_digest text NOT NULL,
 state text NOT NULL CHECK (state IN ('reserved','committed','released')),
 owner_kind text,
 owner_uid text,
 PRIMARY KEY (tenant, operation_id)
);
CREATE TABLE IF NOT EXISTS aenv_bridge.catalog_owner_state (
 tenant text NOT NULL, kind text NOT NULL, uid text NOT NULL,
 released boolean NOT NULL DEFAULT false,
 PRIMARY KEY(tenant,kind,uid)
);
CREATE TABLE IF NOT EXISTS aenv_bridge.catalog_runtime_state (
 tenant text NOT NULL, actor_uid text NOT NULL, generation bigint NOT NULL,
 worker_pod_uid text NOT NULL, worker_epoch bigint NOT NULL,
 executor_instance_id text NOT NULL, stopped boolean NOT NULL DEFAULT false,
 PRIMARY KEY(tenant,actor_uid)
);
CREATE TABLE IF NOT EXISTS aenv_bridge.catalog_operation_refs (
 tenant text NOT NULL,
 operation_id text NOT NULL,
 verified boolean NOT NULL DEFAULT false,
 digest text NOT NULL REFERENCES aenv_bridge.catalog_layers(digest),
 PRIMARY KEY (tenant, operation_id, digest),
 FOREIGN KEY (tenant, operation_id) REFERENCES aenv_bridge.catalog_operations(tenant, operation_id)
);
CREATE TABLE IF NOT EXISTS aenv_bridge.catalog_owners (
 tenant text NOT NULL,
 kind text NOT NULL,
 uid text NOT NULL,
 digest text NOT NULL REFERENCES aenv_bridge.catalog_layers(digest),
 PRIMARY KEY (tenant, kind, uid, digest)
);
CREATE TABLE IF NOT EXISTS aenv_bridge.catalog_pins (
 tenant text NOT NULL,
 actor_uid text NOT NULL,
 generation bigint NOT NULL CHECK (generation > 0),
 worker_pod_uid text NOT NULL,
 worker_epoch bigint NOT NULL CHECK (worker_epoch > 0),
 executor_instance_id text NOT NULL,
 digest text NOT NULL REFERENCES aenv_bridge.catalog_layers(digest),
 PRIMARY KEY (tenant, actor_uid, digest)
);
CREATE TABLE IF NOT EXISTS aenv_bridge.catalog_outbox (
 release_id text PRIMARY KEY,
 tenant text NOT NULL,
 owner_kind text NOT NULL,
 owner_uid text NOT NULL,
 completed boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS catalog_layers_gc ON aenv_bridge.catalog_layers(delete_after) WHERE delete_after IS NOT NULL;
CREATE INDEX IF NOT EXISTS catalog_owner_digest ON aenv_bridge.catalog_owners(digest);
CREATE INDEX IF NOT EXISTS catalog_operation_digest ON aenv_bridge.catalog_operation_refs(digest);
CREATE INDEX IF NOT EXISTS catalog_pin_digest ON aenv_bridge.catalog_pins(digest);
