//! `sc-caps` — standalone kernel capability probe binary.
//!
//! Exists so the probe can be run in isolation (e.g. inside a throwaway VM
//! or container) without booting the whole server — `DEPLOYMENT.md` §2's
//! self-diagnostic block, on demand.

fn main() {
    sc_server::print_kernel_caps();
}
