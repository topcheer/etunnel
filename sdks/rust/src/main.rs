//! `etunnel` CLI (`etunnel 3000`): thin wrapper over the library crate so the
//! same code serves `cargo install etunnel` (binary) and `cargo add etunnel`
//! (`etunnel::connect` / `Tunnel::done()`). See `src/lib.rs` for the API.
fn main() {
    etunnel::main_cli();
}
