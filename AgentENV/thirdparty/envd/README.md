# envd

This module is a Rust **client library** designed to interact with [`envd`](https://github.com/e2b-dev/infra/tree/main/packages/envd) via gRPC. It provides convenient interfaces for filesystem manipulation and process management within a remote or sandboxed environment.

## Overview

The library exposes two main modules corresponding to the defined gRPC services:

- **filesystem**: Offers typical file system operations.
  - **Operations**: `Stat`, `MakeDir`, `Move`, `ListDir`, `Remove`
  - **Features**: File and directory watching (`WatchDir`)

- **process**: Facilitates execution and management of processes.
  - **Operations**: `Start`, `List`, `OpenConnection`
  - **Features**: PTY support, signal sending, standard input/output streaming.

## Usage Examples

### Filesystem Operations

Here is an example of how to connect to the service and list files in a directory.

```rust
use envd::filesystem::FilesystemClient;
use envd::filesystem::ListDirRequest;
use tonic::Request;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Connect to the envd server
    // Ensure the address matches your running envd service
    let mut client = FilesystemClient::connect("http://localhost:49983").await?;

    // List contents of the root directory
    let list_resp = client
        .list_dir(Request::new(ListDirRequest {
            path: "/".to_string(),
            depth: 1,
        }))
        .await?
        .into_inner();
    
    println!("Entries:");
    for entry in list_resp.entries {
        println!("- Name: {}", entry.name);
    }

    Ok(())
}
```

### Process Management

Here is an example of how to start a process and stream its events/output.

```rust
use envd::process::{ProcessClient, StartRequest, ProcessConfig};
use futures_util::StreamExt;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Connect to the envd server
    let mut client = ProcessClient::connect("http://localhost:49983").await?;

    // Configure the process to run
    let config = ProcessConfig {
        cmd: "echo".to_string(),
        args: vec!["Hello, envd!".to_string()],
        envs: Default::default(),
        cwd: None,
    };

    // Prepare the start request
    let request = StartRequest {
        process: Some(config),
        pty: None, // Set PTY options if a pseudo-terminal is needed
        tag: Some("example-process".to_string()),
        stdin: Some(false),
    };

    // Start the process and get the event stream
    let mut stream = client.start(request).await?.into_inner();

    // Iterate over process events (Start, Data, Exit, etc.)
    while let Some(response) = stream.next().await {
        println!("Received event: {:?}", response);
    }

    Ok(())
}
```
