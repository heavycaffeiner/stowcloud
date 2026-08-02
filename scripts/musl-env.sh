#!/usr/bin/env bash
# Sourced, never executed. Wires up whatever *this host* needs to cross-compile
# to x86_64-unknown-linux-musl, and exports `SC_MUSL_PROBE`: the one program
# that has to be on PATH for that to be possible.
#
#   . scripts/musl-env.sh
#
# This lived in a committed `.cargo/config.toml` until that broke CI. A cargo
# config applies to every host that reads it, and this one hardcoded a single
# Windows box's absolute paths, so `ubuntu-latest` resolved the linker relative
# to its checkout and died on
#
#   linker `/home/runner/work/stowcloud/stowcloud/C:/Users/<name>/.../
#   zigcc-musl-linker.cmd` not found
#
# after the same file's `CC_` entry had already needed a special case in the
# workflow for the same reason. Host-specific toolchain wiring belongs
# somewhere that can ask which host it is on.
#
# Every assignment defers to an already-set value, so CI, the Dockerfile and a
# one-off shell can all override without editing this file.
#
# Trade-off, stated because it is a real one: a bare `cargo build --target
# x86_64-unknown-linux-musl` on Windows no longer works without sourcing this
# first. `scripts/verify.sh` and `scripts/deploy.sh` — the two things that
# cross-compile here — both do.

case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    # No native musl-gcc on this platform and no WSL/Docker on the dev box, so
    # both the C compiler (rusqlite is bundled, so every build needs one) and
    # rustc's linker driver route through `zig cc`. Repo-relative, resolved to
    # a Windows path with `pwd -W`: rustc and powershell are native programs
    # and cannot open an MSYS `/c/...` path.
    _sc_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && { pwd -W 2>/dev/null || pwd; })
    : "${CC_x86_64_unknown_linux_musl:=powershell -NoProfile -ExecutionPolicy Bypass -File $_sc_root/tools/zigcc-musl.ps1}"
    : "${AR_x86_64_unknown_linux_musl:=zig ar}"
    : "${CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_LINKER:=$_sc_root/tools/zigcc-musl-linker.cmd}"
    # rustc infers the C compiler's target from the linker's own filename (a
    # real `x86_64-linux-musl-gcc` would say so) and adds no --target of its
    # own for a custom `-C linker=` path. Without this, `zig cc` links as the
    # host and rejects every ELF .o as "unknown file type".
    : "${CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_RUSTFLAGS:=-C link-self-contained=no -C link-arg=--target=x86_64-linux-musl}"
    export CC_x86_64_unknown_linux_musl AR_x86_64_unknown_linux_musl
    export CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_LINKER
    export CARGO_TARGET_X86_64_UNKNOWN_LINUX_MUSL_RUSTFLAGS
    # The shims are thin wrappers; zig is what actually has to exist.
    export SC_MUSL_PROBE=zig
    unset _sc_root
    ;;
  *)
    # Nothing to override. rustc links the musl target self-contained on its
    # own, and the only C compiler involved is the one rusqlite's bundled
    # SQLite needs — `musl-tools` installs it as `musl-gcc`, not under the
    # `x86_64-linux-musl-gcc` name cc-rs probes for.
    : "${CC_x86_64_unknown_linux_musl:=musl-gcc}"
    : "${AR_x86_64_unknown_linux_musl:=ar}"
    export CC_x86_64_unknown_linux_musl AR_x86_64_unknown_linux_musl
    export SC_MUSL_PROBE="${CC_x86_64_unknown_linux_musl%% *}"
    ;;
esac
