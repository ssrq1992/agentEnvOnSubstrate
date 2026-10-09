//! Portable FULL snapshots. Sealed block images are materialized through ublk
//! so this format has no dependency on the source Worker's layer cache.
use std::collections::BTreeMap;
use std::fs::File;
use std::io::{Read, Seek, SeekFrom, Write};
use std::path::{Component, Path, PathBuf};

use anyhow::{ensure, Context, Result};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::cfg::ConfigManager;
use crate::sandbox::{FirecrackerSnapshotConfig, UblkCreateSpec, UblkDeviceManager};

pub const MANIFEST: &str = "agentenv-manifest.v1.json";

#[derive(Serialize, Deserialize)]
pub struct Manifest {
    pub format: String,
    pub compatibility_domain: String,
    pub network_signature: String,
    pub vcpus: u32,
    pub memory_mib: u64,
    pub assets: BTreeMap<String, String>,
    pub files: BTreeMap<String, String>,
    pub tools_image: String,
    pub config: FirecrackerSnapshotConfig,
}

pub fn hash_file(path: &Path) -> Result<String> {
    let mut file = File::open(path)?;
    let mut hash = Sha256::new();
    let mut buffer = vec![0u8; 1024 * 1024];
    loop {
        let n = file.read(&mut buffer)?;
        if n == 0 {
            break;
        }
        hash.update(&buffer[..n]);
    }
    Ok(hex::encode(hash.finalize()))
}

async fn export_disk(config: &Path, size: u64, output: &Path, name: &str) -> Result<PathBuf> {
    ensure!(size > 0, "snapshot disk size is missing");
    let global = &ConfigManager::global_config()
        .ublk
        .overlaybd
        .global_config_path;
    let device = UblkDeviceManager::global()
        .get_or_create_shared_mem(
            &UblkCreateSpec::Overlaybd {
                image_config: config.to_path_buf(),
                global_config: global.clone(),
            },
            size,
        )
        .await?;
    let source = device.device_path().to_path_buf();
    let raw = output.join(format!("{name}.raw"));
    let copy_to = raw.clone();
    let copied = tokio::task::spawn_blocking(move || -> Result<()> {
        let file = File::open(source)?;
        let mut dest = File::create(copy_to)?;
        ensure!(
            std::io::copy(&mut file.take(size), &mut dest)? == size,
            "short block image read"
        );
        dest.sync_all()?;
        Ok(())
    })
    .await
    .context("block image export task")?;
    let released = device.release().await;
    copied?;
    released?;
    let layer_name = format!("{name}.layer");
    overlaybd::tools::package_raw_as_overlaybd(&raw, &output.join(&layer_name)).await?;
    std::fs::remove_file(raw)?;
    let config_name = format!("{name}.json");
    // The relative file is made absolute only in the restore working copy.
    let image = overlaybd::config::ImageConfig {
        lowers: vec![overlaybd::config::LayerConfig {
            file: layer_name,
            ..Default::default()
        }],
        ..Default::default()
    };
    std::fs::write(output.join(&config_name), serde_json::to_vec(&image)?)?;
    Ok(PathBuf::from(config_name))
}

fn copy_tools_image(source: &Path, destination: &Path) -> Result<()> {
    let mut file = File::open(source)?;
    // SEEK_END reports capacity for both a regular image and a block device;
    // stat().len() is zero for ublk block nodes.
    let size = file.seek(SeekFrom::End(0))?;
    ensure!(size > 0, "tools image is empty");
    file.seek(SeekFrom::Start(0))?;
    let mut output = File::create(destination)?;
    ensure!(
        std::io::copy(&mut file.take(size), &mut output)? == size,
        "short tools image read"
    );
    output.sync_all()?;
    Ok(())
}

pub async fn export(
    mut manifest: Manifest,
    output: &Path,
    tools_source: PathBuf,
) -> Result<Vec<String>> {
    std::fs::create_dir_all(output)?;
    let tools_destination = output.join("tools.raw");
    tokio::task::spawn_blocking(move || copy_tools_image(&tools_source, &tools_destination))
        .await
        .context("tools image export task")??;
    manifest.tools_image = "tools.raw".into();
    std::fs::copy(&manifest.config.vm_state_path, output.join("vm-state.bin"))?;
    manifest.config.vm_state_path = "vm-state.bin".into();
    manifest.config.mem_overlaybd_config.image_config_path = export_disk(
        &manifest.config.mem_overlaybd_config.image_config_path,
        manifest.config.mem_virtual_size,
        output,
        "memory",
    )
    .await?;
    let rootfs = manifest
        .config
        .common
        .rootfs_image_config
        .as_mut()
        .context("missing rootfs")?;
    rootfs.image_config_path = export_disk(
        &rootfs.image_config_path,
        manifest
            .config
            .common
            .rootfs_virtual_size
            .context("missing rootfs size")?,
        output,
        "rootfs",
    )
    .await?;
    for (i, drive) in manifest.config.common.extra_drives.iter_mut().enumerate() {
        let file = export_disk(
            drive.image_config_path(),
            drive.virtual_size().context("missing drive size")?,
            output,
            &format!("drive-{i}"),
        )
        .await?;
        *drive = drive
            .with_image_config_path(file)
            .with_volume_snapshot_output_dir(None);
    }
    manifest.config.common.envd_access_token = None;
    manifest.config.common.attached_network = None;
    // Host paths and configuration are injected from verified runtime assets on restore.
    manifest.config.common.firecracker_binary = PathBuf::new();
    manifest.config.common.firecracker_work_base_dir = None;
    manifest.config.common.serial_output_base_dir = None;
    manifest.config.common.stdout_path = None;
    manifest.config.common.stderr_path = None;
    manifest.config.common.ublk_config = None;
    let mut files = vec![
        "vm-state.bin".to_string(),
        "tools.raw".into(),
        "memory.json".into(),
        "memory.layer".into(),
        "rootfs.json".into(),
        "rootfs.layer".into(),
    ];
    for i in 0..manifest.config.common.extra_drives.len() {
        files.push(format!("drive-{i}.json"));
        files.push(format!("drive-{i}.layer"));
    }
    for name in &files {
        manifest
            .files
            .insert(name.clone(), hash_file(&output.join(name))?);
    }
    let path = output.join(MANIFEST);
    let mut file = File::create(&path)?;
    file.write_all(&serde_json::to_vec(&manifest)?)?;
    file.sync_all()?;
    files.push(MANIFEST.into());
    Ok(files)
}

