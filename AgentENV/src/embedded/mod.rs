//! Substrate-owned execution. No HTTP API, scheduler, warm pool or autonomous VM creation.
pub mod protocol {
    tonic::include_proto!("agentenv.executor.v1");
}
mod portable;

use std::collections::{BTreeMap, HashMap};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{ensure, Context, Result};
use prost::Message;
use rocksdb::{IteratorMode, Options, WriteOptions, DB};
use sha2::{Digest, Sha256};
use tokio::sync::Mutex;
use tonic::{Request, Response, Status};

use crate::sandbox::{
    AttachedNetwork, BaseSandboxNetworkPolicy, EnvdAccessToken, ExtraDrive,
    FirecrackerCommonConfig, FirecrackerSandbox, FirecrackerSandboxConfig, SandboxBackend,
    SandboxNetworkEgressPolicy, SandboxNetworkPolicy, UblkConfig,
};
use crate::types::SandboxId;
use protocol::executor_server::Executor;
use protocol::*;

pub const PROTOCOL_VERSION: &str = "agentenv-executor-v1";

pub struct ExecutorConfig {
    pub pod_uid: String,
    pub epoch: u64,
    pub root: PathBuf,
    pub max_actors: usize,
    pub max_devices: usize,
    pub max_vcpus: u64,
    pub max_memory_mib: u64,
    pub cgroup_root: PathBuf,
}

struct Actor {
    fence: Fence,
    spec: LaunchSpec,
    runtime: FirecrackerSandbox,
    state: String,
    startup_task: Option<tokio::task::JoinHandle<()>>,
}

struct State {
    actors: HashMap<String, Actor>,
}

#[derive(Clone)]
pub struct EmbeddedExecutor {
    config: Arc<ExecutorConfig>,
    instance: String,
    journal: Arc<DB>,
    state: Arc<Mutex<State>>,
    draining: Arc<AtomicBool>,
}

impl EmbeddedExecutor {
    pub fn open(config: ExecutorConfig) -> Result<Self> {
        ensure!(
            !config.pod_uid.is_empty() && config.epoch > 0,
            "registered Worker identity is required"
        );
        ensure!(
            config.max_actors > 0 && config.max_vcpus > 0 && config.max_memory_mib > 0,
            "positive Worker budgets are required"
        );
        ensure!(
            config.max_actors <= u32::MAX as usize && (4..=65536).contains(&config.max_devices),
            "Actor/device budgets exceed supported range"
        );
        std::fs::create_dir_all(&config.root)?;
        let mut options = Options::default();
        options.create_if_missing(true);
        let journal = DB::open(&options, config.root.join("operations"))?;
        // A process restart is not proof that its detached VM children died.
        // Refuse activation until the Worker supervisor has fenced this Pod.
        for entry in journal.iterator(IteratorMode::Start) {
            let (key, _) = entry?;
            ensure!(
                !key.starts_with(b"live/") && !key.starts_with(b"devices/"),
                "unreconciled runtime or device session from previous executor; fence and replace Worker Pod"
            );
        }
        Ok(Self {
            config: Arc::new(config),
            instance: uuid::Uuid::now_v7().to_string(),
            journal: Arc::new(journal),
            state: Arc::new(Mutex::new(State {
                actors: HashMap::new(),
            })),
            draining: Arc::new(AtomicBool::new(false)),
        })
    }

    pub fn device_session_owner(&self) -> &str {
        &self.instance
    }

    pub fn begin_shutdown(&self) {
        self.draining.store(true, Ordering::Release);
    }

    /// Claim the shared daemon before initialization can create pooled devices.
    /// This marker survives even when no Actor has ever been started.
    pub fn begin_device_session(&self) -> Result<()> {
        ensure!(
            self.journal.get(b"devices/session")?.is_none(),
            "device session already active"
        );
        self.put("devices/session", self.instance.as_bytes())
    }

    /// Called only after daemon shutdown confirms successful cleanup and exit.
    /// Never clear ownership on process death or an unknown shutdown outcome.
    pub async fn complete_device_session(&self) -> Result<()> {
        ensure!(
            self.draining.load(Ordering::Acquire),
            "executor must be draining before device ownership release"
        );
        let state = self.state.lock().await;
        ensure!(state.actors.is_empty(), "Actor runtimes still own devices");
        ensure!(
            self.journal.get(b"devices/session")?.as_deref() == Some(self.instance.as_bytes()),
            "device session ownership changed"
        );
        self.delete("devices/session")
    }

    /// Stop every runtime before the shared ublk daemon can be stopped. Failed
    /// stops retain their journal entries, forcing fencing on the next launch.
    pub async fn shutdown(&self) -> Result<()> {
        self.begin_shutdown();
        let mut state = self.state.lock().await;
        let mut failures = Vec::new();
        let ids: Vec<_> = state.actors.keys().cloned().collect();
        for id in ids {
            let actor = state
                .actors
                .get_mut(&id)
                .context("missing shutdown Actor")?;
            match stop_runtime(actor).await {
                Ok(()) => {
                    if let Some(task) = actor.startup_task.take() {
                        task.abort();
                    }
                    self.delete(&format!("live/{id}"))?;
                    state.actors.remove(&id);
                }
                Err(error) => failures.push(format!("{id}: {error}")),
            }
        }
        ensure!(
            failures.is_empty(),
            "runtime shutdown incomplete: {}",
            failures.join("; ")
        );
        Ok(())
    }

    fn put(&self, key: &str, value: &[u8]) -> Result<()> {
        let mut opts = WriteOptions::default();
        opts.set_sync(true);
        self.journal.put_opt(key, value, &opts)?;
        Ok(())
    }

