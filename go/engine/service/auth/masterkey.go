package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
)

// The master key protects everything encrypted at rest: the file-sharing
// credential, the second factor's secret, the recoverable share-link token,
// the configuration secrets that are credentials, and the presentation layer's
// short-lived capabilities. Its lifecycle is the one artifact that must never
// sit beside the database it protects, because a backup carrying both has
// encrypted nothing.

// keyFileDefault is the name a key file takes when the environment names no
// path.
const keyFileDefault = "master.key"

// ringMagic identifies a key file as the versioned ring. Files lacking this
// header are interpreted as the legacy raw key, treated as version 1.
const ringMagic = "SCMKEYRNG1\n"

// The refusals the key lifecycle answers with.
var (
	// ErrKeyEnvForbidden is SC_MASTER_KEY being present at all. A key in an
	// environment variable is visible to a container inspection and to
	// /proc, and somebody who set it believes it is being used, so ignoring
	// it silently would honour that belief with a different key.
	ErrKeyEnvForbidden = errors.New(
		"SC_MASTER_KEY must not be set: an environment variable is no place for a key; " +
			"use SC_MASTER_KEY_FILE to name a file")

	// ErrKeyVersionMissing is a ring that does not hold the version the
	// database names. Startup refuses rather than serving on whatever is
	// left.
	ErrKeyVersionMissing = errors.New("the master key ring does not hold the version the database names")

	// ErrNoKeyRing is an operation needing the key before the key was opened.
	// It is a wiring error, not a runtime condition.
	ErrNoKeyRing = errors.New("the master key has not been opened")
)

// KeyRing holds every master key this process can decrypt with, plus whichever
// the database marks active. Rotation retains old and new together, since the
// filesystem and the database cannot commit atomically and the transition must
// be recoverable from either direction.
type KeyRing struct {
	mu       sync.Mutex
	keys     map[uint32][keyLen]byte
	order    []uint32
	newest   uint32
	filePath string
}

// NewKeyRing returns a ring holding one fresh key at version 1, ready to be
// written to a file that does not exist yet.
func NewKeyRing() (*KeyRing, error) {
	var k [keyLen]byte
	if err := fillRandom(k[:]); err != nil {
		return nil, err
	}
	return &KeyRing{keys: map[uint32][keyLen]byte{1: k}, order: []uint32{1}, newest: 1}, nil
}

// LoadKeyRing reads a key file. A missing file is not an error here: the
// caller decides whether to generate one.
func LoadKeyRing(path string) (*KeyRing, bool, error) {
	// Cleaned before the read, because the path comes from the operator's
	// environment and a lexically odd spelling of it should resolve to the
	// same file the warning about its location named.
	clean := filepath.Clean(path)
	b, err := os.ReadFile(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading the master key: %w", err)
	}
	r, err := parseKeyRing(b)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", filepath.Base(clean), err)
	}
	r.filePath = clean
	return r, true, nil
}

func parseKeyRing(b []byte) (*KeyRing, error) {
	// A file of exactly one key's worth of bytes is what a pre-ring
	// deployment wrote. Reading it as version 1 is what upgrades that
	// deployment in place, and dropping the fallback would strand it.
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

	r := &KeyRing{keys: make(map[uint32][keyLen]byte, count)}
	for i := 0; i < count; i++ {
		if len(b) < 4+keyLen {
			return nil, io.ErrUnexpectedEOF
		}
		ver := binary.BigEndian.Uint32(b)
		if _, dup := r.keys[ver]; dup {
			return nil, fmt.Errorf("the ring holds version %d twice", ver)
		}
		var k [keyLen]byte
		copy(k[:], b[4:4+keyLen])
		r.keys[ver] = k
		r.order = append(r.order, ver)
		if ver > r.newest {
			r.newest = ver
		}
		b = b[4+keyLen:]
	}
	if len(r.order) == 0 {
		return nil, errors.New("the ring holds no keys")
	}
	if len(b) != 0 {
		return nil, errors.New("trailing bytes after the last key")
	}
	return r, nil
}

// Active returns the newest key, which is what new ciphertext is sealed under.
func (r *KeyRing) Active() ([keyLen]byte, uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.keys[r.newest], r.newest
}

// Get returns the key at a version.
func (r *KeyRing) Get(ver uint32) ([keyLen]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.keys[ver]
	return k, ok
}

// Has reports whether a version is in the ring.
func (r *KeyRing) Has(ver uint32) bool {
	_, ok := r.Get(ver)
	return ok
}

// Versions is what the ring currently holds, in the order it holds them.
func (r *KeyRing) Versions() []uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint32(nil), r.order...)
}

// withNewKey produces a ring extended with a fresh key one version past the
// newest. The database remains on the previous version at that moment, which is
// why both must coexist.
func (r *KeyRing) withNewKey() (*KeyRing, uint32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	next := r.newest + 1
	if next < r.newest {
		return nil, 0, errors.New("the key version counter would wrap")
	}
	var k [keyLen]byte
	if err := fillRandom(k[:]); err != nil {
		return nil, 0, err
	}
	cp := &KeyRing{
		keys:     make(map[uint32][keyLen]byte, len(r.keys)+1),
		order:    append(append([]uint32(nil), r.order...), next),
		newest:   next,
		filePath: r.filePath,
	}
	for v, key := range r.keys {
		cp.keys[v] = key
	}
	cp.keys[next] = k
	return cp, next, nil
}