fn local_file(root: &Path, name: &Path) -> Result<PathBuf> {
    ensure!(
        name.components().count() == 1
            && matches!(name.components().next(), Some(Component::Normal(_))),
        "unsafe portable file path"
    );
    let path = root.join(name);
    ensure!(
        std::fs::symlink_metadata(&path)?.file_type().is_file(),
        "portable files must be regular files"
    );
    Ok(path)
}

pub fn load(source: &Path, working: &Path, expected_sha: &str) -> Result<Manifest> {
    let manifest_path = local_file(source, Path::new(MANIFEST))?;
    ensure!(
        hash_file(&manifest_path)? == expected_sha,
        "snapshot manifest digest mismatch"
    );
    let mut manifest: Manifest = serde_json::from_reader(File::open(manifest_path)?)?;
    ensure!(
        manifest.format == "agentenv-portable-full-v1",
        "unsupported portable snapshot format"
    );
    for (name, digest) in &manifest.files {
        ensure!(
            hash_file(&local_file(source, Path::new(name))?)? == *digest,
            "snapshot file digest mismatch: {name}"
        );
    }
    ensure!(
        manifest.tools_image == "tools.raw" && manifest.files.contains_key(&manifest.tools_image),
        "missing portable tools image"
    );
    manifest.config.common.portable_tools_drive =
        Some(local_file(source, Path::new(&manifest.tools_image))?);
    std::fs::create_dir_all(working)?;
    ensure!(
        !working.canonicalize()?.starts_with(source.canonicalize()?),
        "restore working directory overlaps immutable snapshot"
    );
    manifest.config.vm_state_path = local_file(source, &manifest.config.vm_state_path)?;
    fn hydrate(
        source: &Path,
        working: &Path,
        path: &mut PathBuf,
        files: &BTreeMap<String, String>,
    ) -> Result<()> {
        ensure!(
            files.contains_key(path.to_str().context("invalid snapshot filename")?),
            "unlisted image config"
        );
        let config: overlaybd::config::ImageConfig =
            serde_json::from_reader(File::open(local_file(source, path)?)?)?;
        ensure!(
            config.lowers.len() == 1,
            "portable image must contain one complete layer"
        );
        let layer = &config.lowers[0];
        ensure!(files.contains_key(&layer.file), "unlisted snapshot layer");
        // Reconstruct rather than retaining config fields that can contain
        // source-node paths or remote download/trace destinations.
        let config = overlaybd::config::ImageConfig {
            lowers: vec![overlaybd::config::LayerConfig {
                file: local_file(source, Path::new(&layer.file))?
                    .to_string_lossy()
                    .into_owned(),
                ..Default::default()
            }],
            ..Default::default()
        };
        let target = working.join(&*path);
        std::fs::write(&target, serde_json::to_vec(&config)?)?;
        *path = target;
        Ok(())
    }
    ensure!(
        manifest.files.contains_key(
            manifest
                .config
                .vm_state_path
                .file_name()
                .and_then(|v| v.to_str())
                .context("missing state filename")?
        ),
        "unlisted VM state"
    );
    hydrate(
        source,
        working,
        &mut manifest.config.mem_overlaybd_config.image_config_path,
        &manifest.files,
    )?;
    hydrate(
        source,
        working,
        &mut manifest
            .config
            .common
            .rootfs_image_config
            .as_mut()
            .context("missing rootfs")?
            .image_config_path,
        &manifest.files,
    )?;
    for drive in &mut manifest.config.common.extra_drives {
        let mut path = drive.image_config_path().to_path_buf();
        hydrate(source, working, &mut path, &manifest.files)?;
        *drive = drive.with_image_config_path(path);
    }
    Ok(manifest)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn portable_tools_copy_is_complete_and_nonempty() -> Result<()> {
        let root = tempfile::tempdir()?;
        let source = root.path().join("tools-source");
        let destination = root.path().join("tools.raw");
        std::fs::write(&source, b"immutable tools bytes")?;
        copy_tools_image(&source, &destination)?;
        assert_eq!(hash_file(&source)?, hash_file(&destination)?);
        std::fs::write(&source, b"")?;
        assert!(copy_tools_image(&source, &destination).is_err());
        Ok(())
    }

    #[test]
    fn reject_escape_and_symlink() -> Result<()> {
        let dir = tempfile::tempdir()?;
        std::fs::write(dir.path().join("valid"), b"data")?;
        assert!(local_file(dir.path(), Path::new("../valid")).is_err());
        assert!(local_file(dir.path(), Path::new("/etc/passwd")).is_err());
        std::os::unix::fs::symlink("valid", dir.path().join("link"))?;
        assert!(local_file(dir.path(), Path::new("link")).is_err());
        assert!(local_file(dir.path(), Path::new("valid")).is_ok());
        Ok(())
    }
}