    fn delete(&self, key: &str) -> Result<()> {
        let mut opts = WriteOptions::default();
        opts.set_sync(true);
        self.journal.delete_opt(key, &opts)?;
        Ok(())
    }

    fn validate_fence(&self, fence: &Fence) -> Result<()> {
        ensure!(
            fence.protocol_version == PROTOCOL_VERSION,
            "protocol version mismatch"
        );
        ensure!(
            fence.worker_pod_uid == self.config.pod_uid
                && fence.worker_epoch == self.config.epoch
                && fence.worker_instance_id == self.instance,
            "stale Worker identity"
        );
        let actor_id = SandboxId::parse_str(&fence.actor_uid).context("invalid Actor UID")?;
        ensure!(
            actor_id.to_string() == fence.actor_uid,
            "Actor UID must be a canonical lowercase UUID"
        );
        ensure!(
            fence.assignment_generation > 0,
            "assignment generation is required"
        );
        Ok(())
    }

    fn path(&self, value: &str) -> Result<PathBuf> {
        let path = Path::new(value);
        ensure!(path.is_absolute(), "absolute Worker path required");
        // Validate the existing parent before creating the leaf, rejecting
        // traversal and symlink escapes out of the Worker's shared volume.
        let parent = path.parent().context("missing parent")?.canonicalize()?;
        ensure!(
            parent.starts_with(self.config.root.canonicalize()?),
            "path escapes Worker root"
        );
        let name = path.file_name().context("missing path name")?;
        let resolved = parent.join(name);
        if resolved.exists() {
            ensure!(
                !resolved.symlink_metadata()?.file_type().is_symlink(),
                "symlink Worker path"
            );
        }
        Ok(resolved)
    }

    async fn apply(
        &self,
        request: OperationRequest,
        effects_started: &mut bool,
    ) -> Result<OperationResponse> {
        let fence = request.fence.as_ref().context("missing fence")?;
        self.validate_fence(fence)?;
        ensure!(
            !request.operation_id.is_empty()
                && request.operation_id.len() <= 128
                && request
                    .operation_id
                    .bytes()
                    .all(|v| v.is_ascii_alphanumeric() || v == b'-' || v == b'_'),
            "invalid operation ID"
        );
        let command = request.command.as_ref().context("missing command")?;
        let digest = hex::encode(Sha256::digest(command.encode_to_vec()));
        ensure!(digest == request.payload_digest, "payload digest mismatch");
        let key = format!(
            "operation/{}/{}/{}",
            fence.actor_uid, fence.assignment_generation, request.operation_id
        );
        let mut state = self.state.lock().await;
        ensure!(
            !self.draining.load(Ordering::Acquire),
            "executor is draining"
        );
        if let Some(old) = self.journal.get(&key)? {
            ensure!(
                old.len() >= 64 && &old[..64] == digest.as_bytes(),
                "operation ID reused with different payload"
            );
            return Ok(OperationResponse::decode(&old[64..])?);
        }
        let now = SystemTime::now().duration_since(UNIX_EPOCH)?.as_millis() as i64;
        ensure!(
            request.deadline_unix_millis > now,
            "operation deadline expired before execution"
        );
        let generation_key = format!("generation/{}", fence.actor_uid);
        if let Some(previous) = self.journal.get(&generation_key)? {
            let old = u64::from_be_bytes(
                previous
                    .as_slice()
                    .try_into()
                    .context("invalid journal generation")?,
            );
            ensure!(
                fence.assignment_generation >= old,
                "stale assignment generation"
            );
            if matches!(
                command.action.as_ref(),
                Some(command::Action::Start(_) | command::Action::Restore(_))
            ) {
                ensure!(
                    fence.assignment_generation > old,
                    "activation requires a new assignment generation"
                );
            }
        }
        if let Some(actor) = state.actors.get(&fence.actor_uid) {
            ensure!(
                actor.fence.assignment_generation == fence.assignment_generation,
                "live Actor has a different assignment"
            );
        }
        let action = command.action.as_ref().context("missing action")?;
        match action {
            command::Action::Start(spec) => self.admit(&state, fence, spec)?,
            command::Action::Restore(spec) => self.admit(
                &state,
                fence,
                spec.launch.as_ref().context("missing restore launch")?,
            )?,
            command::Action::UpdateExtensions(params) => {
                let actor = state
                    .actors
                    .get(&fence.actor_uid)
                    .context("Actor is not active")?;
                ensure!(actor.state == "RUNNING", "Actor is not running");
                ensure!(
                    crate::sandbox::custom_extension::CustomExtensionClient::global().is_some(),
                    "custom extension is not configured"
                );
                ensure!(params.json.len() <= 65536, "extension patch exceeds limit");
                let _: serde_json::Map<String, serde_json::Value> =
                    serde_json::from_str(&params.json)?;
            }
            command::Action::Stop(_) => (),
            _ => ensure!(
                state.actors.contains_key(&fence.actor_uid),
                "Actor is not active"
            ),
        }
        self.put(&generation_key, &fence.assignment_generation.to_be_bytes())?;
        let mut result = OperationResponse {
            effect: Effect::Unknown as i32,
            operation_id: request.operation_id.clone(),
            ..Default::default()
        };
        let mut record = digest.as_bytes().to_vec();
        record.extend(result.encode_to_vec());
        self.put(&key, &record)?;
        *effects_started = true;
        let outcome = self.perform(&mut state, fence, action, &mut result).await;
        match outcome {
            Ok(()) => result.effect = Effect::Completed as i32,
            Err(error) => {
                result.error_code = "EXECUTION_FAILED".into();
                result.error_message = error.to_string();
            }
        }
        let mut record = digest.as_bytes().to_vec();
        record.extend(result.encode_to_vec());
        self.put(&key, &record)?;
        Ok(result)
    }

