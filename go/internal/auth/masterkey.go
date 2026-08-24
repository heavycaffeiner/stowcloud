package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The master key protects everything encrypted at rest: the SMB NT hash, the
// TOTP secret and the recoverable share-link tokens. Its lifecycle is the one
// artifact that must never sit beside the database it protects, which is why
// a backup that carries both has encrypted nothing.

// keyFileDefault is where a master key is looked for and created when
// SC_MASTER_KEY_FILE names nothing.
const keyFileDefault = "master.key"

// keyLen is the XChaCha20-Poly1305 key size.
const keyLen = 32

// ErrKeyEnvForbidden is the hard error for SC_MASTER_KEY being present at
// all. Only a path may come from the environment; a key in an env var is
// visible to docker inspect and /proc/*/environ, which defeats the point of a
// key file, and someone who set it believes it is being used.
var ErrKeyEnvForbidden = errors.New("SC_MASTER_KEY must not be set: an environment variable is no place for a key; use SC_MASTER_KEY_FILE to name a file")

// ErrKeyVersionMissing is a key ring that does not hold the version state.db
// names as active. Startup refuses rather than serving on whatever is left.
var ErrKeyVersionMissing = errors.New("the master key ring does not hold the version the database names")

// ringMagic marks a key file as the versioned ring. A file that is not this
// header is treated as the legacy raw key, which is version 1.
const ringMagic = "SCMKEYRNG1\n"

// KeyRing is the set of master keys this process can open ciphertext with,
// plus the one the database says is active. Rotation keeps old and new keys
// side by side so the filesystem and SQLite can cross a durability boundary
// recoverably.
type KeyRing struct {
	keys     map[uint32][keyLen]byte
	order    []uint32
	newest   uint32
	filePath string
}

// NewKeyRing returns a ring holding a single fresh key at version 1, ready to
// be written to a file that does not exist yet.
func NewKeyRing() *KeyRing {
	var k [keyLen]byte
	mustRand(k[:])
	r := &KeyRing{keys: map[uint32][keyLen]byte{1: k}, order: []uint32{1}, newest: 1}
	return r
}

// mustRand fills b from crypto/rand or panics; a system RNG that fails means
// nothing that follows should run on a guess.
func mustRand(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
}

// LoadKeyRing reads a key file from path and reports whether it resolved
// inside the data directory. A missing file is not an error here; callers
// decide whether to generate.
func LoadKeyRing(path string) (*KeyRing, bool, error) {
	//nolint:gosec // the path is the operator's own key file, from the default data directory or their env var.
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading the master key: %w", err)
	}
	r, err := parseKeyRing(b)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	r.filePath = path
	return r, true, nil
}

func parseKeyRing(b []byte) (*KeyRing, error) {
	if len(b) == keyLen {
		var k [keyLen]byte
		copy(k[:], b)
		return &KeyRing{keys: map[uint32][keyLen]byte{1: k}, order: []uint32{1}, newest: 1}, nil
	}
	if !bytes.HasPrefix(b, []byte(ringMagic)) {
		return nil, errors.New("the file is neither a legacy raw key nor a versioned ring")
	}
	b = b[len(ringMagic):]
	if len(b) < 2 {
		return nil, io.ErrUnexpectedEOF
	}
	count := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	r := &KeyRing{keys: map[uint32][keyLen]byte{}}
	for i := 0; i < count; i++ {
		if len(b) < 4+keyLen {
			return nil, io.ErrUnexpectedEOF
		}
		ver := binary.BigEndian.Uint32(b)
		var k [keyLen]byte
		copy(k[:], b[4:4+keyLen])
		r.keys[ver] = k
		r.order = append(r.order, ver)
		if ver > r.newest {
			r.newest = ver
		}
		b = b[4+keyLen:]
	}
	if len(b) != 0 {
		return nil, errors.New("trailing bytes after the last key")
	}
	return r, nil
}

// Active returns the key with the highest version, which is what newly sealed
// ciphertext uses.
func (r *KeyRing) Active() ([keyLen]byte, uint32) {
	return r.keys[r.newest], r.newest
}

// Get returns the key at ver.
func (r *KeyRing) Get(ver uint32) ([keyLen]byte, bool) {
	k, ok := r.keys[ver]
	return k, ok
}

// Has reports whether ver is in the ring.
func (r *KeyRing) Has(ver uint32) bool {
	_, ok := r.keys[ver]
	return ok
}

