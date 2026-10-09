use crate::auth::{save, Credentials};
use anyhow::Result;
use std::io::{self, BufRead, Write};

const DEFAULT_URL: &str = "http://localhost:8000";

pub fn run() -> Result<()> {
    let stdin = io::stdin();
    let mut stdout = io::stdout();

    write!(stdout, "AENV server URL [{}]: ", DEFAULT_URL)?;
    stdout.flush()?;
    let mut url = String::new();
    stdin.lock().read_line(&mut url)?;
    let url = url.trim();
    let url = if url.is_empty() { DEFAULT_URL } else { url };

    let api_key = rpassword::prompt_password("API key: ")?;
    let api_key = api_key.trim().to_string();
    if api_key.is_empty() {
        anyhow::bail!("API key cannot be empty");
    }

    save(&Credentials {
        url: url.to_string(),
        api_key,
    })?;
    println!("Credentials saved.");
    Ok(())
}
