CREATE TABLE aenv_bridge.sandboxes (
 tenant text NOT NULL,
 external_id text NOT NULL,
 actor_atespace text NOT NULL,
 actor_name text NOT NULL,
 actor_uid text,
 template_alias text NOT NULL,
 timeout_seconds integer NOT NULL CHECK(timeout_seconds > 0 AND timeout_seconds <= 86400),
 expires_at timestamptz NOT NULL,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision > 0),
 deleted boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant,external_id),
 UNIQUE(actor_atespace,actor_name)
);
CREATE INDEX sandboxes_expiry ON aenv_bridge.sandboxes(expires_at) WHERE NOT deleted;
CREATE TABLE aenv_bridge.requests (
 tenant text NOT NULL,
 request_id text NOT NULL,
 payload_digest text NOT NULL CHECK(payload_digest ~ '^[0-9a-f]{64}$'),
 external_id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('create','delete','suspend','resume','timeout','fork','capture','policy','extension')),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','completed','rejected')),
 result bytea,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant,request_id),
 FOREIGN KEY(tenant,external_id) REFERENCES aenv_bridge.sandboxes(tenant,external_id)
);
CREATE INDEX requests_pending ON aenv_bridge.requests(created_at) WHERE state='pending';

-- A committed expiry decision is irreversible even if the delete RPC times out.
-- Extensions lock the sandbox row before checking this durable decision.
CREATE TABLE aenv_bridge.expiry_claims (
 tenant text NOT NULL,
 external_id text NOT NULL,
 request_id text NOT NULL,
 retry_after timestamptz NOT NULL,
 PRIMARY KEY(tenant,external_id),
 FOREIGN KEY(tenant,external_id) REFERENCES aenv_bridge.sandboxes(tenant,external_id),
 FOREIGN KEY(tenant,request_id) REFERENCES aenv_bridge.requests(tenant,request_id)
);