    fn admit(&self, state: &State, fence: &Fence, spec: &LaunchSpec) -> Result<()> {
        ensure!(
            !state.actors.contains_key(&fence.actor_uid),
            "Actor already exists"
        );
        ensure!(
            state.actors.len() < self.config.max_actors,
            "Worker Actor budget exhausted"
        );
        ensure!(
            spec.vcpus > 0 && spec.memory_mib > 0 && spec.memory_mib <= u32::MAX as u64,
            "invalid machine resources"
        );
        let cpus: u64 = state.actors.values().map(|a| u64::from(a.spec.vcpus)).sum();
        let memory: u64 = state.actors.values().map(|a| a.spec.memory_mib).sum();
        ensure!(
            u64::from(spec.vcpus) <= self.config.max_vcpus.saturating_sub(cpus)
                && spec.memory_mib <= self.config.max_memory_mib.saturating_sub(memory),
            "Worker compute budget exhausted"
        );
        let expected_cgroup = self.config.cgroup_root.canonicalize()?.join(format!(
            "aenv-{}-{}",
            fence.actor_uid, fence.assignment_generation
        ));
        ensure!(
            Path::new(&spec.cgroup_path) == expected_cgroup.as_path()
                && Path::new(&spec.cgroup_path).canonicalize()? == expected_cgroup,
            "Actor cgroup does not match the allocation"
        );
        ensure!(
            !spec.compatibility_domain.is_empty(),
            "compatibility domain is required"
        );
        ensure!(
            spec.image_digest
                .rsplit_once("@sha256:")
                .is_some_and(|(name, d)| !name.is_empty()
                    && d.len() == 64
                    && d.bytes()
                        .all(|v| v.is_ascii_digit() || (b'a'..=b'f').contains(&v))),
            "immutable image digest required"
        );
        Ok(())
    }

    fn common(&self, spec: &LaunchSpec) -> Result<FirecrackerCommonConfig> {
        let mut common = FirecrackerCommonConfig::from_global_config()?;
        let mut seen = std::collections::HashSet::new();
        for asset in &spec.assets {
            ensure!(seen.insert(&asset.name), "duplicate runtime asset");
            ensure!(
                portable::hash_file(Path::new(&asset.path))? == asset.sha256,
                "runtime asset digest mismatch: {}",
                asset.name
            );
        }
        common.firecracker_binary = asset(spec, "firecracker")?.into();
        common.cgroup_path = Some(PathBuf::from(&spec.cgroup_path));
        common.envd_access_token = Some(EnvdAccessToken::from_control_plane(
            spec.envd_access_token.clone(),
        )?);
        common.env_vars = Some(
            spec.env
                .iter()
                .map(|v| (v.name.clone(), v.value.clone()))
                .collect(),
        );
        common.default_workdir = (!spec.cwd.is_empty()).then(|| spec.cwd.clone());
        common.default_user = (!spec.user.is_empty()).then(|| spec.user.clone());
        common.rootfs_virtual_size = (spec.rootfs_bytes > 0).then_some(spec.rootfs_bytes);
        common.firecracker_work_base_dir = Some(self.config.root.join("runtime"));
        std::fs::create_dir_all(
            common
                .firecracker_work_base_dir
                .as_ref()
                .context("runtime directory")?,
        )?;
        let net = spec
            .network
            .as_ref()
            .context("network attachment is required")?;
        ensure!(
            net.tap_name == "tap0" && net.mtu == 1500,
            "unsupported network layout"
        );
        common.attached_network = Some(AttachedNetwork::new(
            net.netns_path.clone().into(),
            net.guest_ipv4.parse()?,
            net.gateway_ipv4.parse()?,
            net.netmask.parse()?,
            net.dns_ipv4.parse()?,
            net.interaction_ipv4.parse()?,
            net.guest_mac.clone(),
            net.platform_denied_cidrs.clone(),
        )?);
        common.network_policy = net.policy.as_ref().map(network_policy).transpose()?;
        common.extra_drives = spec
            .drives
            .iter()
            .map(|drive| {
                ExtraDrive::try_new_overlaybd_with_mount_path(
                    drive.id.clone(),
                    PathBuf::from(&drive.image_config_path),
                    drive.read_only,
                    PathBuf::from(&drive.mount_path),
                    (!drive.sub_path.is_empty()).then(|| PathBuf::from(&drive.sub_path)),
                )
                .and_then(|extra| {
                    if drive.virtual_size == 0 {
                        Ok(extra)
                    } else {
                        extra.try_with_virtual_size(drive.virtual_size)
                    }
                })
            })
            .collect::<Result<Vec<_>>>()?;
        common.custom_extension_params = spec
            .extensions
            .as_ref()
            .map(|p| serde_json::from_str(&p.json))
            .transpose()?;
        Ok(common)
    }

