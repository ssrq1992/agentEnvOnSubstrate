# Standard Kubernetes Pod identity

This directory supplies the stable-API certificate part of an installation. It
uses `TokenReview`, live Pod/ServiceAccount/Node lookups, an audience-bound token,
and an init container plus an ordinary renewal sidecar. Neither
`PodCertificateRequest` nor `ClusterTrustBundle` is required. Existing installers
are unchanged; do not run the experimental pod-certificate-controller installer
for this configuration.

This is not yet the complete AgentENV installer or cluster acceptance. Configure
PostgreSQL, OIDC/authorization grants, object storage, runtime assets, WorkerPool,
SDK Gateway/bridge and external ingress separately. Those components must be
validated together on the target cluster.

## Bootstrap and render

Run from the Substrate repository. Build/publish the two issuer/agent commands
with the version suite. Resolve all `ko://` images before applying manifests.

```sh
go build -o /tmp/ate-identity-bootstrap ./cmd/ate-identity-bootstrap
go build -o /tmp/ate-generic-manifests ./cmd/ate-generic-manifests
/tmp/ate-identity-bootstrap --output=/secure/location/identity-bootstrap.json
```

The output contains a Secret and a public ConfigMap in `ate-system`. The output
file is created exclusively with mode 0600: existing keys and symlinks are never
overwritten. It contains private CA material; store it with the cluster's secret
backups. The namespace must already exist when these resources are applied.
Bootstrap does not contact a cluster.

The issuer deployment in `issuer.yaml` requires that Secret. Its HTTPS certificate
has the `podidentity-issuer.ate-system.svc` DNS identity and a 90-day lifetime;
the two initial CA roots last 365 days. The HTTPS CA and Pod identity CA are
separate. The Pod CA signs a combined SPIFFE/Pod identity and Service DNS
certificate, so `podidentity-ca.crt` and `servicedns-ca.crt` initially match.

Render the existing control/data-plane installation YAML **after** its version,
image and deployment configuration has been resolved:

```sh
/tmp/ate-generic-manifests \
  --issuer=https://podidentity-issuer.ate-system.svc \
  --agent-image=REGISTRY/podidentityagent@sha256:DIGEST \
  < configured-installation.yaml > standard-installation.json
```

The input can be a YAML stream or Kubernetes List. It must exclude the old
pod-certificate controller and experimental certificate resources. The renderer
converts API server, controller, atelet, ingress router and egress gateway Pod
specifications. It also rewrites certificate file paths embedded in ConfigMaps.
An unsupported signer, mixed-purpose projection or remaining experimental
projection fails before any output is written.

Review the rendered resources, then apply the bootstrap Secret/ConfigMap, issuer,
and converted workloads in that order. The issuer allowlist is explicit; the
manifest includes `ate-system/aenv-worker` but deliberately excludes `default`.
WorkerPools must set:

```yaml
spec:
  template:
    serviceAccountName: aenv-worker
  podIdentityIssuer:
    endpoint: https://podidentity-issuer.ate-system.svc
    agentImage: REGISTRY/podidentityagent@sha256:DIGEST
    trustConfigMap: ate-identity-roots
```

Pools in another namespace need their own public roots ConfigMap, ServiceAccount
and an explicit issuer allowlist entry. The CA private key stays in the issuer
namespace. WorkerPool-generated Pods already use the same stable provider and
must not be processed a second time by the manifest renderer.

The renderer enables `--experimental-enable-authz=true` on ateapi (an application
flag, not a Kubernetes experimental API), `--configmap-trust-provider=true` on the
controller and atelet's file-based egress trust provider. The controller produces
`egress-mitm-trust` from the existing `egress-mitm-ca-pool` Secret; atelet waits for
that ConfigMap. Establish the normal OIDC identities and OpenFGA grants before
sending tenant API traffic.

The generic certificate conversion intentionally does not select an object store
or image credential provider. The upstream atelet manifest has GKE-specific
credential-provider hostPaths; remove/replace these and their flags for your node
image. Set `ATE_STORAGE_BACKEND=s3` and the existing S3 provider configuration on
ateapi and atelet for an S3 deployment. Install the existing CRDs and RBAC as well
as the rendered workloads. A rendered file is not a successful installation.

## Rotation and permissions

All trust roots are directory-mounted ConfigMaps, never `subPath` mounts. The
agent rereads the projected token and issuer roots for each renewal and atomically
replaces a combined key/certificate PEM. Conversion uses a dedicated Pod group
65532 and mode 0640 so containers with different UIDs can read the same key;
only the agent mounts the identity volume writable. Existing conflicting Pod
`fsGroup` settings require explicit reconciliation.

For CA rotation, publish old and new public roots together first, wait for all
consumers to receive the update, then switch the signing pool's active CA and
issuer serving certificate. Retain old roots until all old leaf certificates and
connections have drained. Update existing Secret/ConfigMap objects; do not rerun
bootstrap over a live installation. `tls-ca-pool.json` is retained in the bootstrap
Secret for administrative serving-certificate renewal but is not mounted in the
issuer Pod. Automated rotation of the issuer's own serving certificate and CA
roots is still an operational integration item; Pod leaf rotation is implemented.

## Verification status

Go race tests cover conversion of all five upstream workload manifests, rejected
unsafe projections, mount references, initialization order, key permissions,
bootstrap trust separation and overwrite refusal. These tests do not exercise
Kubernetes admission, kubelet projection updates, actual Service connectivity,
KVM/ublk, SDK traffic or full installation/drain/upgrade.
