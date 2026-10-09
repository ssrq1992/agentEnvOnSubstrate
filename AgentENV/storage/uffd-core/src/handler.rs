use anyhow::{anyhow, bail, Context, Result};
use bytes::Bytes;
use dashmap::DashMap;
use serde::Deserialize;
use std::os::fd::{FromRawFd, IntoRawFd};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use tokio::io::unix::AsyncFd;
use tokio::net::{unix::pid_t, UnixListener, UnixStream};
use tokio::sync::SetOnce;
use tokio::task::JoinSet;
use userfaultfd::{Event, Uffd};
use uuid::Uuid;

use storage_util::CompactWriter;

use crate::backend::MemoryImageBackend;
use crate::scm::UnixRecvFdExt;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum PageState {
    /// The page has been accessed by FC. We assume it is dirty and should be
    /// included in the memory snapshot.
    Accessed,
    /// The page has been removed (due to virtio-balloon). When the next page
    /// fault occurs on this page, the handler returns a zero page. During
    /// snapshot, its content is not dumped.
    Removed,
}

/// Memory region mapping received from Firecracker via the uffd handshake.
#[derive(Clone, Deserialize)]
pub struct GuestRegionUffdMapping {
    /// Base host virtual address where the guest memory contents for this
    /// region should be copied/populated.
    pub base_host_virt_addr: u64,
    /// Region size in bytes.
    pub size: usize,
    /// Offset in the snapshot memory image where this region's data starts.
    /// This is NOT the guest physical address — MMIO holes between regions
    /// are collapsed, so region offsets are contiguous.
    pub offset: u64,
    /// The configured page size for this memory region.
    pub page_size: usize,
}

impl std::fmt::Debug for GuestRegionUffdMapping {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("GuestRegionUffdMapping")
            .field(
                "base_host_virt_addr",
                &format_args!("{:#x}", self.base_host_virt_addr),
            )
            .field("size", &format_args!("{:#x}", self.size))
            .field("offset", &format_args!("{:#x}", self.offset))
            .field("page_size", &self.page_size)
            .finish()
    }
}

/// Userfaultfd handler for a single Firecracker VM instance.
///
/// Manages the uffd event loop, page fault resolution, and dirty page tracking.
/// The actual page data source is abstracted via [`MemoryImageBackend`].
pub struct UffdHandle {
    uds_path: PathBuf,
    image: Box<dyn MemoryImageBackend>,
    inner: SetOnce<Result<UffdHandleInner>>,
    bg_tasks: Mutex<Vec<tokio::task::AbortHandle>>,
    /// Per-page state indexed by host virtual page offset.
    ///
    /// Firecracker's uffd does not implement UFFDIO_REGISTER_MODE_WP, so
    /// any accessed page is eagerly treated as dirty.
    page_state_by_host_virtual_pgoff: DashMap<u64, PageState>,
}

struct UffdHandleInner {
    mem_regions: Vec<GuestRegionUffdMapping>,
    uffd: AsyncFd<Uffd>,
    #[allow(unused)]
    peer_pid: pid_t,
}

/// Receive memory mappings and userfaultfd from a Firecracker process.
///
/// Firecracker creates the uffd in non-blocking, missing-fault mode. The
/// mappings describe guest memory regions; the uffd fd is sent via SCM_RIGHTS.
async fn get_mappings_and_uffd(stream: &UnixStream) -> Result<(Vec<GuestRegionUffdMapping>, Uffd)> {
    // 1KB is enough. For firecracker, typically there is only one or two GuestRegionUffdMappings.
    let mut message_buf = vec![0u8; 1024];
    let mut f = None;
    // Retry for possible empty messages, similar to Firecracker source code.
    for _ in 0..5 {
        match stream.recv_with_fd(&mut message_buf[..]).await {
            Ok((bytes_read, Some(file))) => {
                message_buf.resize(bytes_read, 0);
                f = Some(file);
                break;
            }
            Ok((_, None)) => { /* Retry */ }
            Err(err) => bail!("read uffd from uds failed: {err:?}"),
        }
    }
    let Some(uffd) = f else {
        bail!("cannot get uffd from uds");
    };
    // SAFETY: the uffd fd is received from the Unix domain socket.
    let uffd = unsafe { Uffd::from_raw_fd(uffd.into_raw_fd()) };

    let body = str::from_utf8(&message_buf).map_err(|_| {
        anyhow!("Received body is not a utf-8 valid string. Raw bytes received: {message_buf:#?}")
    })?;
    let mappings = serde_json::from_str::<Vec<GuestRegionUffdMapping>>(body).map_err(|err| {
        anyhow!("Cannot deserialize memory mappings: {err:?}. Received body: {body}")
    })?;
    Ok((mappings, uffd))
}

