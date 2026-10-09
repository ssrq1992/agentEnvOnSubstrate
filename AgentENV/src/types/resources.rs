use serde::{Deserialize, Serialize};

pub fn bytes_to_mib_ceil(bytes: u64) -> u32 {
    bytes.div_ceil(1 << 20).try_into().unwrap_or(u32::MAX)
}

#[derive(Clone, Debug, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct SandboxResources {
    pub cpu_count: u32,
    pub memory_mib: u32,
    pub disk_size_mib: u32,
}

impl Default for SandboxResources {
    fn default() -> Self {
        Self {
            cpu_count: 1,
            memory_mib: 128,
            disk_size_mib: 1024,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::bytes_to_mib_ceil;

    #[test]
    fn bytes_to_mib_ceil_rounds_up() {
        assert_eq!(bytes_to_mib_ceil(0), 0);
        assert_eq!(bytes_to_mib_ceil(1), 1);
        assert_eq!(bytes_to_mib_ceil(1 << 20), 1);
        assert_eq!(bytes_to_mib_ceil((1 << 20) + 1), 2);
    }
}
