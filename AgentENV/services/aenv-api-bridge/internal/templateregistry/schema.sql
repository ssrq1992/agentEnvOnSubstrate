CREATE TABLE aenv_bridge.template_registry (
 tenant text NOT NULL,
 template_id text NOT NULL,
 profile jsonb NOT NULL,
 profile_digest text NOT NULL,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 PRIMARY KEY(tenant,template_id)
);
CREATE TABLE aenv_bridge.template_references (
 tenant text NOT NULL,
 reference text NOT NULL,
 template_id text NOT NULL,
 PRIMARY KEY(tenant,reference),
 FOREIGN KEY(tenant,template_id) REFERENCES aenv_bridge.template_registry(tenant,template_id)
);
CREATE INDEX template_registry_page ON aenv_bridge.template_registry(tenant,created_at DESC,template_id DESC);
