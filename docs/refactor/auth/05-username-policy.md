# Auth 05: the one username rule

> This document describes a from-scratch rebuild. The existing code it
> replaces lives in three places (`go/internal/server/setup.go`
> `validateUsername`, `go/internal/smb/render.go` `checkPasswdName`,
> `go/internal/smbagent/accounts.go` `ValidName`) and is referenced as a
> behavioral specification only. The new implementation is written
> completely from scratch; nothing is copied.

## The problem this ends

Account names are validated in three inconsistent spellings today:

| Site | Rule | Purpose it serves |
| --- | --- | --- |
| `server/setup.go` | 1..64 chars of `[a-zA-Z0-9.\-_@+]` | the setup screen |
| `smb/render.go` | non-empty, no `:` `\n` `\r` NUL, plus the safe-name check | the passwd file format |
| `smbagent/accounts.go` | 1..N chars, `[a-z_]` first then `[a-z0-9_-]` | POSIX account creation and argument-injection defence |

The setup rule admits names the SMB rules refuse (`Alice`, `a.b@c`,
anything with an upper-case letter or `.` `@` `+`). The consequence the
survey records: one such account fails the **whole batch** SMB render,
because the render refuses the file rather than the name, and every
account loses SMB at once over one name nobody could have known was
wrong when it was created.

## The rule

One function, in `engine/service/auth/username.go`, called at every
creation path and re-exported through the SMB phase's contract:

```go
// ValidUsername holds a new account name to the one rule every consumer
// of the name can carry: the web screens, the passwd file, the POSIX
// account tools and the credential importer.
func ValidUsername(name string) error   // nil, or ErrNameInvalid with a fixed reason
```

The rule is the **intersection**, which is the strictest of the three:

- 1 to 32 characters (the POSIX portable bound the agent enforces).
- First character `[a-z]` or `_`.
- Remaining characters `[a-z0-9_-]`.

Lower-case only, no dots, no `@`, no `+`. Existing accounts that predate
the rule are **not** invalidated: the rule gates creation, and every
consumer that reads stored names keeps its own defensive refusal (the
passwd renderer still refuses a `:` it should never see). Validation at
the boundary does not excuse the format writers from checking what they
write.

The error carries a fixed reason, never an echo of the input, exactly as
the setup validator does today: a validation message that echoes input
is a reflection primitive.

## Who calls it

| Caller | Phase | Via |
| --- | --- | --- |
| `CreateUser` / `CreateAdmin` | this one | directly |
| The setup gate | 3 (assembly) | `auth.ValidUsername`, replacing its local copy |
| The SMB render | smb | the contract re-export; its own format checks stay as backstops |
| The agent's account creation | smb | same |

## Deliberate changes

1. **The intersection replaces the union.** The setup screen stops
   admitting names SMB later refuses. This narrows what a new account
   may be called; it does not touch any stored name.
2. **Creation-time enforcement in auth.** The old tree validates at the
   setup screen only; an account created through the admin surface
   bypassed validation entirely. `CreateUser` refuses now, whoever
   calls it.

## Tests

- Every name the old agent rule accepts passes; every name it refuses
  refuses.
- The specific breakage class: an upper-case name, a dotted name and an
  `@` name all refuse at creation with `ErrNameInvalid`.
- A name of a colon, a newline or a NUL refuses (and would refuse at the
  passwd renderer's backstop too).
- A leading dash refuses (the argument-injection case).
- The boundary lengths: 1 and 32 pass, 0 and 33 refuse.
- The error's message is fixed and does not contain the input.
- Admin-surface creation is gated: `CreateUser` with an invalid name
  refuses before any row exists.