impl UffdHandle {
    /// Create a new uffd handler.
    ///
    /// Binds a Unix listener at `uds_path` and spawns a background task that
    /// waits for a Firecracker connection, receives the uffd fd and memory
    /// mappings, then starts the page fault event loop.
    pub fn new(image: Box<dyn MemoryImageBackend>, uds_path: PathBuf) -> Result<Arc<Self>> {
        let lis = UnixListener::bind(&uds_path).context("bind uds in UffdHandle")?;
        let this = Arc::new(UffdHandle {
            uds_path,
            image,
            bg_tasks: Mutex::new(vec![]),
            inner: SetOnce::new(),
            page_state_by_host_virtual_pgoff: DashMap::new(),
        });

        let handle = tokio::spawn({
            let this = this.clone();
            async move {
                let inner = this.sync_with_fc(lis).await;
                tracing::debug!(
                    uds_path=?this.uds_path,
                    mappings=?inner.as_ref().map(|h| &h.mem_regions),
                    "already synced with fc"
                );
                if this.inner.set(inner).is_err() {
                    tracing::error!("UffdHandle inner has already been set");
                }
                this.start();
            }
        });
        this.bg_tasks.lock().unwrap().push(handle.abort_handle());
        Ok(this)
    }

    /// Returns the Unix socket path this handler is listening on.
    pub fn uds_path(&self) -> &PathBuf {
        &self.uds_path
    }

    /// Returns a reference to the dirty page tracking map.
    ///
    /// Callers can use this to read page state for snapshotting.
    pub fn dirty_pages(&self) -> &DashMap<u64, PageState> {
        &self.page_state_by_host_virtual_pgoff
    }

    /// Returns the memory image backend.
    pub fn image(&self) -> &dyn MemoryImageBackend {
        self.image.as_ref()
    }

    async fn sync_with_fc(&self, lis: UnixListener) -> Result<UffdHandleInner> {
        let (stream, _) = lis.accept().await?;
        let peer_pid = stream
            .peer_cred()
            .context("get peer cred")?
            .pid()
            .ok_or_else(|| anyhow!("peer pid is None"))?;
        let (mappings, uffd) = get_mappings_and_uffd(&stream).await?;
        let img_page_size = self.image.pagesize();
        for (i, region) in mappings.iter().enumerate() {
            if region.page_size != img_page_size {
                bail!(
                    "region {i} page_size ({:#x}) does not match image page_size ({img_page_size:#x})",
                    region.page_size
                );
            }
        }
        Ok(UffdHandleInner {
            mem_regions: mappings,
            uffd: AsyncFd::new(uffd).context("create tokio AsyncFd from uffd")?,
            peer_pid,
        })
    }

    fn inner(&self) -> Result<&UffdHandleInner> {
        self.inner
            .get()
            .ok_or_else(|| anyhow!("UffdHandleInner has not been init"))?
            .as_ref()
            .map_err(|err| anyhow!("init UffdHandleInner failed: {err}"))
    }

    /// Start the page-fault event loop in the background. Non-blocking.
    fn start(self: &Arc<Self>) {
        let this = self.clone();
        let handle = tokio::spawn(async move {
            let res = this.serve_pf().await;
            match &res {
                Ok(()) => {
                    tracing::info!(uds_path=?this.uds_path, "uffd handler has finished event loop")
                }
                Err(err) => {
                    tracing::error!(?err, uds_path=?this.uds_path, "uffd handler event loop failed")
                }
            }
        });
        self.bg_tasks.lock().unwrap().push(handle.abort_handle());
    }

