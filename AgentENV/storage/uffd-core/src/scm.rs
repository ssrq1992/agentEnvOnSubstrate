use std::os::fd::{AsRawFd, FromRawFd, RawFd};

use async_trait::async_trait;
use nix::sys::socket::{ControlMessageOwned, MsgFlags};
use tokio::net::UnixStream;

/// Receive a single file descriptor over a Unix domain socket via SCM_RIGHTS.
///
/// From the uffd handler's perspective we only need to receive (not send)
/// and we only expect a single fd from the Firecracker process.
#[async_trait]
pub(crate) trait UnixRecvFdExt {
    /// Receive a message with an optional fd from a Unix domain socket.
    ///
    /// Returns the number of bytes received and an optional file descriptor.
    async fn recv_with_fd(&self, buf: &mut [u8])
        -> std::io::Result<(usize, Option<std::fs::File>)>;
}

#[async_trait]
impl UnixRecvFdExt for UnixStream {
    async fn recv_with_fd(
        &self,
        buf: &mut [u8],
    ) -> std::io::Result<(usize, Option<std::fs::File>)> {
        let mut iovs = [std::io::IoSliceMut::new(buf)];
        let mut cmsg_buf = nix::cmsg_space!([RawFd; 1]);
        let recv_msg = loop {
            self.readable().await?;
            match nix::sys::socket::recvmsg::<'_, '_, '_, ()>(
                self.as_raw_fd(),
                &mut iovs,
                Some(cmsg_buf.as_mut_slice()),
                MsgFlags::empty(),
            ) {
                Ok(msg) => break msg,
                Err(err) if err == nix::errno::Errno::EWOULDBLOCK => { /* spurious wakeup */ }
                Err(err) => return Err(err.into()),
            }
        };
        let fds = recv_msg
            .cmsgs()?
            .filter_map(|cmsg_buf| match cmsg_buf {
                ControlMessageOwned::ScmRights(fds) => Some(fds),
                _ => None,
            })
            .flatten()
            .collect::<Vec<_>>();
        let recv_fd_num = fds.len();
        if recv_fd_num >= 2 {
            for fd in fds {
                let _ = nix::unistd::close(fd);
            }
            return Err(std::io::Error::other(format!(
                "recv_with_fd got too many fds: {recv_fd_num}"
            )));
        }
        // SAFETY: the fd comes from a Unix socket SCM_RIGHTS message.
        let recv_file = fds
            .first()
            .map(|fd| unsafe { std::fs::File::from_raw_fd(*fd) });
        drop(fds);
        Ok((recv_msg.bytes, recv_file))
    }
}
