use agentenv::embedded::protocol::executor_server::ExecutorServer;
use agentenv::embedded::{EmbeddedExecutor, ExecutorConfig};
use anyhow::{ensure, Result};
use clap::Parser;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

#[derive(Parser)]
struct Args {
    #[arg(long)]
    config: PathBuf,
    #[arg(long)]
    socket: PathBuf,
    #[arg(long)]
    root: PathBuf,
    #[arg(long)]
    worker_pod_uid: String,
    #[arg(long)]
    worker_epoch: u64,
    #[arg(long, default_value_t = 1)]
    max_actors: usize,
    #[arg(long)]
    max_vcpus: u64,
    #[arg(long)]
    max_memory_mib: u64,
    #[arg(long)]
    cgroup_root: PathBuf,
    #[arg(long, default_value_t = 64)]
    max_devices: usize,
}

#[tokio::main]
async fn main() -> Result<()> {
    agentenv::logging::init();
    let args = Args::parse();
    let config = agentenv::cfg::ConfigManager::init_global_from_path(&args.config)?.config();
    ensure!(
        (4..=65536).contains(&args.max_devices),
        "device budget must be between 4 and 65536"
    );
    let device_root = args.root.join("devices");
    let (service, listener) = claim_executor(
        ExecutorConfig {
            pod_uid: args.worker_pod_uid,
            epoch: args.worker_epoch,
            root: args.root,
            max_actors: args.max_actors,
            max_devices: args.max_devices,
            max_vcpus: args.max_vcpus,
            max_memory_mib: args.max_memory_mib,
            cgroup_root: args.cgroup_root,
        },
        &args.socket,
    )?;
    let mut daemon_config = agentenv::sandbox::UblkDaemonConfig::from_app_config(config)?;
    // Idle prewarm must leave room for live/root/tools/recording devices.
    if let Some(pool) = daemon_config.pool_config.as_mut() {
        pool.high_watermark = pool.high_watermark.min(args.max_devices / 2);
        pool.low_watermark = pool.low_watermark.min(pool.high_watermark);
    }
    daemon_config.device_ledger = Some(uvm_ublk_daemon::DeviceLedgerConfig {
        root: device_root,
        owner: service.device_session_owner().to_owned(),
        max_devices: args.max_devices,
    });
    agentenv::sandbox::UblkDeviceManager::init_global(Some(daemon_config)).await;
    ensure!(
        agentenv::sandbox::UblkDeviceManager::global().is_available(),
        "ublk daemon unavailable"
    );
    let incoming = Box::pin(futures::stream::unfold(listener, |listener| async {
        let connection = listener.accept().await.map(|(stream, _)| stream);
        Some((connection, listener))
    }));
    let mut terminate = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())?;
    let mut interrupt = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::interrupt())?;
    let shutdown_service = service.clone();
    let server_result = tonic::transport::Server::builder()
        .add_service(ExecutorServer::new(service.clone()))
        .serve_with_incoming_shutdown(incoming, async move {
            tokio::select! { _=terminate.recv()=>{}, _=interrupt.recv()=>{} }
            shutdown_service.begin_shutdown();
        })
        .await;
    service.shutdown().await?;
    agentenv::sandbox::UblkDeviceManager::global()
        .shutdown_daemon()
        .await?;
    service.complete_device_session().await?;
    std::fs::remove_file(args.socket)?;
    server_result?;
    Ok(())
}

// Complete the journal and endpoint ownership barriers before any daemon effects.
fn claim_executor(
    config: ExecutorConfig,
    socket: &Path,
) -> Result<(EmbeddedExecutor, tokio::net::UnixListener)> {
    let service = EmbeddedExecutor::open(config)?;
    let parent = socket
        .parent()
        .ok_or_else(|| anyhow::anyhow!("socket parent required"))?;
    std::fs::create_dir_all(parent)?;
    std::fs::set_permissions(parent, std::fs::Permissions::from_mode(0o700))?;
    // Never unlink a pre-existing endpoint, even if connect fails.
    let listener = tokio::net::UnixListener::bind(socket)?;
    std::fs::set_permissions(socket, std::fs::Permissions::from_mode(0o600))?;
    // Prewarm effects can occur before the first Actor; retain them on failure.
    service.begin_device_session()?;
    Ok((service, listener))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config(root: &Path) -> ExecutorConfig {
        ExecutorConfig {
            pod_uid: "pod".into(),
            epoch: 1,
            root: root.to_path_buf(),
            max_actors: 1,
            max_devices: 64,
            max_vcpus: 1,
            max_memory_mib: 256,
            cgroup_root: root.join("cgroups"),
        }
    }

    #[tokio::test]
    async fn endpoint_conflict_does_not_claim_device_session() -> Result<()> {
        let root = tempfile::tempdir()?;
        let socket = root.path().join("executor.sock");
        let listener = tokio::net::UnixListener::bind(&socket)?;
        let journal = root.path().join("journal");
        assert!(claim_executor(config(&journal), &socket).is_err());
        // A different journal cannot displace the owner or touch device services.
        assert!(tokio::net::UnixStream::connect(&socket).await.is_ok());
        EmbeddedExecutor::open(config(&journal))?;
        drop(listener);
        Ok(())
    }

    #[tokio::test]
    async fn uncertain_device_session_rejects_before_binding() -> Result<()> {
        let root = tempfile::tempdir()?;
        let journal = root.path().join("journal");
        let socket = root.path().join("executor.sock");
        let (executor, listener) = claim_executor(config(&journal), &socket)?;
        drop(listener);
        drop(executor);
        std::fs::remove_file(&socket)?;
        assert!(claim_executor(config(&journal), &socket).is_err());
        assert!(!socket.exists());
        Ok(())
    }
}