    async fn serve_pf(self: &Arc<Self>) -> Result<()> {
        let inner = self.inner()?;
        let mut handlers = JoinSet::new();
        loop {
            let mut guard = inner
                .uffd
                .readable()
                .await
                .context("wait for uffd events")?;
            // NOTE: Event is !Send (contains a raw pointer). We extract owned
            // data and drop(event) before any .await so the future stays Send.
            let Some(event) = guard.get_inner().read_event().context("read uffd event")? else {
                guard.clear_ready();
                continue;
            };
            match event {
                Event::Pagefault { addr, .. } => {
                    let addr = addr as u64;
                    drop(event);
                    let this = self.clone();
                    handlers.spawn(async move {
                        if let Err(err) = this.handle_page_fault(addr).await {
                            tracing::error!(?err, addr = ?addr, "Failed to handle page fault");
                        }
                    });
                }
                Event::Remove { start, end } => {
                    // When guest enables virtio-balloon, it calls MADV_DONTNEED
                    // or MADV_REMOVE. Track these pages so the next page fault
                    // returns a zero page, and snapshot emits ZERO instead of
                    // PRESENT.
                    //
                    // Do NOT drain handlers here. Each pending Remove event
                    // increments the kernel's mmap_changing counter; only reading
                    // (consuming) the event decrements it. If we block here
                    // waiting for pagefault handlers while more Remove events sit
                    // unread, mmap_changing stays >0 and UFFDIO_COPY returns
                    // EAGAIN forever — a deadlock.
                    self.record_remove_range(start as u64, end as u64);
                }
                event => {
                    return Err(anyhow!("UffdHandle recv unsupported event: {event:?}"));
                }
            }
        }
    }

    async fn handle_page_fault(&self, fault_addr: u64) -> Result<()> {
        let page_size = self.image.pagesize() as u64;
        let fault_addr = fault_addr & !(page_size - 1);
        let host_virtual_pgoff = fault_addr / page_size;
        let inner = self.inner()?;
        let prev_state = self
            .page_state_by_host_virtual_pgoff
            .insert(host_virtual_pgoff, PageState::Accessed);

        if prev_state == Some(PageState::Removed) {
            return self.zeropage(fault_addr).await;
        }

        let image_offset = inner.hva_to_image_offset(fault_addr)?;

        let Some(data) = self
            .image
            .read_page_at_offset(image_offset)
            .await
            .with_context(|| format!("read page data at image_offset {image_offset:#x}"))?
        else {
            return self.zeropage(fault_addr).await;
        };
        self.copy(fault_addr, data).await
    }

    /// Record page removals synchronously in the event loop.
    ///
    /// This MUST NOT be spawned to a background task. The uffd event stream is
    /// ordered: REMOVE then PAGEFAULT for the same page means the guest freed
    /// and then re-accessed. If we process REMOVE asynchronously, the page
    /// fault handler may insert `Accessed` first, then the delayed REMOVE
    /// overwrites it with `Removed` — corrupting snapshot state.
    fn record_remove_range(&self, start: u64, end: u64) {
        if end <= start {
            tracing::warn!(start, end, "ignore invalid uffd remove range");
            return;
        }

        let page_size = self.image.pagesize() as u64;
        let aligned_start = start & !(page_size - 1);
        let aligned_end = end.next_multiple_of(page_size);

        for hva in (aligned_start..aligned_end).step_by(page_size as usize) {
            let pgoff = hva / page_size;
            self.page_state_by_host_virtual_pgoff
                .insert(pgoff, PageState::Removed);
        }
    }

