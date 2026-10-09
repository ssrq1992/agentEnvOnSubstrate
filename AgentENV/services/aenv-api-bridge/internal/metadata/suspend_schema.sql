-- This is a durable timer hold and operation intent, not authoritative runtime state.
CREATE TABLE aenv_bridge.suspend_intents (
 tenant text NOT NULL,
 external_id text NOT NULL,
 request_id text NOT NULL,
 assignment_generation bigint NOT NULL CHECK(assignment_generation >= 0),
 retry_after timestamptz NOT NULL,
 PRIMARY KEY(tenant,external_id),
 FOREIGN KEY(tenant,external_id) REFERENCES aenv_bridge.sandboxes(tenant,external_id),
 FOREIGN KEY(tenant,request_id) REFERENCES aenv_bridge.requests(tenant,request_id)
);
