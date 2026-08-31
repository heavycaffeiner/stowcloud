# Auth 02: the master key and the seal layer

> This document describes a from-scratch rebuild. The existing code under
> `go/internal/auth` (here `masterkey.go`, `keystate.go`, `rotate.go`,
> `seal.go`) is referenced as a behavioral specification only. The new
> implementation is written completely from scratch; nothing is copied.

## What the master key protects

Everything encrypted at rest: the SMB NT hash, the TOTP secret, the
recoverable share-link token, and the configuration secrets that are
credentials (the single-sign-on client secret is one). The key's lifecycle
is the one artifact that must never sit beside the database it protects:
a backup that carries both has encrypted nothing.

## The seal layer

Everything sealed travels as `nonce(24) || ciphertext`, XChaCha20-Poly1305
under a 32-byte key. Every seal binds the record it sits in and the key
version that sealed it as AAD, so a ciphertext cannot be transplanted
between records or replayed across a rotation.

| Binding prefix | Record | AAD carries |
| --- | --- | --- |
| `smb_nt` | user_smb_secret | user id, key version |
| `totp` | totp_secret | user id, key version |
| `shlink` | share_link | token hash, key version |
| `config` | settings secret | secret name, key version |

The prefixes are short and distinct so one record's AAD can never be
confused with another's. A stored blob shorter than a nonce is
`ErrCiphertextTooShort`, which is corruption, not an AEAD failure, and the
two must stay distinguishable in logs.

```go
func (s *Service) SealConfigSecret(name string, plain []byte) ([]byte, uint32, error)
func (s *Service) OpenConfigSecret(name string, blob []byte, keyVer uint32) ([]byte, error)

type LinkCipher struct{ /* one key */ }
func NewLinkCipher(key [32]byte) LinkCipher
func (c LinkCipher) Seal(token, tokenHash []byte, keyVer uint32) ([]byte, error)
func (c LinkCipher) Open(blob, tokenHash []byte, keyVer uint32) ([]byte, error)
```

`LinkCipher` satisfies the core's `core.LinkCipher` seam
(`../core/10-share-links.md`): the ciphertext format is defined here, in
exactly one place, and the server wires a cipher built from the loaded
ring into the core at assembly.

## The key ring

```go
type KeyRing struct { /* keys by version, order, newest, file path */ }

func NewKeyRing() (*KeyRing, error)                         // one fresh key at version 1
func LoadKeyRing(path string) (*KeyRing, bool, error)       // false: no file
func (r *KeyRing) Active() ([32]byte, uint32)
func (r *KeyRing) Get(ver uint32) ([32]byte, bool)
func (r *KeyRing) Has(ver uint32) bool
```

- **File format.** `SCMKEYRNG1\n`, a big-endian uint16 count, then
  `version(4) || key(32)` per entry. A file of exactly 32 bytes is the
  legacy raw key, read as version 1; this fallback is what upgrades a
  pre-ring deployment in place and it must not be dropped.