    async fn perform(
        &self,
        state: &mut State,
        fence: &Fence,
        action: &command::Action,
        result: &mut OperationResponse,
    ) -> Result<()> {
        let uid = &fence.actor_uid;
        match action {
            command::Action::Start(spec) => {
                let mut resolved_spec = spec.clone();
                for drive in &mut resolved_spec.drives {
                    if drive.image_config_path.is_empty() {
                        ensure!(
                            !drive.image_digest.is_empty(),
                            "attached drive image required"
                        );
                        drive.image_config_path = crate::image::ImageResolver::new(
                            crate::cfg::ConfigManager::global_config(),
                        )
                        .resolve(&drive.image_digest)
                        .await?
                        .overlaybd_config_path
                        .to_string_lossy()
                        .into_owned();
                    }
                }
                let mut common = self.common(&resolved_spec)?;
                ensure!(
                    spec.argv.first().is_none_or(|cmd| !cmd.is_empty()),
                    "empty startup command"
                );
                let image_path = if spec.image_config_path.is_empty() {
                    let resolved = crate::image::ImageResolver::new(
                        crate::cfg::ConfigManager::global_config(),
                    )
                    .resolve(&spec.image_digest)
                    .await?;
                    let mut env = resolved.base_context.env_vars;
                    env.extend(common.env_vars.take().unwrap_or_default());
                    common.env_vars = Some(env);
                    if common.default_workdir.is_none() {
                        common.default_workdir = resolved.base_context.workdir;
                    }
                    if common.default_user.is_none() {
                        common.default_user = resolved.base_context.user;
                    }
                    resolved.overlaybd_config_path
                } else {
                    PathBuf::from(&spec.image_config_path)
                };
                common.rootfs_image_config = Some(crate::sandbox::OverlaybdConfig {
                    image_config_path: image_path.clone(),
                    read_only: false,
                    runtime_upper_mode: overlaybd::config::UpperMode::LogStructured,
                });
                common.ublk_config = Some(UblkConfig::overlaybd(image_path, false));
                let config = FirecrackerSandboxConfig {
                    common,
                    kernel_image: asset(spec, "kernel")?.into(),
                    boot_args: {
                        let app = crate::cfg::ConfigManager::global_config();
                        let extra = crate::sandbox::firecracker::filter_extra_boot_args(
                            Some(&spec.extra_boot_args),
                            app.firecracker
                                .allowed_extra_boot_args_prefixes
                                .as_deref()
                                .unwrap_or_default(),
                        );
                        match (app.firecracker.boot_args.clone(), extra) {
                            (Some(base), Some(extra)) => Some(format!("{base} {extra}")),
                            (base, None) => base,
                            (None, Some(extra)) => Some(format!(
                                "{} {extra}",
                                crate::sandbox::firecracker::default_boot_args()
                            )),
                        }
                    },
                    vcpu_count: spec.vcpus,
                    mem_size_mib: spec.memory_mib as u32,
                };
                let runtime = FirecrackerSandbox::new_with_id(config, SandboxId::parse_str(uid)?)?;
                self.start_runtime(state, fence, spec, runtime).await?;
                if let Some(command) = spec.argv.first() {
                    use crate::sandbox::SandboxExecutor;
                    let actor = state.actors.get_mut(uid).context("started Actor missing")?;
                    let opts = crate::sandbox::ProcessOpts::new().with_envs(
                        spec.env
                            .iter()
                            .map(|v| (v.name.clone(), v.value.clone()))
                            .collect(),
                    );
                    let args: Vec<&str> = spec.argv.iter().skip(1).map(String::as_str).collect();
                    // Use the owned executor: SandboxExecutor convenience futures are
                    // explicitly !Send and cannot cross the tonic task boundary.
                    let executor = actor.runtime.executor()?;
                    let mut handle = executor.start_process(command, &args, &opts).await?;
                    let actor_uid = uid.clone();
                    actor.startup_task = Some(tokio::spawn(async move {
                        match handle.wait().await {
                            Ok(output) => {
                                tracing::info!(%actor_uid,exit_code=output.exit_code,stdout=%output.stdout,stderr=%output.stderr,"Actor startup process exited")
                            }
                            Err(error) => {
                                tracing::warn!(%actor_uid,%error,"Actor startup process stream ended")
                            }
                        }
                    }));
                }
            }
            command::Action::Restore(restore) => {
                let spec = restore.launch.as_ref().context("missing restore launch")?;
                let working = self.config.root.join("restore").join(uid);
                let mut manifest = portable::load(
                    &self.path(&restore.snapshot_dir)?,
                    &working,
                    &restore.manifest_sha256,
                )?;
                ensure!(
                    manifest.compatibility_domain == spec.compatibility_domain
                        && manifest.vcpus == spec.vcpus
                        && manifest.memory_mib == spec.memory_mib,
                    "snapshot machine compatibility mismatch"
                );
                let assets: BTreeMap<_, _> = spec
                    .assets
                    .iter()
                    .map(|a| (a.name.clone(), a.sha256.clone()))
                    .collect();
                ensure!(manifest.assets == assets, "snapshot runtime asset mismatch");
                ensure!(
                    manifest.network_signature == network_signature(spec)?,
                    "snapshot network layout mismatch"
                );
                let overrides = self.common(spec)?;
                ensure!(
                    manifest.config.common.tools_drive_version == overrides.tools_drive_version,
                    "tools drive version mismatch"
                );
                let old = &mut manifest.config.common;
                apply_restore_options(old, &overrides);
                old.firecracker_binary = overrides.firecracker_binary;
                old.firecracker_work_base_dir = overrides.firecracker_work_base_dir;
                old.attached_network = overrides.attached_network;
                old.cgroup_path = overrides.cgroup_path;
                old.envd_access_token = overrides.envd_access_token;
                old.cpu_config_json = None;
                old.ublk_config = Some(UblkConfig::overlaybd(
                    old.rootfs_image_config
                        .as_ref()
                        .context("missing rootfs")?
                        .image_config_path
                        .clone(),
                    false,
                ));
                let runtime = FirecrackerSandbox::from_snapshot_config_with_override(
                    manifest.config,
                    SandboxId::parse_str(uid)?,
                    old_token(spec)?,
                )?;
                self.start_runtime(state, fence, spec, runtime).await?;
            }
            command::Action::Capture(capture) => {
                let output = self.path(&capture.output_dir)?;
                ensure!(!output.exists(), "capture output must not already exist");
                let actor = state.actors.get_mut(uid).context("Actor is not active")?;
                ensure!(actor.state == "RUNNING", "Actor is not running");
                let tools_source = actor.runtime.tools_drive_source()?;
                let capture_root = tempfile::tempdir_in(&self.config.root)?;
                actor.state = "CAPTURING".into();
                let held;
                let config = if capture.continue_running {
                    let captured = actor.runtime.snapshot().await;
                    held = Some(capture_result(&mut actor.state, captured)?);
                    actor.state = "RUNNING".into();
                    held.as_ref()
                        .context("capture missing")?
                        .downcast_artifacts_ref::<crate::sandbox::FirecrackerCaptureArtifacts>()
                        .context("missing Firecracker capture")?
                        .snapshot_config()
                        .clone()
                } else {
                    held = None;
                    let captured = actor.runtime.capture_to_dir(capture_root.path()).await;
                    let (_, artifacts) = capture_result(&mut actor.state, captured)?;
                    actor.state = "PAUSED".into();
                    *artifacts
                        .context("missing paused capture artifacts")?
                        .downcast::<crate::sandbox::FirecrackerSnapshotConfig>()
                        .map_err(|_| anyhow::anyhow!("invalid paused capture artifacts"))?
                };
                let exported = portable::export(
                    portable::Manifest {
                        format: "agentenv-portable-full-v1".into(),
                        compatibility_domain: actor.spec.compatibility_domain.clone(),
                        network_signature: network_signature(&actor.spec)?,
                        vcpus: actor.spec.vcpus,
                        memory_mib: actor.spec.memory_mib,
                        assets: actor
                            .spec
                            .assets
                            .iter()
                            .map(|a| (a.name.clone(), a.sha256.clone()))
                            .collect(),
                        files: BTreeMap::new(),
                        tools_image: String::new(),
                        config,
                    },
                    &output,
                    tools_source,
                )
                .await;
                drop(held);
                result.snapshot_files = match exported {
                    Ok(files) => files,
                    Err(error) => {
                        if !capture.continue_running {
                            match actor.runtime.resume().await {
                                Ok(()) => actor.state = "RUNNING".into(),
                                Err(resume_error) => {
                                    actor.state = "FAILED".into();
                                    return Err(error.context(format!(
                                        "capture export failed and resume failed: {resume_error}"
                                    )));
                                }
                            }
                        }
                        return Err(error);
                    }
                };
                if !capture.continue_running {
                    stop_runtime(actor).await?;
                    if let Some(task) = actor.startup_task.take() {
                        task.abort();
                    }
                    self.delete(&format!("live/{uid}"))?;
                    state.actors.remove(uid);
                }
            }
            command::Action::Stop(_) => {
                if let Some(actor) = state.actors.get_mut(uid) {
                    stop_runtime(actor).await?;
                    if let Some(task) = actor.startup_task.take() {
                        task.abort();
                    }
                    self.delete(&format!("live/{uid}"))?;
                    state.actors.remove(uid);
                }
            }
            command::Action::UpdatePolicy(policy) => {
                let actor = state.actors.get_mut(uid).context("Actor is not active")?;
                let old = actor
                    .spec
                    .network
                    .as_ref()
                    .and_then(|n| n.policy.as_ref())
                    .map_or(0, |p| p.revision);
                ensure!(policy.revision > old, "stale policy revision");
                actor
                    .runtime
                    .update_network_policy(Some(network_policy(policy)?))
                    .await?;
                actor
                    .spec
                    .network
                    .as_mut()
                    .context("missing network")?
                    .policy = Some(policy.clone());
                result.applied_policy_revision = policy.revision;
            }
            command::Action::UpdateExtensions(params) => {
                let actor = state.actors.get_mut(uid).context("Actor is not active")?;
                ensure!(actor.state == "RUNNING", "Actor is not running");
                let client = crate::sandbox::custom_extension::CustomExtensionClient::global()
                    .context("custom extension is not configured")?;
                let approved = client
                    .hook_patch_params(
                        SandboxId::parse_str(uid)?,
                        serde_json::from_str(&params.json)?,
                    )
                    .await?;
                actor.spec.extensions = approved
                    .as_ref()
                    .map(|value| serde_json::to_string(value).map(|json| ExtensionParams { json }))
                    .transpose()?;
                result.approved_extensions = Some(ExtensionParams {
                    json: approved
                        .as_ref()
                        .map(serde_json::to_string)
                        .transpose()?
                        .unwrap_or_else(|| "{}".into()),
                });
                actor.runtime.update_custom_extension_params(approved);
            }
        }
        Ok(())
    }