// withNewKey returns a ring that also holds a fresh key at the version after
// newest. The database is still on the old version at this point, which is
// why both keys have to be present together.
func (r *KeyRing) withNewKey() (*KeyRing, uint32) {
	next := r.newest + 1
	var k [keyLen]byte
	mustRand(k[:])
	cp := &KeyRing{keys: map[uint32][keyLen]byte{}, order: append([]uint32{}, r.order...), newest: next, filePath: r.filePath}
	for v, key := range r.keys {
		cp.keys[v] = key
	}
	cp.keys[next] = k
	cp.order = append(cp.order, next)
	return cp, next
}

// compactTo returns a ring holding only ver.
func (r *KeyRing) compactTo(ver uint32) (*KeyRing, bool) {
	k, ok := r.keys[ver]
	if !ok {
		return nil, false
	}
	return &KeyRing{keys: map[uint32][keyLen]byte{ver: k}, order: []uint32{ver}, newest: ver, filePath: r.filePath}, true
}

// marshal renders the ring file bytes.
func (r *KeyRing) marshal() []byte {
	var buf bytes.Buffer
	buf.Write([]byte(ringMagic)) //nolint:errcheck // bytes.Buffer.Write never fails.
	var c [2]byte
	binary.BigEndian.PutUint16(c[:], uint16(len(r.order))) //nolint:gosec // a key ring holds a handful of keys, far below the field width.
	buf.Write(c[:])                                        //nolint:errcheck // bytes.Buffer.Write never fails.
	for _, ver := range r.order {
		var v [4]byte
		binary.BigEndian.PutUint32(v[:], ver)
		buf.Write(v[:]) //nolint:errcheck // bytes.Buffer.Write never fails.
		k := r.keys[ver]
		buf.Write(k[:]) //nolint:errcheck // bytes.Buffer.Write never fails.
	}
	return buf.Bytes()
}

// persist durably replaces the ring file, preserving the exact 0600 mode so
// the key never becomes readable to a neighbour of the data directory.
func (r *KeyRing) persist() error {
	if r.filePath == "" {
		return errors.New("cannot persist a key ring with no file")
	}
	b := r.marshal()
	return vfs.ReplaceFileDurable(r.filePath, 0o600, func(f *os.File) error {
		_, err := f.Write(b)
		return err
	})
}

// ResolveKeyFile names the master key path from the environment, enforcing
// the rule that only a path may come from there. It returns the path and
// whether the key resolves inside the data directory (a warning the caller
// logs, never a refusal, because it is the default location).
func ResolveKeyFile(dir string) (string, bool, error) {
	if _, set := os.LookupEnv("SC_MASTER_KEY"); set {
		return "", false, ErrKeyEnvForbidden
	}
	if p, set := os.LookupEnv("SC_MASTER_KEY_FILE"); set && p != "" {
		return p, pathInside(dir, p), nil
	}
	return filepath.Join(dir, keyFileDefault), true, nil
}

// pathInside reports whether child is strictly within dir. The default key
// path is, which is why the caller logs the warning rather than refusing: a
// deployment that backs up its database would otherwise be unable to start in
// its default configuration.
func pathInside(dir, child string) bool {
	rel, err := filepath.Rel(dir, child)
	if err != nil {
		return false
	}
	return rel != ".." && !pathHasDotDot(rel)
}

func pathHasDotDot(rel string) bool {
	for _, part := range filepath.SplitList(rel) {
		if part == ".." {
			return true
		}
	}
	return false
}

// OpenMasterKey loads or generates the key ring and performs the startup
// checks that require the database. It returns the ring with its newest key
// ready for sealing.
func (s *Service) OpenMasterKey(ctx context.Context) (*KeyRing, error) {
	dir := s.dir
	path, inside, err := ResolveKeyFile(dir)
	if err != nil {
		return nil, err
	}
	if inside {
		// The default location, not a refusal: refusing would make the
		// default configuration fail to start. This is the line an operator
		// acts on when they set up backups.
		slog.Warn("the master key resolves inside the data directory; keep it out of the database backup",
			slog.String("file", path))
	}

	ring, ok, err := LoadKeyRing(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		ring = NewKeyRing()
		ring.filePath = path
		if err := ring.persist(); err != nil {
			return nil, fmt.Errorf("generating the master key: %w", err)
		}
		slog.Info("generated a new master key", slog.String("file", filepath.Base(path)))
	}
	s.mk = ring

	if err := s.startupKeyState(ctx); err != nil {
		return nil, err
	}
	return ring, nil
}