// compactTo returns a ring holding only one version.
func (r *KeyRing) compactTo(ver uint32) (*KeyRing, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k, ok := r.keys[ver]
	if !ok {
		return nil, false
	}
	return &KeyRing{
		keys:     map[uint32][keyLen]byte{ver: k},
		order:    []uint32{ver},
		newest:   ver,
		filePath: r.filePath,
	}, true
}

// marshal renders the file bytes.
func (r *KeyRing) marshal() ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	count, err := num.Narrow[uint16](len(r.order))
	if err != nil {
		return nil, fmt.Errorf("a ring of %d keys does not fit the file format: %w", len(r.order), err)
	}
	out := make([]byte, 0, len(ringMagic)+2+len(r.order)*(4+keyLen))
	out = append(out, ringMagic...)
	out = binary.BigEndian.AppendUint16(out, count)
	for _, ver := range r.order {
		out = binary.BigEndian.AppendUint32(out, ver)
		k := r.keys[ver]
		out = append(out, k[:]...)
	}
	return out, nil
}

// persist durably replaces the ring file, keeping the exact 0600 mode so the
// key never becomes readable to a neighbour of the data directory.
func (r *KeyRing) persist() error {
	if r.filePath == "" {
		return errors.New("cannot persist a key ring with no file")
	}
	b, err := r.marshal()
	if err != nil {
		return err
	}
	return fsatomic.ReplaceFileDurable(r.filePath, 0o600, func(f *os.File) error {
		_, werr := f.Write(b)
		return werr
	})
}

// ResolveKeyFile derives the master key path from the environment, enforcing
// that only a path may originate there. It also reports whether the key lands
// inside the data directory, which the caller records.
func ResolveKeyFile(dir string) (path string, insideDataDir bool, err error) {
	if _, set := os.LookupEnv("SC_MASTER_KEY"); set {
		return "", false, ErrKeyEnvForbidden
	}
	if p, set := os.LookupEnv("SC_MASTER_KEY_FILE"); set && p != "" {
		return p, pathInside(dir, p), nil
	}
	return filepath.Join(dir, keyFileDefault), true, nil
}

// pathInside reports whether child is within dir. The default key path is,
// and the caller warns rather than refusing: refusing would leave the default
// deployment unable to start, and the warning is the line an operator acts on
// when setting up backups.
func pathInside(dir, child string) bool {
	rel, err := filepath.Rel(dir, child)
	if err != nil {
		return false
	}
	if rel == ".." {
		return false
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".." {
			return false
		}
	}
	return true
}

// fillRandom fills b from the system source. A failure is fatal to the
// operation rather than survivable: nothing after it should run on a guess.
func fillRandom(b []byte) error {
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("the system random source failed: %w", err)
	}
	return nil
}

// activeKey is the newest ring key, or the wiring error.
func (s *Service) activeKey() ([keyLen]byte, uint32, error) {
	ring := s.keyRing()
	if ring == nil {
		var zeroKey [keyLen]byte
		return zeroKey, 0, ErrNoKeyRing
	}
	k, ver := ring.Active()
	return k, ver, nil
}

// keyAt is the ring key at one version.
func (s *Service) keyAt(ver uint32) ([keyLen]byte, error) {
	var zeroKey [keyLen]byte
	ring := s.keyRing()
	if ring == nil {
		return zeroKey, ErrNoKeyRing
	}
	k, ok := ring.Get(ver)
	if !ok {
		return zeroKey, fmt.Errorf("%w: version %d", ErrKeyVersionMissing, ver)
	}
	return k, nil
}

// OpenMasterKey loads or generates the ring and runs the startup checks that
// need the database. It is called once, before the first request: a key that
// cannot decrypt what is on disk is a refused startup rather than a cascade
// of failing logins.
func (s *Service) OpenMasterKey(ctx context.Context) (*KeyRing, error) {
	path, inside, err := ResolveKeyFile(s.dir)
	if err != nil {
		return nil, err
	}
	if inside {
		// The default location, and a warning rather than a refusal: refusing
		// would make the default configuration fail to start.
		s.log.Warn("the master key resolves inside the data directory; keep it out of the database backup",
			"file", path)
	}

	ring, found, err := LoadKeyRing(path)
	if err != nil {
		return nil, err
	}
	if !found {
		if ring, err = NewKeyRing(); err != nil {
			return nil, err
		}
		ring.filePath = path
		if perr := ring.persist(); perr != nil {
			return nil, fmt.Errorf("generating the master key: %w", perr)
		}
		s.log.Info("generated a new master key", "file", filepath.Base(path))
	}
	s.setKeyRing(ring)

	if err := s.startupKeyState(ctx); err != nil {
		return nil, err
	}
	return s.keyRing(), nil
}