    async fn start_runtime(
        &self,
        state: &mut State,
        fence: &Fence,
        spec: &LaunchSpec,
        runtime: FirecrackerSandbox,
    ) -> Result<()> {
        self.put(&format!("live/{}", fence.actor_uid), &fence.encode_to_vec())?;
        let actor = state
            .actors
            .entry(fence.actor_uid.clone())
            .or_insert(Actor {
                fence: fence.clone(),
                spec: spec.clone(),
                runtime,
                state: "STARTING".into(),
                startup_task: None,
            });
        match actor.runtime.start().await {
            Ok(()) => {
                actor.state = "RUNNING".into();
                Ok(())
            }
            Err(error) => {
                actor.state = "FAILED".into();
                Err(error)
            }
        }
    }
}

fn capture_result<T>(
    state: &mut String,
    result: crate::sandbox::SandboxCaptureResult<T>,
) -> Result<T> {
    result.map_err(|error| {
        *state = if error.is_terminal() {
            "FAILED"
        } else {
            "RUNNING"
        }
        .into();
        error.into()
    })
}

async fn stop_runtime(actor: &mut Actor) -> Result<()> {
    ensure!(
        actor.state != "STOP_UNCONFIRMED",
        "previous device cleanup is unconfirmed; fence Worker before reusing device IDs"
    );
    actor.state = "STOPPING".into();
    if let Err(error) = actor.runtime.stop().await {
        actor.state = "STOP_UNCONFIRMED".into();
        return Err(error);
    }
    Ok(())
}

