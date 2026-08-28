# SMB 02: the agent's durable writes

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/smbagent/sync_linux.go` and `accounts.go` is referenced as
> a behavioral specification only. The new implementation is written
> completely from scratch; nothing is copied.

## The defect this document exists for

The audit's finding 1, one of the survey's highest-severity items: the
agent writes `smb.conf` with a plain `os.WriteFile` while the durable
primitive sits two functions away in the same call graph
(`WritePasswd` already uses it for `/etc/passwd`). Two writes, two
different fates on power loss:

| Write | Old code | Consequence of a torn write |
| --- | --- | --- |
| the testparm candidate | `os.WriteFile`, 0600 | none: regenerated every pass, never read by the daemon |
| the promoted `smb.conf` | `os.WriteFile`, 0644 | **`smbd` fails entirely on next start or reload** |
| `/etc/passwd` | durable replace | already correct |

The rebuild: **the promotion is a durable atomic replace**
(`store/fsatomic`), mode preserved. The candidate keeps its plain
write, with a comment saying why that is allowed (validation scratch,
regenerated, never read by the daemon): the distinction is the lesson,
and marking both as "must be durable" would erase it.

The sequencing that surrounds the promotion is also part of the
contract: render candidate, validate with `testparm`, promote durably,
then `Import`/`Prune` accounts against the promoted file. A crash
between promote and import leaves a valid config with stale accounts,
which the next poll converges; there is no window where the daemon can
read a torn file.

## The account sync

`accounts.go` carries the verified pieces whole:

- `WritePasswd` durable; the passwd line format is what `smbd`'s libc
  reads (the old format test carries over).
- `Collisions` refuses a sync whose desired entries collide with real
  system accounts on name or uid; `ValidName` is the strict rule that
  became auth's canonical one (`../auth/05-username-policy.md`), and
  the agent keeps its copy as the last line of defence, since the
  agent cannot assume every server predates the rule.
- The batch-wide refusal on collision **stays** at this layer: unlike
  the render's account lists (00), a passwd write that partially
  applied against a collision is exactly how a uid ends up owned by
  the wrong account. With auth's creation-time rule, the batch refusal
  becomes the emergency brake it should have been, not the everyday
  failure it was.
- `Import` moves credentials into the daemon's own database;
  `MissingPassdb`/`MissingGroups`/`Prune` are the reconciliation
  reads.

## Deliberate changes

1. **The promoted `smb.conf` write becomes durable** (the defect).
2. The candidate write is documented as deliberately plain.
3. Nothing else: the sequencing, the collision rules and the passwd
   format carry whole.

## Tests

- The promotion survives fault injection: kill between stage and
  rename; the daemon-visible file is always whole (old or new, never
  torn).
- The candidate is not durable (a torn candidate self-heals next
  pass): assert only that the pass regenerates it.
- The sequencing: a crash after promote and before import converges on
  the next poll.
- Collisions refuse the batch; a colliding uid never lands in passwd.
- The passwd line format test (the byte-exact fixture).
- `ValidName` agrees with auth's rule on every vector (parity test
  against the shared vectors).
