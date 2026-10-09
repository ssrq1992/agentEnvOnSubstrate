//! Durable ownership of kernel devices. Unknown creation/deletion retains budget.
use anyhow::{ensure, Context, Result};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::fs::{File, OpenOptions};
use std::io::Write;
use std::os::fd::AsRawFd;
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, OnceLock};

static GLOBAL: OnceLock<Arc<DeviceLedger>> = OnceLock::new();

#[derive(Clone, Serialize, Deserialize)]
struct Record {
    operation: u64,
    owner: String,
    image: PathBuf,
    device_id: Option<u32>,
}
struct State {
    next: u64,
    records: BTreeMap<u64, Record>,
    uncertain: bool,
}
pub struct DeviceLedger {
    root: PathBuf,
    owner: String,
    limit: usize,
    state: Mutex<State>,
    _lock: File,
}

impl DeviceLedger {
    pub fn open(root: &Path, owner: String, limit: usize) -> Result<Arc<Self>> {
        ensure!(
            root.is_absolute() && root.file_name().is_some() && !owner.is_empty() && limit > 0,
            "invalid device ledger configuration"
        );
        std::fs::create_dir_all(root)?;
        File::open(root.parent().context("device ledger parent required")?)?.sync_all()?;
        std::fs::set_permissions(root, std::fs::Permissions::from_mode(0o700))?;
        let lock = OpenOptions::new()
            .create(true)
            .mode(0o600)
            .truncate(false)
            .read(true)
            .write(true)
            .open(root.join("lock"))?;
        // Lock before inspecting records; another daemon must never share them.
        ensure!(
            unsafe { libc::flock(lock.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } == 0,
            "device ledger is owned by another daemon"
        );
        for entry in std::fs::read_dir(root)? {
            let entry = entry?;
            ensure!(
                entry.file_name() == "lock",
                "unreconciled device ledger entry {}; fence the old Worker/node before replacement",
                entry.path().display()
            );
        }
        Ok(Arc::new(Self {
            root: root.to_path_buf(),
            owner,
            limit,
            _lock: lock,
            state: Mutex::new(State {
                next: 1,
                records: BTreeMap::new(),
                uncertain: false,
            }),
        }))
    }
    fn persist(&self, record: &Record) -> Result<()> {
        let temp = self.root.join(format!("{}.pending", record.operation));
        let mut file = OpenOptions::new()
            .write(true)
            .mode(0o600)
            .create_new(true)
            .open(&temp)?;
        file.write_all(&serde_json::to_vec(record)?)?;
        file.sync_all()?;
        std::fs::rename(&temp, self.root.join(format!("{}.json", record.operation)))?;
        File::open(&self.root)?.sync_all()?;
        Ok(())
    }
    fn reserve(self: &Arc<Self>, image: &Path) -> Result<Reservation> {
        let mut state = self.state.lock().unwrap();
        ensure!(
            !state.uncertain,
            "device ledger has an unknown durable outcome"
        );
        ensure!(
            state.records.len() < self.limit,
            "Worker ublk device budget exhausted"
        );
        let operation = state.next;
        state.next = state
            .next
            .checked_add(1)
            .context("device operation counter exhausted")?;
        let record = Record {
            operation,
            owner: self.owner.clone(),
            image: image.to_path_buf(),
            device_id: None,
        };
        // Retain the budget even if fsync fails: the durable result is unknown.
        state.records.insert(operation, record.clone());
        if let Err(error) = self.persist(&record) {
            state.uncertain = true;
            return Err(error);
        }
        Ok(Reservation {
            ledger: Some(self.clone()),
            operation,
        })
    }
    fn allocated(&self, operation: u64, id: u32) -> Result<()> {
        let mut state = self.state.lock().unwrap();
        ensure!(
            !state.records.values().any(|r| r.device_id == Some(id)),
            "kernel device ID already owned"
        );
        let record = state
            .records
            .get_mut(&operation)
            .context("missing device reservation")?;
        ensure!(
            record.device_id.is_none(),
            "device reservation already allocated"
        );
        // Preserve the ID in memory if persistence fails so shutdown can find it.
        record.device_id = Some(id);
        let result = self.persist(record);
        if result.is_err() {
            state.uncertain = true;
        }
        result
    }
    fn released(&self, id: u32) -> Result<()> {
        let mut state = self.state.lock().unwrap();
        let operation = state
            .records
            .iter()
            .find_map(|(op, r)| (r.device_id == Some(id)).then_some(*op))
            .context("deleted device has no ownership record")?;
        let path = self.root.join(format!("{operation}.json"));
        match std::fs::remove_file(path) {
            Ok(()) => {}
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => {
                state.uncertain = true;
                return Err(error.into());
            }
        }
        if let Err(error) = File::open(&self.root).and_then(|directory| directory.sync_all()) {
            state.uncertain = true;
            return Err(error.into());
        }
        state.records.remove(&operation);
        Ok(())
    }
    pub fn ensure_empty(&self) -> Result<()> {
        let state = self.state.lock().unwrap();
        ensure!(
            !state.uncertain && state.records.is_empty(),
            "device ownership remains unconfirmed"
        );
        for entry in std::fs::read_dir(&self.root)? {
            ensure!(
                entry?.file_name() == "lock",
                "durable device ownership remains unconfirmed"
            );
        }
        Ok(())
    }
}

// Reservations deliberately have no Drop rollback. Dropping a future or handle
// cannot prove that ADD_DEV did not execute in the kernel.
pub(crate) struct Reservation {
    ledger: Option<Arc<DeviceLedger>>,
    operation: u64,
}
impl Reservation {
    pub(crate) fn allocated(&self, id: u32) -> Result<()> {
        match &self.ledger {
            Some(ledger) => ledger.allocated(self.operation, id),
            None => Ok(()),
        }
    }
}
pub fn install(root: &Path, owner: String, limit: usize) -> Result<()> {
    let ledger = DeviceLedger::open(root, owner, limit)?;
    GLOBAL
        .set(ledger)
        .map_err(|_| anyhow::anyhow!("device ledger already initialized"))
}
pub(crate) fn reserve(image: &Path) -> Result<Reservation> {
    match GLOBAL.get() {
        Some(ledger) => ledger.reserve(image),
        None => Ok(Reservation {
            ledger: None,
            operation: 0,
        }),
    }
}
pub(crate) fn released(id: u32) -> Result<()> {
    match GLOBAL.get() {
        Some(ledger) => ledger.released(id),
        None => Ok(()),
    }
}
pub(crate) fn ensure_empty() -> Result<()> {
    match GLOBAL.get() {
        Some(ledger) => ledger.ensure_empty(),
        None => Ok(()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn concurrent_reservations_obey_one_budget() -> Result<()> {
        let root = tempfile::tempdir()?;
        let ledger = DeviceLedger::open(root.path(), "instance".into(), 2)?;
        let jobs: Vec<_> = (0..16)
            .map(|_| {
                let ledger = ledger.clone();
                std::thread::spawn(move || ledger.reserve(Path::new("image")).is_ok())
            })
            .collect();
        let successes = jobs
            .into_iter()
            .map(|job| usize::from(job.join().unwrap()))
            .sum::<usize>();
        assert_eq!(successes, 2);
        assert_eq!(ledger.state.lock().unwrap().records.len(), 2);
        Ok(())
    }

    #[test]
    fn duplicate_device_id_never_overwrites_an_owner() -> Result<()> {
        let root = tempfile::tempdir()?;
        let ledger = DeviceLedger::open(root.path(), "instance".into(), 2)?;
        ledger.reserve(Path::new("first"))?.allocated(7)?;
        assert!(ledger.reserve(Path::new("second"))?.allocated(7).is_err());
        let record: Record = serde_json::from_slice(&std::fs::read(root.path().join("1.json"))?)?;
        assert_eq!(record.device_id, Some(7));
        assert_eq!(record.image, Path::new("first"));
        ledger.released(7)?;
        assert!(ledger.ensure_empty().is_err());
        Ok(())
    }

    #[test]
    fn unknown_creation_survives_restart_and_holds_budget() -> Result<()> {
        let root = tempfile::tempdir()?;
        let ledger = DeviceLedger::open(root.path(), "instance".into(), 1)?;
        drop(ledger.reserve(Path::new("image"))?);
        assert!(ledger.reserve(Path::new("another")).is_err());
        assert!(ledger.ensure_empty().is_err());
        drop(ledger);
        assert!(DeviceLedger::open(root.path(), "new".into(), 1).is_err());
        Ok(())
    }
    #[test]
    fn only_confirmed_deletion_releases_ownership() -> Result<()> {
        let root = tempfile::tempdir()?;
        let ledger = DeviceLedger::open(root.path(), "instance".into(), 1)?;
        let reservation = ledger.reserve(Path::new("image"))?;
        reservation.allocated(7)?;
        drop(reservation);
        assert!(ledger.released(8).is_err());
        assert!(ledger.reserve(Path::new("another")).is_err());
        ledger.released(7)?;
        ledger.ensure_empty()?;
        drop(ledger);
        DeviceLedger::open(root.path(), "new".into(), 1)?;
        Ok(())
    }
    #[test]
    fn concurrent_daemon_and_partial_write_are_rejected() -> Result<()> {
        let root = tempfile::tempdir()?;
        let ledger = DeviceLedger::open(root.path(), "instance".into(), 2)?;
        assert!(DeviceLedger::open(root.path(), "another".into(), 2).is_err());
        drop(ledger);
        std::fs::write(root.path().join("1.pending"), b"partial")?;
        assert!(DeviceLedger::open(root.path(), "another".into(), 2).is_err());
        Ok(())
    }
    #[test]
    fn persistence_failure_never_returns_capacity() -> Result<()> {
        let root = tempfile::tempdir()?;
        let ledger = DeviceLedger::open(root.path(), "instance".into(), 2)?;
        std::fs::create_dir(root.path().join("1.pending"))?;
        assert!(ledger.reserve(Path::new("image")).is_err());
        assert!(ledger.reserve(Path::new("another")).is_err());
        assert!(ledger.ensure_empty().is_err());
        Ok(())
    }
}
