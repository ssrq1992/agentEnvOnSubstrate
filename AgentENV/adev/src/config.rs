use anyhow::{Context, Result};
use serde::Deserialize;
use std::path::{Path, PathBuf};

/// Subset of `config/deps_manifest.toml` that `adev` actually reads.
#[derive(Debug, Deserialize)]
pub struct Config {
    pub protoc: ProtocConfig,
}

#[derive(Debug, Deserialize)]
pub struct ProtocConfig {
    pub version: String,
    pub url: String,
}

/// Resolve a URL template by replacing `{key}` placeholders with values.
pub fn resolve_url(template: &str, vars: &[(&str, &str)]) -> String {
    let mut result = template.to_string();
    for (key, value) in vars {
        result = result.replace(&format!("{{{key}}}"), value);
    }
    result
}

/// Find the project root by walking up from the current directory looking for Cargo.toml
/// with [workspace] in it.
pub fn project_root() -> Result<PathBuf> {
    let mut dir = std::env::current_dir().context("failed to get current directory")?;
    loop {
        let candidate = dir.join("Cargo.toml");
        if candidate.exists() {
            let contents = std::fs::read_to_string(&candidate)?;
            if contents.contains("[workspace]") {
                return Ok(dir);
            }
        }
        if !dir.pop() {
            anyhow::bail!("could not find workspace root (no Cargo.toml with [workspace] found)");
        }
    }
}

/// Load config from config/deps_manifest.toml relative to project root, or from a custom path.
/// If `path` is None and you already have the project root, use `load_config_from_root` instead.
pub fn load_config(path: Option<&Path>) -> Result<Config> {
    let config_path = match path {
        Some(p) => p.to_path_buf(),
        None => project_root()?.join("config/deps_manifest.toml"),
    };
    let contents = std::fs::read_to_string(&config_path)
        .context(format!("reading {}", config_path.display()))?;
    let config: Config = toml::from_str(&contents).context("parsing config")?;
    Ok(config)
}

/// Load config when you already have the project root (avoids duplicate filesystem walk).
pub fn load_config_from_root(root: &Path) -> Result<Config> {
    load_config(Some(&root.join("config/deps_manifest.toml")))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_protoc_section() {
        let toml_str = r#"
[protoc]
version = "33.4"
url = "https://github.com/protocolbuffers/protobuf/releases/download/v{version}/protoc-{version}-{platform}.zip"
"#;
        let config: Config = toml::from_str(toml_str).unwrap();
        assert_eq!(config.protoc.version, "33.4");
        assert!(config.protoc.url.contains("{platform}"));
    }

    #[test]
    fn ignores_runtime_only_sections() {
        // Anything outside `[protoc]` belongs to server setup and is not
        // relevant to adev's codegen dependency installation.
        let toml_str = r#"
[firecracker]
version = "v1.12.1"
url = "https://example.com/{version}.tgz"
boot_args = "console=ttyS0"

[kernel]
version = "vmlinux-6.1.102"
url = "https://example.com/{version}/vmlinux.bin"

[user_image]
image = "docker.io/library/debian:bookworm-slim"

[ublk]
enabled = true

[protoc]
version = "33.4"
url = "https://example.com/protoc/{version}-{platform}.zip"
"#;
        let config: Config = toml::from_str(toml_str).unwrap();
        assert_eq!(config.protoc.version, "33.4");
    }

    #[test]
    fn requires_protoc_section() {
        let toml_str = r#"
[firecracker]
version = "v1.12.1"
url = "https://example.com/{version}.tgz"
"#;
        assert!(toml::from_str::<Config>(toml_str).is_err());
    }

    #[test]
    fn resolves_url_template() {
        let url = resolve_url(
            "https://example.com/{version}/file-{arch}.tgz",
            &[("version", "v1.0"), ("arch", "x86_64")],
        );
        assert_eq!(url, "https://example.com/v1.0/file-x86_64.tgz");
    }
}
