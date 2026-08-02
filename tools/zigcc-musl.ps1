# CC shim for cross-compiling to x86_64-unknown-linux-musl via `zig cc` on a
# Windows dev machine that has no native musl cross-gcc (see
# docs/DESIGN-AUTH.md environment constraints: `cargo check --target
# x86_64-unknown-linux-musl` must pass, Windows-only, no WSL/Docker).
#
# Why this exists: cc-rs (used transitively by rusqlite's "bundled" feature,
# which every crate using SQLite in this workspace enables) always appends
# `--target=x86_64-unknown-linux-musl` for clang-family compilers when
# cross-compiling. zig (>= ~0.12, confirmed on 0.16.0) rejects that 4-part
# LLVM/rustc triple ("arch-unknown-os-abi") outright:
#
#   error: unable to parse target query 'x86_64-unknown-linux-musl': UnknownOperatingSystem
#
# because zig's own -target/--target= query parser wants its 3-part form
# ("arch-os-abi", e.g. "x86_64-linux-musl"), not a 4-part vendor-qualified
# LLVM triple. This shim rewrites just that one token before forwarding
# everything else to `zig cc` untouched, so any rusqlite-bundled crate can
# cross-check against musl on this machine.
#
# Wired up by scripts/musl-env.sh (CC_x86_64_unknown_linux_musl), which only
# does so on a Windows host. It used to be a committed .cargo/config.toml,
# which pointed every host at this file's absolute path and broke CI.
#
# cc-rs treats CC_<target> as an already target-specific cross-compiler (the
# GNU convention, e.g. `x86_64-linux-musl-gcc`) and never appends a --target
# flag of its own. zig cc is target-generic, so without one it silently
# compiled every C dependency (sqlite3, zstd, mimalloc) for the *host*
# (Windows/COFF) instead of musl/ELF -- objects with no ELF magic, surfacing
# downstream as "undefined symbol" at the final link, not as a compile error.
# So this flag must be injected, not just reformatted when already present.

$fixed = $args | ForEach-Object {
    if ($_ -eq '--target=x86_64-unknown-linux-musl') {
        '--target=x86_64-linux-musl'
    } elseif ($_ -eq 'x86_64-unknown-linux-musl') {
        'x86_64-linux-musl'
    } else {
        $_
    }
}
if (-not ($fixed | Where-Object { $_ -like '--target=*' -or $_ -eq '-target' })) {
    $fixed = @('--target=x86_64-linux-musl') + $fixed
}
& zig cc @fixed
exit $LASTEXITCODE