    /// Issue UFFDIO_ZEROPAGE ioctl.
    ///
    /// `hva` must be aligned at the page boundary.
    async fn zeropage(&self, hva: u64) -> Result<()> {
        let uffd = self.inner()?.uffd.get_ref();
        let page_size = self.image.pagesize();
        loop {
            match unsafe { uffd.zeropage(hva as _, page_size, true) } {
                Ok(size) if size == page_size => {
                    return Ok(());
                }
                Ok(size) => {
                    tracing::warn!(
                        size,
                        hva = %format_args!("{hva:#x}"),
                        "uffd zeropage succeeded but with only partial data"
                    );
                }
                Err(userfaultfd::Error::ZeropageFailed(errno)) if errno as i32 == libc::EAGAIN => {}
                Err(userfaultfd::Error::ZeropageFailed(errno)) if errno as i32 == libc::EEXIST => {
                    return Ok(());
                }
                Err(err) => {
                    bail!("zeropage at {hva:#x} failed: {err:?}")
                }
            }
            tokio::task::yield_now().await;
        }
    }

    /// Issue UFFDIO_COPY.
    ///
    /// `hva` must be aligned at the page boundary. `data` must be exactly one
    /// page in size.
    async fn copy(&self, hva: u64, data: Bytes) -> Result<()> {
        let uffd = self.inner()?.uffd.get_ref();
        let buf = data.as_ref();
        loop {
            match unsafe { uffd.copy(buf.as_ptr() as _, hva as _, buf.len(), true) } {
                Ok(size) if size != buf.len() => {
                    tracing::warn!(
                        buf_ptr = %format_args!("{:#x}", buf.as_ptr() as u64),
                        copy_size = size,
                        buf_len = buf.len(),
                        hva = %format_args!("{hva:#x}"),
                        "uffd succeeded but with only partial data"
                    );
                }
                Err(userfaultfd::Error::PartiallyCopied(size)) => {
                    if size >= buf.len() {
                        // This is weird, when get PartiallyCopied, typically the size should <
                        // buf.len(). When size > buf.len(), we still retry as a conservative
                        // defense.
                        tracing::warn!("uffd copy returned PartiallyCopied but copied all");
                    }
                }
                Ok(_) => {
                    return Ok(());
                }
                Err(userfaultfd::Error::CopyFailed(errno)) if errno as i32 == libc::EAGAIN => {}
                Err(userfaultfd::Error::CopyFailed(errno)) if errno as i32 == libc::EEXIST => {
                    return Ok(());
                }
                Err(err) => {
                    bail!("uffd copy failed: {err:?}");
                }
            }
            tokio::task::yield_now().await;
        }
    }

    /// Create an incremental memory snapshot.
    ///
    /// Delegates to [`MemoryImageBackend::snapshot`], passing the peer PID,
    /// memory region mappings, and dirty page state tracked by this handler.
    pub async fn snapshot(&self, layer_dir: &str, writer: Arc<dyn CompactWriter>) -> Result<Uuid> {
        let inner = self.inner()?;
        self.image
            .snapshot(
                inner.peer_pid,
                &inner.mem_regions,
                &self.page_state_by_host_virtual_pgoff,
                layer_dir,
                writer,
            )
            .await
    }

    /// Stop the handler and all of its background tasks.
    pub fn stop(&self) {
        let bg_tasks =
            std::mem::take::<Vec<tokio::task::AbortHandle>>(&mut self.bg_tasks.lock().unwrap());
        for handle in bg_tasks.iter() {
            handle.abort();
        }
    }
}

impl Drop for UffdHandle {
    fn drop(&mut self) {
        self.stop();
    }
}

impl UffdHandleInner {
    /// Find which region contains the given host virtual address.
    fn find_region_by_hva(&self, hva: u64) -> Option<&GuestRegionUffdMapping> {
        self.mem_regions.iter().find(|region| {
            hva >= region.base_host_virt_addr
                && hva < region.base_host_virt_addr + region.size as u64
        })
    }

    /// Convert a host virtual address (HVA) to image offset.
    ///
    /// The image offset is a byte offset within the snapshot memory image,
    /// with MMIO holes collapsed. It is stable across restored instances,
    /// while HVA may change.
    fn hva_to_image_offset(&self, hva: u64) -> Result<u64> {
        let region = self
            .find_region_by_hva(hva)
            .with_context(|| format!("find region for hva {:#x}", hva))?;
        let region_offset = hva - region.base_host_virt_addr;
        Ok(region.offset + region_offset)
    }
}