- Persisting goes through the durable replace with mode `0600` preserved
  exactly; the write moves from `vfs.ReplaceFileDurable` to
  `store/fsatomic` (the survey's repoint), same semantics.
- **Key material handling.** Keys live in fixed arrays, are filled by
  `crypto/rand` via a helper that panics on RNG failure (a system RNG
  that fails means nothing after it should run on a guess), and the ring
  is never logged.

## Resolution and the environment rule

```go
func ResolveKeyFile(dir string) (path string, insideDataDir bool, err error)
```

- **`SC_MASTER_KEY` set at all is a hard error** (`ErrKeyEnvForbidden`),
  whatever its value. A key in an environment variable is visible to
  `docker inspect` and `/proc/*/environ`; someone who set it believes it
  is being used, and silently ignoring it would honor that belief with a
  different key.
- `SC_MASTER_KEY_FILE` names a path; unset resolves to
  `{data}/master.key`.
- **Inside-the-data-directory is a warning, never a refusal.** This is a
  stated design decision (audit finding 7): the default location is
  inside the data directory, and refusing would make the default
  deployment unable to start. The warning is the line an operator acts
  on when setting up backups. The rebuild keeps the decision and this
  paragraph is its record.

## Startup alignment

```go
func (s *Service) OpenMasterKey(ctx context.Context) (*KeyRing, error)
```

Load or generate, then `startupKeyState`:

1. A database with no `key_version` row has never established one (a
   fresh deployment); the active version is recorded now.
2. A database naming a version the ring does not hold refuses startup
   with `ErrKeyVersionMissing`. Serving would discover the wrong key one
   failing login at a time.
3. **`alignRing`.** An interrupted rotation leaves the ring and the
   database disagreeing in exactly one of two ways, and both are
   recovered here: the database names an older version than the ring's
   newest (step 2 never committed; compact the ring back) or the ring
   still holds both keys while the database names the newest (step 3
   never ran; finish the compaction). Either way the ring ends holding
   exactly what the committed database requires.
4. **The decryption check** (`checkMasterKey`): open one sealed row of
   each kind under the loaded key, so a wrong key file refuses startup
   rather than surfacing as per-user failures.

## Rotation

```go
type RotationReport struct {
    OldVersion, NewVersion uint32
    SMBBrought, TOTPBrought, LinksBrought, ConfigSecretsBrought int
}

func (s *Service) RotateMasterKey(ctx context.Context) (RotationReport, error)
```

Three steps under an exclusive hold, because SQLite cannot commit
atomically with a key-file rename:

1. Persist a ring holding **both** the old and the new key.
2. In **one state transaction**: decrypt and re-seal every NT hash, TOTP
   secret, recoverable link token and config secret under the new key,
   then set the database's key version. Any row that cannot open aborts
   the transaction, which changes nothing.
3. Compact the ring to the new key only.

A crash at any boundary leaves at least the key the committed database
requires; `alignRing` finishes or rolls back whichever step never landed.
The report counts what moved, so the CLI prints how many rows rather than
only "done".

## Deliberate changes

1. **The file replace goes through `store/fsatomic`** instead of
   `vfs.ReplaceFileDurable` (the survey's inventory names this repoint).
   Semantics identical: durable, atomic, mode preserved.
2. **The narrowings** in the parse and marshal paths use `kit/num`.
3. **Presentation secrets join the key ring's durable material** (Phase 3
   amendment): a stable CSRF derivation key and an AEAD key/version for
   short-lived content capabilities and sealed login-flow delivery state.
   Sessions are durable, so a process-random CSRF key would strand live
   sessions after every restart. Content/login-flow plaintext never rests in
   the database. The methods are narrow and purpose-bound:

   ```go
   func (s *Service) CSRFKey(ctx context.Context) ([]byte, error)
   func (s *Service) SealPresentation(ctx context.Context, purpose string, plain []byte) (sealed []byte, version uint32, err error)
   func (s *Service) OpenPresentation(ctx context.Context, purpose string, sealed []byte, version uint32) ([]byte, error)
   ```

   `purpose` is AAD and callers use fixed literals (`content-claim`,
   `login-flow-delivery`), never request input. Rotation re-seals durable
   login-flow delivery rows in the same state transaction; short-lived content
   claims remain readable through their recorded old version until their
   five-minute expiry, so the ring keeps the immediately previous key for that
   bounded overlap before compaction.

Nothing else changes: the format, the env rule, the warning decision, the
three-step protocol and the startup checks are all as the reference.

## Tests

- Ring file round-trip, including the legacy 32-byte fallback reading as
  version 1; trailing bytes refuse; a short file refuses.
- `ResolveKeyFile`: `SC_MASTER_KEY` present errors whatever the value;
  the file variable wins; the default lands inside and reports so.
- Startup: a fresh database establishes the version; a database naming a
  missing version refuses; a wrong key file refuses at the decryption
  check, not at the first login.
- **Rotation fault injection** (the old `TestRotationFaultInjection`,
  rebuilt): kill the process between each pair of steps; the next
  startup aligns and every sealed row still opens.
- Rotation re-seals every kind and compacts; the report's counts match
  the rows.
- AAD binding: a ciphertext moved to another record refuses; a
  ciphertext replayed under the wrong version refuses; `LinkCipher`
  round-trips with the core's seam test doubles.
- Config secrets round-trip by name; the wrong name refuses.
- `CSRFKey` is stable across restart, 32 bytes and never logged; two fresh
  deployments differ.
- Presentation sealing is purpose- and version-bound; cross-purpose replay
  refuses. Rotation preserves an unexpired content claim and a deliverable
  login flow, then compacts after the overlap.
