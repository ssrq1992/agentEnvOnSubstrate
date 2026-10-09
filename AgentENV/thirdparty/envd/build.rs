fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_prost_build::configure()
        // Needed for older protoc versions used on some Linux hosts.
        .protoc_arg("--experimental_allow_proto3_optional")
        .emit_rerun_if_changed(true)
        .build_server(false)
        .build_client(true)
        .build_transport(false)
        .compile_protos(
            &["proto/filesystem.proto", "proto/process.proto"],
            &["proto"],
        )?;

    Ok(())
}
