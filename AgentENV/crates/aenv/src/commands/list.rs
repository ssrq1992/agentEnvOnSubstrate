use crate::client::{sandboxes::ListedSandbox, Client};
use crate::output::{self, Format};
use anyhow::Result;
use clap::Args as ClapArgs;
use tabled::Tabled;

#[derive(ClapArgs)]
pub struct Args {
    #[arg(long, value_enum)]
    output: Option<Format>,
}

#[derive(Tabled)]
struct Row {
    #[tabled(rename = "SANDBOX ID")]
    sandbox_id: String,
    template: String,
    state: String,
    #[tabled(rename = "CPU")]
    cpu: String,
    #[tabled(rename = "MEM (MiB)")]
    mem: String,
    #[tabled(rename = "DISK (MiB)")]
    disk: String,
    #[tabled(rename = "STARTED")]
    started: String,
}

pub fn run(args: Args) -> Result<()> {
    let client = Client::from_env()?;
    let sandboxes = client.list_sandboxes()?;
    output::render(
        output::resolve(args.output),
        &sandboxes,
        |s: &ListedSandbox| Row {
            sandbox_id: s.sandbox_id.clone(),
            template: s.alias.clone().unwrap_or_else(|| s.template_id.clone()),
            state: output::dash(s.state.clone()),
            cpu: output::dash(s.cpu_count),
            mem: output::dash(s.memory_mib),
            disk: output::dash(s.disk_size_mib),
            started: output::dash(s.started_at.clone()),
        },
    )
}
