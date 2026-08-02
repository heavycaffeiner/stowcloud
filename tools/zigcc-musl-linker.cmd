@echo off
REM rustc's `-C linker=` needs a single no-space program path (see
REM zigcc-musl.ps1's header for why the CC_* shim can't be reused as-is).
REM Calls `zig cc` directly, skipping the PowerShell layer that
REM zigcc-musl.ps1 needs for the CC role (rewriting cc-rs's 4-part
REM `--target=x86_64-unknown-linux-musl` to zig's 3-part form): the link
REM step never carries that token, and routing rustc's `@response-file`
REM linker argument through `& zig cc @fixed` in PowerShell was silently
REM truncating it to bare "@" (zig then opened "" and got IsDir) --
REM confirmed by logging $fixed inside zigcc-musl.ps1, which showed the
REM full path intact right before the splat call to zig, and zig's own
REM error report after.  cmd's `%*` forwards the response file untouched.
zig cc %*
