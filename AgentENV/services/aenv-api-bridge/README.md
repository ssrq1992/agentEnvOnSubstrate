# AgentENV Substrate API bridge (in development)

Implemented routes include v1/v2 sandbox creation from configured immutable
native templates, list/info, deletion, timeout updates, network policy
replacement, metrics history, persistent pause, legacy resume, connect, batch fork,
and runtime custom extension parameter GET/PATCH. Template list (v1/v2), alias lookup
and detail read from the persistent tenant-scoped template registry.
The SDK data proxy routes HTTP, streaming input, and WebSocket upgrades through
Substrate ingress. Dynamic template build/import/deletion, volume APIs,
and shared-layer integration remain in development.
These source implementations have not passed real VM/Kubernetes SDK acceptance.

Build from this directory with `go build ./cmd/aenv-api-bridge`. Run
`aenv-api-bridge CONFIG.json` with `AENV_BRIDGE_DATABASE_URL` set to a PostgreSQL
DSN using verified TLS in deployment. The process applies checksummed catalog
and metadata migrations, then serves HTTPS and enforces persisted timeouts.
Metadata migrations upgrade v1 through v8 transactionally and verify every prior
migration checksum. The metrics, catalog and v1 template registry migrations are checked independently.

Configuration:

```json
{
  "listen": ":8443",
  "sandboxDomain": "sandboxes.example.test",
  "certificate": "/identity/tls.crt",
  "privateKey": "/identity/tls.key",
  "gatewayRoots": "/identity/roots.pem",
  "gatewayIdentities": ["spiffe://example.test/service/gateway"],
  "controlAddress": "ateapi.example.test:443",
  "controlServerName": "ateapi.example.test",
  "controlRoots": "/identity/control-roots.pem",
  "ingressOrigin": "https://atenet-ingress.internal.example.test",
  "ingressRoots": "/identity/ingress-roots.pem",
  "ingressCredentialBundle": "/identity/credential-bundle.pem",
  "dataCredentialKeyFile": "/credentials/data-key",
  "autoResumeTimeout": 300,
  "templates": [{
    "tenant": "team-a", "id": "python", "alias": "python-default",
    "name": "agentenv-python", "envdPort": 49983, "envdVersion": "FROM_PINNED_RUNTIME",
    "cpuCount": 1, "memoryMB": 512, "diskSizeMB": 20480
  }],
  "tenants": [{
    "id": "team-a",
    "atespace": "team-a",
    "apiKeySHA256": "REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS",
    "controlTokenFile": "/credentials/team-a-token"
  }]
}
```

Gateway must present a verified certificate with exactly one allowed SPIFFE
URI. Client headers cannot choose a tenant. Each tenant's OIDC token must be
accepted and authorized by Substrate; enable Substrate authorization enforcement.
The bridge uses server-authenticated TLS and per-request bearer tokens for
Substrate, not a shared client certificate that would override tenant identity.
Gateway trust roots and bridge certificates reload on each TLS handshake.
The control-plane trust bundle also reloads when gRPC establishes a connection.

Secrets and certificates must be readable by UID 65532 in the container. Publish
certificate/key pairs atomically, and retain overlapping roots during rotation.
The bridge does not issue credentials or grant Substrate roles itself.

Run `go test -race ./...` and `go vet ./...`. Database tests require a disposable
PostgreSQL administrator DSN in `AENV_CATALOG_TEST_DSN`; they create randomly
named databases. CI and `REQUIRE_CATALOG_DATABASE=true` fail if this is absent.
Local skips are not database acceptance. Use `integration/Dockerfile.bridge`
from the parent two-repository build context for the Linux image; it has not yet
been built or exercised in Kubernetes.


Persistent pause reserves a timer hold before calling Substrate. Recovery keeps
the original assignment generation; a late pause cannot suspend a newer resumed
allocation. Resume binds to the recorded source snapshot URI and only rearms the
timer after RUNNING is confirmed. Tokens are fetched from the current runtime and
never stored in receipts. A background loop retries unknown lifecycle outcomes.
Connect extends an existing deadline; resuming a paused sandbox replaces it.
Legacy timeout values accept the original uint32 range, including zero; v2
connect requires at least one second and defaults to 300. The sandbox domain is
required configuration for SDK connection responses; provision its DNS and
traffic routing separately as part of the integration deployment.

Metrics sampling runs every 15 seconds with a 12-second sweep budget and 8
concurrent calls. PostgreSQL retains samples for one hour and isolates tenants
and Actor incarnations. This shared history survives bridge restarts, unlike the
original node-local history; include this difference in compatibility testing.
Creation defaults autoPause to true and autoResume to false, matching the original.
Timeout claims are committed before RPC; autoPause transfers its claim atomically
into a generation-fenced suspension hold. autoPause=false retains deletion.
Incoming data credentials are checked before autoResume, which uses persistent
restore and commits TTL through the same lifecycle service.

