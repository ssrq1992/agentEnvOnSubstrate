CREATE TABLE aenv_bridge.sandbox_access (
 tenant text NOT NULL,
 external_id text NOT NULL,
 secure boolean NOT NULL,
 allow_public_traffic boolean NOT NULL,
 auto_resume boolean NOT NULL,
 envd_port integer NOT NULL CHECK(envd_port BETWEEN 1 AND 65535),
 PRIMARY KEY(tenant,external_id),
 FOREIGN KEY(tenant,external_id) REFERENCES aenv_bridge.sandboxes(tenant,external_id)
);
CREATE INDEX sandboxes_external_lookup ON aenv_bridge.sandboxes(external_id) WHERE NOT deleted;
