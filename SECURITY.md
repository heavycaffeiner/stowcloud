# Security policy

## Reporting a vulnerability

Report privately through GitHub: **Security → Report a vulnerability** on this
repository. That opens a draft advisory only you and the maintainer can read.

Do not open a public issue for a suspected vulnerability. There is no bug
bounty, and no email channel — the commit author address is a GitHub `noreply`
alias and does not receive mail.

A useful report says what an attacker gains, and gives enough detail to
reproduce it: the version or commit, the configuration that matters
(`allowed_origins`, `app_hosts`, whether SMB or OIDC is enabled, whether a
reverse proxy sits in front), and the request sequence.

This is maintained by one person as a non-commercial project. Reports are
handled on a best-effort basis and no response time is promised. If a fix
lands, the advisory is published with credit unless you ask otherwise.

## Supported versions

Only the current `master` tip. The project is pre-1.0 (`0.3.2`), there are no
release branches, and fixes are not backported.

## Scope

Deployment means Linux, in the configuration `docs/DEPLOYMENT.md` describes.
In scope:

- Escaping a share root, or reaching a path an account has no grant for
- Authentication or session flaws — login, TOTP, app passwords, recovery
  codes, OIDC, and the compat layer's Login Flow v2
- Share links returning more than their permissions allow, or leaking the
  paths behind them
- Sandbox escape from the forked preview worker
- Injection or memory-safety faults reachable from a request

Out of scope:

- **The non-Linux VFS backend.** `crates/sc-vfs/src/backend/portable.rs` walks
  paths in userspace instead of `openat2(RESOLVE_BENEATH)` and has no Landlock
  behind it. It exists so the test suite runs on a developer's machine.
  Windows and macOS are not deployment targets — see `README.md`.
- Anything that requires administrator access to exploit. An administrator can
  already grant themselves any path.
- Missing hardening in a deployment that ignores `docs/DEPLOYMENT.md` — no
  reverse proxy, a published port on a public interface, `docker run` without
  the compose file's `read_only`/`cap_drop`.
- Denial of service by resource exhaustion from an authenticated account.

## Not vulnerabilities

These are deliberate. If you think one of them is wrong, that is an issue or a
discussion, not an advisory:

- **CSRF is enforced for cookie sessions only.** The `Sc-Csrf` header is
  required when a session cookie authenticates the request. Bearer tokens and
  app passwords are not ambient credentials — a browser never attaches them on
  its own — so they do not need it.
- **The setup token is read from the request body, never a header.** Headers
  reach proxy and CDN access logs; bodies effectively never do.
- **The compat layer reports `productname` as `Nextcloud`.** Real clients
  branch on that string. It is a protocol value, not a claim of affiliation —
  see `README.md`.
- **SMB is off by default and LAN-only when on.** Exposing it beyond a LAN is
  the operator's decision, not a defect.

## Known gaps

Stated in `README.md` as well, repeated here because it bears on what a report
is worth: this code has never been reviewed by anyone outside this repository.
There is no Litmus conformance run in CI and no automated sync-client
regression suite. The Landlock and seccomp layers depend on kernel
configuration — `docs/DEPLOYMENT.md` §2 covers what happens when they are
unavailable, which is a downgrade, not a failure to start.
