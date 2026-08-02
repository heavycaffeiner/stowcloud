//! `sc-caps` — standalone kernel capability probe binary.
//!
//! Exists so the probe can be run in isolation (e.g. inside a throwaway VM
//! or container) without booting the whole server — the same probe the
//! startup self-diagnostic runs, on demand.

fn main() {
    sc_server::print_kernel_caps();
}