fn network_signature(spec: &LaunchSpec) -> Result<String> {
    let n = spec.network.as_ref().context("missing network")?;
    Ok(format!(
        "{}|{}|{}|{}|{}|{}",
        n.guest_ipv4, n.gateway_ipv4, n.guest_mac, n.netmask, n.dns_ipv4, n.mtu
    ))
}
fn asset<'a>(spec: &'a LaunchSpec, name: &str) -> Result<&'a str> {
    spec.assets
        .iter()
        .find(|a| a.name == name)
        .map(|a| a.path.as_str())
        .with_context(|| format!("missing runtime asset {name}"))
}
// An omitted restore override preserves settings captured with the guest.
// In particular, absence must not weaken an updated network policy or discard
// extension parameters approved by the previous runtime's hooks.
fn apply_restore_options(saved: &mut FirecrackerCommonConfig, requested: &FirecrackerCommonConfig) {
    if let Some(env) = &requested.env_vars {
        saved
            .env_vars
            .get_or_insert_with(Default::default)
            .extend(env.clone());
    }
    if requested.network_policy.is_some() {
        saved.network_policy = requested.network_policy.clone();
    }
    if requested.custom_extension_params.is_some() {
        saved.custom_extension_params = requested.custom_extension_params.clone();
    }
    if requested.default_workdir.is_some() {
        saved.default_workdir = requested.default_workdir.clone();
    }
    if requested.default_user.is_some() {
        saved.default_user = requested.default_user.clone();
    }
}