The data key file contains exactly 32 random bytes and must be shared unchanged
by bridge replicas. Generate it once as a Kubernetes Secret; never regenerate on
restart. Sandbox-scoped HMAC credentials remain valid across pause/restore, while
the proxy injects the fresh allocation-local envd credential. Application ports
never receive envd/API/traffic credentials. Secure and allowPublicTraffic have
separate authorization semantics. Rotating this key invalidates issued proxy
credentials and requires clients to connect again.

Ingress is an internal service: restrict its listener with Kubernetes NetworkPolicy
so external clients cannot bypass bridge authentication or trigger the original
unbound native auto-resume route. The bridge always sends Actor UID and assignment
generation; the router uses a read-only GetActor path for these requests, and the
Worker verifies both before forwarding. Configure the router's authorized principal
for GetActor and its existing Worker mTLS identity.

Template profiles map SDK IDs/aliases to native ActorTemplates. Explicit CPU and
memory limits must match the registered profile; image references and runtime
assets remain pinned. Creation freezes a private template specification to carry
environment variables and initial extension parameters. The background creation
job retains unknown outcomes, and timeout starts only after RUNNING and runtime
version are confirmed. Full template publication/GC and advanced volume/fork launch
preparations still need completion.

原生快照控制路径已加入 `Capture` 和携带源 Tag UID 的 `CreateFromSnapshot`；创建 job 支持复用捕获模板及每个子实例独立确认。批量 fork HTTP 路由和父操作后台恢复已接入；真实 VM/S3/SDK 与带卷 fork 验收尚未完成。


`POST /sandboxes/{sandboxID}/fork` accepts optional `count` (1–100) and
`timeout` (u32 seconds), inheriting the parent timeout when omitted. Supply a
stable `Idempotency-Key` to retry the same operation. Metadata v7 freezes source
UID/generation, template, policy, access options and deterministic child IDs.
Capture publishes a single Tag; the parent may then stop independently. Each
child is a separate durable creation job and receives its own UID and runtime
credentials. Successful and cleaned-up failed child outcomes are individually
committed before the overall receipt. Receipts contain no bearer credentials.
Timeouts remain pending and are reconciled; a permanent child failure is
reported only after UID-bound deletion acknowledgment. Unknown or unbound
cleanup remains pending instead of assuming the child never existed. The
pending capture holds expiry and persistent pause until capture is confirmed.

Fork Tags currently retain their independent snapshot ownership. Integration
with catalog operation pins and eventual unborrowed Tag collection is still
required; automatic catalog GC must not be enabled for these objects yet.


Runtime extension parameters:

- `GET /sandboxes/{sandboxID}/custom-extension-params` returns the complete
  hook-approved JSON object. `PATCH` forwards an object patch and returns the
  approved full object, matching the existing AgentENV route shape.
- `Idempotency-Key` freezes the original Actor UID, assignment generation,
  expected parameter revision and operation ID. Requests with a different body
  cannot reuse the same key. JSON numbers retain their original precision.
- Metadata v8 commits extension jobs and lifecycle holds before dispatch.
  A background worker replays the same frozen request after bridge restart or
  transport failure. Only a matching control-plane approval or explicit
  no-effect receipt completes the job. A confirmed no-effect rejection returns
  HTTP 400; unconfirmed execution remains pending and returns an unavailable
  error, never an optimistic approval.
- Pending extension jobs exclude timeout handling, pause and uncaptured fork
  reservations. Explicit user deletion still tears down the Actor normally.
  Holds release after a confirmed approval/rejection. Completed receipts are
  immutable and contain the full approved parameters.
- Creation and snapshot-child creation observe actual runtime parameters before
  confirming the SDK creation and starting its timer, so the initial durable
  pause preserves GET behavior. Legacy suspended Actors without an observed
  runtime state require restoration for the first observation.

Go race tests cover database holds/migrations (v1/v7 to v8), restart/retry,
unknown results, commit failure, exact receipt validation, tenant isolation,
JSON precision and HTTP shape. Real extension hook/VM/S3/SDK acceptance remains
unexecuted; this does not close all F10 resource/cgroup acceptance requirements.


Configured templates are imported into the persistent directory after resolving
and validating their native ActorTemplate UID, specification digest and machine
resources. An existing published ID cannot change specification, and references
cannot steal another template ID or alias. SDK creation reads this directory and
rejects native name reuse/specification changes. Removing an entry from startup
configuration does not delete its persistent directory record.

`GET /templates`, `GET /v2/templates`, `GET /templates/aliases/{alias}` and
`GET /templates/{templateID}` expose committed pre-provisioned native artifacts.
Their imported build ID is the native ActorTemplate UID. These routes do not
implement the V3/V2 dynamic build pipeline or provide BuildKit execution evidence.


Template build persistence now has a separate checksummed v1 migration, applied
at bridge startup. The registry stores frozen requests, fenced build leases,
cancellation intent, and bounded durable logs. Expired in-flight jobs become
uncertain and are never automatically dispatched again. Alias replacement uses
a transactional comparison against the previously observed target. Registry
profiles and build inputs are verified against their stored digests on reads.
These methods are not yet wired to the SDK build write routes or an Actor build
executor; they do not constitute a completed template build pipeline.
