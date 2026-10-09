use anyhow::Result;
use clap::ValueEnum;
use serde::Serialize;
use std::fmt::Display;
use std::io::IsTerminal;
use tabled::settings::Style;
use tabled::{Table, Tabled};

pub fn dash<T: Display>(opt: Option<T>) -> String {
    opt.map(|v| v.to_string()).unwrap_or_else(|| "-".into())
}

#[derive(Copy, Clone, Debug, PartialEq, Eq, ValueEnum)]
pub enum Format {
    Table,
    Json,
}

pub fn resolve(explicit: Option<Format>) -> Format {
    explicit.unwrap_or_else(|| {
        if std::io::stdout().is_terminal() {
            Format::Table
        } else {
            Format::Json
        }
    })
}

pub fn render<T, R>(format: Format, rows: &[T], to_row: impl Fn(&T) -> R) -> Result<()>
where
    T: Serialize,
    R: Tabled,
{
    match format {
        Format::Json => {
            serde_json::to_writer_pretty(std::io::stdout().lock(), rows)?;
            println!();
        }
        Format::Table => {
            let mapped: Vec<R> = rows.iter().map(to_row).collect();
            let mut table = Table::new(&mapped);
            table.with(Style::sharp());
            println!("{}", table);
        }
    }
    Ok(())
}