fn old_token(spec: &LaunchSpec) -> Result<Option<EnvdAccessToken>> {
    Ok(Some(EnvdAccessToken::from_control_plane(
        spec.envd_access_token.clone(),
    )?))
}
fn network_policy(policy: &NetworkPolicy) -> Result<SandboxNetworkPolicy> {
    let base = match network_policy::Base::try_from(policy.base)? {
        network_policy::Base::Default => BaseSandboxNetworkPolicy::Default,
        network_policy::Base::Allow => BaseSandboxNetworkPolicy::Allow,
        network_policy::Base::Deny => BaseSandboxNetworkPolicy::Deny,
    };
    Ok(SandboxNetworkPolicy::new(
        true,
        base,
        SandboxNetworkEgressPolicy::new(
            Some(policy.allow_out.clone()),
            Some(policy.deny_out.clone()),
        )?,
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn restore_preserves_captured_policy_extensions_and_environment() -> Result<()> {
        let policy = crate::sandbox::FirecrackerRuntimePolicy {
            socket_timeout: std::time::Duration::from_secs(1),
            socket_poll_interval: std::time::Duration::from_millis(1),
            envd_timeout: std::time::Duration::from_secs(1),
            envd_poll_interval: std::time::Duration::from_millis(1),
        };
        let mut saved = FirecrackerCommonConfig::new("firecracker".into(), "tools".into(), policy);
        saved.network_policy = Some(network_policy(&NetworkPolicy {
            base: network_policy::Base::Deny as i32,
            ..Default::default()
        })?);
        saved.custom_extension_params = Some(serde_json::from_value::<
            crate::sandbox::CustomExtensionParams,
        >(serde_json::json!({"approved": true}))?);
        saved.env_vars = Some(
            [("SAVED".to_string(), "value".to_string())]
                .into_iter()
                .collect(),
        );
        let prior_policy = serde_json::to_value(&saved.network_policy)?;
        let mut requested =
            FirecrackerCommonConfig::new("firecracker".into(), "tools".into(), policy);
        requested.env_vars = Some(Default::default());
        apply_restore_options(&mut saved, &requested);
        assert_eq!(serde_json::to_value(&saved.network_policy)?, prior_policy);
        assert_eq!(
            saved.custom_extension_params,
            Some(serde_json::from_value::<
                crate::sandbox::CustomExtensionParams,
            >(serde_json::json!({"approved": true}))?)
        );
        assert_eq!(
            saved
                .env_vars
                .as_ref()
                .and_then(|env| env.get("SAVED"))
                .map(String::as_str),
            Some("value")
        );
        requested.network_policy = Some(network_policy(&NetworkPolicy::default())?);
        requested.custom_extension_params = Some(serde_json::from_value::<
            crate::sandbox::CustomExtensionParams,
        >(serde_json::json!({}))?);
        requested.env_vars = Some(
            [("NEW".to_string(), "override".to_string())]
                .into_iter()
                .collect(),
        );
        apply_restore_options(&mut saved, &requested);
        assert_eq!(
            serde_json::to_value(&saved.network_policy)?,
            serde_json::to_value(&requested.network_policy)?
        );
        assert_eq!(
            saved.custom_extension_params,
            Some(serde_json::from_value::<
                crate::sandbox::CustomExtensionParams,
            >(serde_json::json!({}))?)
        );
        assert_eq!(saved.env_vars.as_ref().map(|env| env.len()), Some(2));
        Ok(())
    }

    #[test]
    fn capture_failure_preserves_backend_recovery_classification() {
        let mut state = "CAPTURING".to_string();
        let recoverable =
            crate::sandbox::SandboxCaptureError::recoverable(anyhow::anyhow!("recovered"));
        assert!(capture_result::<()>(&mut state, Err(recoverable)).is_err());
        assert_eq!(state, "RUNNING");
        let terminal =
            crate::sandbox::SandboxCaptureError::terminal(anyhow::anyhow!("cannot thaw"));
        assert!(capture_result::<()>(&mut state, Err(terminal)).is_err());
        assert_eq!(state, "FAILED");
    }

    fn config(root: &Path) -> ExecutorConfig {
        ExecutorConfig {
            pod_uid: "worker-pod".into(),
            epoch: 1,
            root: root.into(),
            max_actors: 2,
            max_devices: 64,
            max_vcpus: 4,
            max_memory_mib: 4096,
            cgroup_root: root.into(),
        }
    }

    fn stop_request(executor: &EmbeddedExecutor, generation: u64) -> OperationRequest {
        let command = Command {
            action: Some(command::Action::Stop(StopSpec {})),
        };
        OperationRequest {
            fence: Some(Fence {
                protocol_version: PROTOCOL_VERSION.into(),
                worker_pod_uid: executor.config.pod_uid.clone(),
                worker_epoch: executor.config.epoch,
                worker_instance_id: executor.instance.clone(),
                actor_uid: uuid::Uuid::now_v7().to_string(),
                assignment_generation: generation,
            }),
            operation_id: "stop-1".into(),
            payload_digest: hex::encode(Sha256::digest(command.encode_to_vec())),
            deadline_unix_millis: i64::MAX,
            command: Some(command),
        }
    }

    async fn execute(executor: &EmbeddedExecutor, request: OperationRequest) -> OperationResponse {
        Executor::execute(executor, Request::new(request))
            .await
            .unwrap()
            .into_inner()
    }

    #[tokio::test]
    async fn retries_preserve_result_and_stop_tombstone_fences_late_requests() -> Result<()> {
        let root = tempfile::tempdir()?;
        let executor = EmbeddedExecutor::open(config(root.path()))?;
        let request = stop_request(&executor, 2);
        let first = execute(&executor, request.clone()).await;
        assert_eq!(first.effect, Effect::Completed as i32);
        assert_eq!(first, execute(&executor, request.clone()).await);

        let mut late = request.clone();
        late.operation_id = "late-stop".into();
        late.fence.as_mut().unwrap().assignment_generation = 1;
        assert_eq!(
            execute(&executor, late).await.effect,
            Effect::NoEffect as i32
        );

        let mut reuse = request;
        reuse.command = Some(Command {
            action: Some(command::Action::Capture(CaptureSpec {
                output_dir: root.path().join("capture").to_string_lossy().into_owned(),
                continue_running: true,
            })),
        });
        reuse.payload_digest = hex::encode(Sha256::digest(
            reuse.command.as_ref().unwrap().encode_to_vec(),
        ));
        assert_eq!(
            execute(&executor, reuse).await.effect,
            Effect::NoEffect as i32
        );
        Ok(())
    }

    #[tokio::test]
    async fn process_restart_changes_identity_and_retains_generation() -> Result<()> {
        let root = tempfile::tempdir()?;
        let executor = EmbeddedExecutor::open(config(root.path()))?;
        let request = stop_request(&executor, 2);
        assert_eq!(
            execute(&executor, request.clone()).await.effect,
            Effect::Completed as i32
        );
        drop(executor);
        let executor = EmbeddedExecutor::open(config(root.path()))?;
        assert_ne!(
            request.fence.as_ref().unwrap().worker_instance_id,
            executor.instance
        );
        assert_eq!(
            execute(&executor, request.clone()).await.effect,
            Effect::NoEffect as i32
        );
        let mut late = request;
        late.fence.as_mut().unwrap().worker_instance_id = executor.instance.clone();
        late.fence.as_mut().unwrap().assignment_generation = 1;
        late.operation_id = "late".into();
        assert_eq!(
            execute(&executor, late).await.effect,
            Effect::NoEffect as i32
        );
        Ok(())
    }

    #[test]
    fn uncertain_previous_runtime_requires_fencing() -> Result<()> {
        let root = tempfile::tempdir()?;
        let executor = EmbeddedExecutor::open(config(root.path()))?;
        executor.put("live/actor", b"unknown")?;
        drop(executor);
        assert!(EmbeddedExecutor::open(config(root.path())).is_err());
        Ok(())
    }

    #[tokio::test]
    async fn device_session_survives_actorless_process_restart() -> Result<()> {
        let root = tempfile::tempdir()?;
        let executor = EmbeddedExecutor::open(config(root.path()))?;
        executor.begin_device_session()?;
        assert!(executor.begin_device_session().is_err());
        assert!(executor.complete_device_session().await.is_err());
        // Stopping Actors alone is not confirmation that the daemon cleaned up.
        executor.shutdown().await?;
        drop(executor);
        assert!(EmbeddedExecutor::open(config(root.path())).is_err());
        Ok(())
    }

    #[tokio::test]
    async fn confirmed_device_shutdown_releases_restart_barrier() -> Result<()> {
        let root = tempfile::tempdir()?;
        let executor = EmbeddedExecutor::open(config(root.path()))?;
        executor.begin_device_session()?;
        executor.shutdown().await?;
        executor.complete_device_session().await?;
        assert!(executor.complete_device_session().await.is_err());
        drop(executor);
        EmbeddedExecutor::open(config(root.path()))?;
        Ok(())
    }

    #[tokio::test]
    async fn shutdown_rejects_new_operations() -> Result<()> {
        let root = tempfile::tempdir()?;
        let executor = EmbeddedExecutor::open(config(root.path()))?;
        executor.shutdown().await?;
        assert_eq!(
            execute(&executor, stop_request(&executor, 1)).await.effect,
            Effect::NoEffect as i32
        );
        Ok(())
    }
}

#[tonic::async_trait]
impl Executor for EmbeddedExecutor {
    async fn capabilities(
        &self,
        _: Request<CapabilitiesRequest>,
    ) -> Result<Response<CapabilitiesResponse>, Status> {
        Ok(Response::new(CapabilitiesResponse {
            protocol_version: PROTOCOL_VERSION.into(),
            worker_instance_id: self.instance.clone(),
            max_actors: self.config.max_actors as u32,
            max_devices: self.config.max_devices as u32,
            capabilities: vec![
                "start".into(),
                "stop".into(),
                "portable-full-v1".into(),
                "online-capture".into(),
                "backend-egress".into(),
                "allocation-cgroup-v2".into(),
                "durable-device-ledger-v1".into(),
            ],
        }))
    }
    async fn execute(
        &self,
        request: Request<OperationRequest>,
    ) -> Result<Response<OperationResponse>, Status> {
        // An RPC deadline must not cancel a partially completed VM mutation.
        let this = self.clone();
        let request = request.into_inner();
        let result = tokio::spawn(async move {
            let operation_id = request.operation_id.clone();
            let mut effects_started = false;
            match this.apply(request, &mut effects_started).await {
                Ok(result) => result,
                Err(error) => OperationResponse {
                    operation_id,
                    effect: if effects_started {
                        Effect::Unknown as i32
                    } else {
                        Effect::NoEffect as i32
                    },
                    error_code: if effects_started {
                        "JOURNAL_RESULT_UNKNOWN"
                    } else {
                        "PRECONDITION_FAILED"
                    }
                    .into(),
                    error_message: error.to_string(),
                    ..Default::default()
                },
            }
        })
        .await
        .map_err(|_| Status::internal("executor operation task failed"))?;
        Ok(Response::new(result))
    }
    async fn inspect(
        &self,
        request: Request<InspectRequest>,
    ) -> Result<Response<InspectResponse>, Status> {
        let request = request.into_inner();
        let fence = request
            .fence
            .context("missing fence")
            .map_err(|e| Status::invalid_argument(e.to_string()))?;
        self.validate_fence(&fence)
            .map_err(|e| Status::failed_precondition(e.to_string()))?;
        let state = self.state.lock().await;
        let mut response = InspectResponse {
            fence: Some(fence.clone()),
            state: "ABSENT".into(),
            ..Default::default()
        };
        if let Some(actor) = state.actors.get(&fence.actor_uid) {
            if actor.fence.assignment_generation != fence.assignment_generation {
                return Err(Status::failed_precondition("stale assignment"));
            }
            response.state = actor.state.clone();
            response.envd_version = actor.runtime.envd_version().to_owned();
            response.rootfs_bytes = actor.runtime.rootfs_bytes();
            response.current_extensions = Some(ExtensionParams {
                json: actor
                    .runtime
                    .current_extension_params()
                    .map(serde_json::to_string)
                    .transpose()
                    .map_err(|_| Status::internal("extension serialization failed"))?
                    .unwrap_or_else(|| "{}".into()),
            });
        }
        if !request.operation_id.is_empty() {
            let key = format!(
                "operation/{}/{}/{}",
                fence.actor_uid, fence.assignment_generation, request.operation_id
            );
            if let Some(record) = self
                .journal
                .get(key)
                .map_err(|_| Status::internal("read operation journal"))?
            {
                if record.len() < 64 {
                    return Err(Status::data_loss("invalid operation journal"));
                }
                response.operation = Some(
                    OperationResponse::decode(&record[64..])
                        .map_err(|_| Status::data_loss("invalid operation result"))?,
                );
            }
        }
        Ok(Response::new(response))
    }
    async fn reconcile(
        &self,
        request: Request<ReconcileRequest>,
    ) -> Result<Response<ReconcileResponse>, Status> {
        let request = request.into_inner();
        if request.worker_pod_uid != self.config.pod_uid
            || request.worker_epoch != self.config.epoch
            || request.worker_instance_id != self.instance
        {
            return Err(Status::failed_precondition("stale Worker identity"));
        }
        let state = self.state.lock().await;
        let actors = state
            .actors
            .values()
            .map(|actor| InspectResponse {
                fence: Some(actor.fence.clone()),
                state: actor.state.clone(),
                ..Default::default()
            })
            .collect();
        // Reconciliation reports observations. Only explicit fenced Stop commands
        // may destroy VMs; a stale or partial assignment list is not authority.
        Ok(Response::new(ReconcileResponse { actors }))
    }
    async fn stats(
        &self,
        request: Request<StatsRequest>,
    ) -> Result<Response<StatsResponse>, Status> {
        let fence = request
            .into_inner()
            .fence
            .ok_or_else(|| Status::invalid_argument("missing fence"))?;
        self.validate_fence(&fence)
            .map_err(|e| Status::failed_precondition(e.to_string()))?;
        let sample = {
            let state = self
                .state
                .try_lock()
                .map_err(|_| Status::unavailable("lifecycle operation in progress"))?;
            let actor = state
                .actors
                .get(&fence.actor_uid)
                .ok_or_else(|| Status::not_found("Actor is not active"))?;
            if actor.fence.assignment_generation != fence.assignment_generation {
                return Err(Status::failed_precondition("stale assignment"));
            }
            actor.runtime.metrics_sample().ok_or_else(|| {
                Status::failed_precondition("metrics unavailable before readiness")
            })?
        }
        .await
        .map_err(|e| Status::unavailable(e.to_string()))?;
        Ok(Response::new(StatsResponse {
            fence: Some(fence),
            observed_at_unix_millis: sample.timestamp.timestamp_millis(),
            memory_used_bytes: sample.mem_used as u64,
            memory_total_bytes: sample.mem_total as u64,
            memory_cache_bytes: sample.mem_cache as u64,
            cpu_used_percent: sample.cpu_used_pct,
            cpu_count: sample.cpu_count as u32,
            disk_used_bytes: sample.disk_used as u64,
            disk_total_bytes: sample.disk_total as u64,
        }))
    }
}
