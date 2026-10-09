use anyhow::{Context, Result};
use clap::Parser;
use std::path::PathBuf;

use overlaybd::tools::package_ext4_as_overlaybd;

#[derive(Debug, Parser)]
struct Args {
    #[arg(long)]
    source: PathBuf,
    #[arg(long)]
    output: PathBuf,
    #[arg(long)]
    index: Option<PathBuf>,
    #[arg(long)]
    chunk_size: Option<usize>,
    #[arg(long, default_value_t = false)]
    force: bool,
}

#[tokio::main]
async fn main() -> Result<()> {
    let args = Args::parse();
    let index = args
        .index
        .clone()
        .unwrap_or_else(|| args.output.with_extension("index"));

    if !args.force && args.output.exists() && index.exists() {
        let src_mtime = std::fs::metadata(&args.source)
            .with_context(|| format!("stat source rootfs failed: {}", args.source.display()))?
            .modified()?;
        let out_mtime = std::fs::metadata(&args.output)
            .with_context(|| format!("stat output lower failed: {}", args.output.display()))?
            .modified()?;
        let idx_mtime = std::fs::metadata(&index)
            .with_context(|| format!("stat output index failed: {}", index.display()))?
            .modified()?;
        if out_mtime >= src_mtime && idx_mtime >= src_mtime {
            println!("already up to date: {}", args.output.display());
            return Ok(());
        }
    }

    package_ext4_as_overlaybd(&args.source, &args.output, &index, args.chunk_size).await?;
    println!("generated overlaybd lower: {}", args.output.display());
    println!("generated overlaybd index: {}", index.display());
    Ok(())
}
