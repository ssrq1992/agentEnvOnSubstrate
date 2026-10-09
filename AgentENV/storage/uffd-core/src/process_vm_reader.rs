use anyhow::{bail, Context, Result};
use async_trait::async_trait;
use bytes::Bytes;
use nix::sys::uio::{process_vm_readv, RemoteIoVec};
use nix::unistd::Pid;
use std::io::IoSliceMut;

use overlaybd::virtual_file::VirtualFile;

/// A single region for a scatter-read: a remote address paired with a local
/// destination buffer.
pub struct ScatterRegion<'a> {
    /// Virtual address of remote process
    pub va: u64,
    /// Local buffer to store the read data
    pub buf: &'a mut [u8],
}

/// Reads memory from a remote process (Firecracker VM) using `process_vm_readv`.
///
/// Implements [`VirtualFile`] so it can be passed directly to `compact_to`.
/// Only `read_at_into` is meaningful; other methods bail.
pub struct ProcessVmReader {
    pid: Pid,
}

impl ProcessVmReader {
    pub fn new(pid: Pid) -> Self {
        Self { pid }
    }

    /// Scatter-read multiple disjoint regions from the remote process in a
    /// single `process_vm_readv` syscall.
    ///
    /// On success returns the total number of bytes read.
    pub fn read_scatter(&self, regions: &mut [ScatterRegion<'_>]) -> Result<usize> {
        if regions.is_empty() {
            return Ok(0);
        }

        let remote_iovs: Vec<RemoteIoVec> = regions
            .iter()
            .map(|r| RemoteIoVec {
                base: r.va as usize,
                len: r.buf.len(),
            })
            .collect();

        let mut local_iovs: Vec<IoSliceMut<'_>> =
            regions.iter_mut().map(|r| IoSliceMut::new(r.buf)).collect();

        process_vm_readv(self.pid, &mut local_iovs, &remote_iovs)
            .context("process_vm_readv scatter")
    }
}

#[async_trait]
impl VirtualFile for ProcessVmReader {
    async fn read_at(&self, _offset: u64, _len: usize) -> Result<Bytes> {
        bail!("ProcessVmReader does not support read_at()")
    }

    /// `offset` here is a host virtual address (HVA), not a typical file offset.
    async fn read_at_into(&self, offset: u64, dst: &mut [u8]) -> Result<usize> {
        if dst.is_empty() {
            return Ok(0);
        }
        let region = ScatterRegion {
            va: offset,
            buf: dst,
        };
        self.read_scatter(&mut [region])
    }

    async fn write_at(&self, _offset: u64, _data: &[u8]) -> Result<usize> {
        bail!("ProcessVmReader does not support write_at()")
    }

    async fn size(&self) -> Result<u64> {
        bail!("ProcessVmReader does not support size()")
    }
}
