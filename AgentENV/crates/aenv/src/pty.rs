use anyhow::Result;
use crossterm::terminal::{disable_raw_mode, enable_raw_mode};

pub struct RawModeGuard;

impl RawModeGuard {
    pub fn enter() -> Result<Self> {
        enable_raw_mode()?;
        Ok(Self)
    }
}

impl Drop for RawModeGuard {
    fn drop(&mut self) {
        let _ = disable_raw_mode();
    }
}

pub fn terminal_size() -> (u32, u32) {
    crossterm::terminal::size()
        .map(|(c, r)| (c as u32, r as u32))
        .unwrap_or((80, 24))
}
