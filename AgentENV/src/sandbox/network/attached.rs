//! Network attachment supplied by the embedding Worker. This module owns only
//! policy/proxy state, never namespace creation, address allocation or routing.
use std::fs::File;
use std::net::Ipv4Addr;
use std::os::fd::AsFd;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};

use anyhow::{ensure, Context, Result};
use nix::sched::{setns, CloneFlags};

use super::egress_proxy::EgressProxy;
use super::policy::{initialize_namespace_egress_chain, set_namespace_egress_policy};
use super::SandboxNetworkPolicy;

#[derive(Clone, Debug)]
pub struct AttachedNetwork {
    pub netns_path: PathBuf,
    pub guest_ip: Ipv4Addr,
    pub gateway_ip: Ipv4Addr,
    pub netmask: Ipv4Addr,
    pub dns_ip: Ipv4Addr,
    pub interaction_ip: Ipv4Addr,
    pub guest_mac: String,
    proxy: Arc<EgressProxy>,
    initialized: Arc<Mutex<bool>>,
    denied_cidrs: Vec<String>,
}

impl AttachedNetwork {
    pub fn new(
        netns_path: PathBuf,
        guest_ip: Ipv4Addr,
        gateway_ip: Ipv4Addr,
        netmask: Ipv4Addr,
        dns_ip: Ipv4Addr,
        interaction_ip: Ipv4Addr,
        guest_mac: String,
        denied_cidrs: Vec<String>,
    ) -> Result<Self> {
        ensure!(
            netns_path.is_absolute() && netns_path.is_file(),
            "network namespace must exist at an absolute path"
        );
        ensure!(
            !guest_ip.is_unspecified() && !gateway_ip.is_unspecified() && guest_ip != gateway_ip,
            "invalid guest addressing"
        );
        let mac: Vec<_> = guest_mac.split(':').collect();
        ensure!(
            mac.len() == 6
                && mac
                    .iter()
                    .all(|v| v.len() == 2 && u8::from_str_radix(v, 16).is_ok()),
            "invalid guest MAC"
        );
        Ok(Self {
            netns_path,
            guest_ip,
            gateway_ip,
            netmask,
            dns_ip,
            interaction_ip,
            guest_mac,
            proxy: EgressProxy::with_denied_cidrs(
                denied_cidrs
                    .iter()
                    .map(|v| v.parse())
                    .collect::<Result<Vec<_>, _>>()?,
            ),
            initialized: Arc::new(Mutex::new(false)),
            denied_cidrs,
        })
    }

    pub(crate) fn boot_arg(&self) -> String {
        format!(
            "ip={}::{}:{}:instance:eth0:off:{}",
            self.guest_ip, self.gateway_ip, self.netmask, self.dns_ip
        )
    }

    pub(crate) fn set_policy(&self, policy: Option<&SandboxNetworkPolicy>) -> Result<()> {
        let mediated = policy.is_some_and(SandboxNetworkPolicy::requires_egress_proxy);
        let was_active = self.proxy.has_active(self.interaction_ip);
        if mediated {
            self.proxy
                .ensure_listener(self.interaction_ip, &self.netns_path)?;
            self.proxy.prepare(
                self.interaction_ip,
                policy.context("missing mediated policy")?,
            );
            if !was_active {
                self.proxy.activate(self.interaction_ip);
            }
        }
        let netns = self.netns_path.clone();
        let initialized = self.initialized.clone();
        let dns_ip = self.dns_ip;
        let denied_cidrs = self.denied_cidrs.clone();
        let policy = policy.cloned();
        let port = self.proxy.port();
        let result = std::thread::spawn(move || -> Result<()> {
            let file = File::open(netns)?;
            setns(file.as_fd(), CloneFlags::CLONE_NEWNET)?;
            let mut ready = initialized
                .lock()
                .map_err(|_| anyhow::anyhow!("network initialization lock poisoned"))?;
            if !*ready {
                initialize_namespace_egress_chain(dns_ip, &denied_cidrs)?;
                *ready = true;
            }
            set_namespace_egress_policy(policy.as_ref(), port)
        })
        .join()
        .map_err(|_| anyhow::anyhow!("policy worker panicked"))?;
        match &result {
            Ok(()) if mediated => self.proxy.activate(self.interaction_ip),
            Ok(()) => self.proxy.deactivate(self.interaction_ip),
            Err(_) if was_active => self.proxy.discard_pending(self.interaction_ip),
            Err(_) => self.proxy.teardown(self.interaction_ip),
        }
        result
    }
}
