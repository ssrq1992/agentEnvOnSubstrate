use anyhow::{bail, Context, Result};
use directories::ProjectDirs;
use serde::{Deserialize, Serialize};
use std::fs;
use std::io;
use std::path::PathBuf;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Credentials {
    pub url: String,
    pub api_key: String,
}

fn credentials_path() -> Result<PathBuf> {
    let dirs = ProjectDirs::from("", "", "aenv").context("could not determine config directory")?;
    Ok(dirs.config_dir().join("credentials"))
}

pub fn load() -> Result<Credentials> {
    let path = credentials_path()?;
    let text = match fs::read_to_string(&path) {
        Ok(t) => t,
        Err(e) if e.kind() == io::ErrorKind::NotFound => bail!(
            "not authenticated — run `aenv auth` first (expected credentials at {})",
            path.display()
        ),
        Err(e) => return Err(e).with_context(|| format!("reading {}", path.display())),
    };
    let creds: Credentials =
        toml::from_str(&text).with_context(|| format!("parsing {}", path.display()))?;
    Ok(creds)
}

pub fn save(creds: &Credentials) -> Result<()> {
    let path = credentials_path()?;
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).with_context(|| format!("creating {}", parent.display()))?;
    }
    let text = toml::to_string(creds)?;
    fs::write(&path, text).with_context(|| format!("writing {}", path.display()))?;
    restrict_permissions(&path)?;
    Ok(())
}

#[cfg(unix)]
fn restrict_permissions(path: &std::path::Path) -> Result<()> {
    use std::os::unix::fs::PermissionsExt;
    let mut perms = fs::metadata(path)?.permissions();
    perms.set_mode(0o600);
    fs::set_permissions(path, perms)?;
    Ok(())
}

#[cfg(not(unix))]
fn restrict_permissions(_path: &std::path::Path) -> Result<()> {
    Ok(())
}
