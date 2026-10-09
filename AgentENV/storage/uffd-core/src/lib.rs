mod backend;
mod handler;
#[cfg(feature = "overlaybd")]
pub mod overlaybd;
#[cfg(feature = "overlaybd")]
pub mod process_vm_reader;
mod scm;

pub use backend::MemoryImageBackend;
pub use handler::{GuestRegionUffdMapping, PageState, UffdHandle};
