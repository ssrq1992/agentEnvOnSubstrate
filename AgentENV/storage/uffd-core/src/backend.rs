use anyhow::Result;
use async_trait::async_trait;
use bytes::Bytes;
use dashmap::DashMap;
use std::sync::Arc;
use uuid::Uuid;

use storage_util::CompactWriter;

use crate::handler::{GuestRegionUffdMapping, PageState};

/// Backend trait for reading pages from a memory snapshot image
/// and creating incremental snapshots.
///
/// Different image formats (overlaybd, uvm-diff, etc.) implement this trait.
/// The uffd handler uses it via trait object (`Box<dyn MemoryImageBackend>`)
/// so a single handler collection can manage multiple formats.
#[async_trait]
pub trait MemoryImageBackend: Send + Sync {
    /// Page size in bytes, typically 4096.
    fn pagesize(&self) -> usize;

    /// Read a single page at the given offset within the snapshot image.
    ///
    /// - `offset`: byte offset within the snapshot memory image (MMIO holes
    ///   are collapsed, so region offsets are contiguous). The caller guarantees
    ///   page-alignment.
    /// - Returns `None` when the page is a hole; the handler will fill a zero page.
    async fn read_page_at_offset(&self, offset: u64) -> Result<Option<Bytes>>;

    /// Create an incremental snapshot from the dirty-page state tracked by
    /// the uffd handler.
    ///
    /// The default implementation returns an error. Backends that support
    /// snapshotting should override this method.
    ///
    /// - `peer_pid`: Firecracker process PID (for `process_vm_readv`).
    /// - `mem_regions`: FC guest memory region mappings.
    /// - `dirty_pages`: per-page state tracked by the uffd handler
    ///   (host virtual page offset → `PageState`).
    /// - `layer_dir`: destination directory for the new snapshot layer.
    /// - `writer`: destination writer for the snapshot data.
    async fn snapshot(
        &self,
        _peer_pid: i32,
        _mem_regions: &[GuestRegionUffdMapping],
        _dirty_pages: &DashMap<u64, PageState>,
        _layer_dir: &str,
        _writer: Arc<dyn CompactWriter>,
    ) -> Result<Uuid> {
        anyhow::bail!("snapshot not implemented for this backend")
    }
}
