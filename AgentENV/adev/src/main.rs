mod codegen;
mod config;
mod coverage;
mod ensure_tool;
mod mutants;
mod util;

use clap::{Parser, Subcommand};

#[derive(Parser)]
#[command(name = "adev", about = "AENV dev/CI tooling")]
struct Cli {
    #[command(subcommand)]
    command: Commands,
}

#[derive(Subcommand)]
enum Commands {
    /// Run code generators (auto-installs protoc/openapi if missing)
    Codegen(codegen::CodegenArgs),
    /// Run mutation tests (auto-installs cargo-mutants)
    Mutants(mutants::MutantsArgs),
    /// Run code coverage (auto-installs cargo-llvm-cov)
    Coverage(coverage::CoverageArgs),
}

fn main() -> anyhow::Result<()> {
    let cli = Cli::parse();

    match cli.command {
        Commands::Codegen(args) => codegen::run(args),
        Commands::Mutants(args) => mutants::run(args),
        Commands::Coverage(args) => coverage::run(args),
    }
}
