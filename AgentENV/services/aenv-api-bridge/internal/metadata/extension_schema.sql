CREATE TABLE aenv_bridge.extension_jobs (
    tenant text NOT NULL,
    request_id text NOT NULL,
    external_id text NOT NULL,
    actor_uid text NOT NULL,
    prepared bytea NOT NULL,
    retry_after timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant, request_id),
    FOREIGN KEY (tenant, request_id) REFERENCES aenv_bridge.requests(tenant, request_id),
    FOREIGN KEY (tenant, external_id) REFERENCES aenv_bridge.sandboxes(tenant, external_id)
);
CREATE INDEX extension_jobs_retry ON aenv_bridge.extension_jobs(retry_after);
