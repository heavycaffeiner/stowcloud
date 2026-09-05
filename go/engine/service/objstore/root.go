//go:build linux

package objstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// objstoreDeviceTag marks a synthetic Dev value as belonging to this
// backend rather than to any real dev_t the kernel could hand back, so a
// caller comparing devices across backends never mistakes one for a real
// mounted filesystem. It has no meaning beyond that: it is not a magic
// number this package interprets, only a fixed high bit pattern real device
// numbers do not use.
const objstoreDeviceTag = uint64(0x6f626a73_00000000) // "objs" in the high 32 bits

// syntheticDevice derives a stable per-share device number: stable across a
// restart because it is a pure function of the share id, and distinct per
// share because two different s3 shares must never look like the same
// filesystem to a caller pairing (dev, ino).
func syntheticDevice(share vfs.ShareID) uint64 {
	return objstoreDeviceTag ^ uint64(share)
}

// Options carries everything Open needs to serve one share's bucket.
type Options struct {
	Share      vfs.ShareID
	Config     Config
	Secret     secret.Secret
	ScratchDir string
	Policy     vfs.SharePolicy
	Logger     *slog.Logger
	Client     *http.Client

	// Clock reads the time newRequest signs each request with. Nil takes
	// the system clock.
	Clock clock.Clock
}

// partHandle records where CreatePart staged one part, so PublishPart can
// find it by the same SafePath the caller created it under.
//
// The descriptor is deliberately absent: it belongs to the caller, which
// closes it before publishing, so the only durable handle on the staged
// bytes is their name in scratch space.
type partHandle struct {
	scratch vfs.SafePath
}

// Root serves one share's S3-compatible bucket as a vfs.Root. Every byte a
// caller reads or writes passes through server-owned scratch space: this
// package never hands back a descriptor pointing directly at the bucket,
// because there is no such descriptor to hand back.
type Root struct {
	share  vfs.ShareID
	cfg    Config
	policy vfs.SharePolicy
	logger *slog.Logger
	http   *http.Client
	signer *signer
	clk    clock.Clock

	endpointScheme string
	endpointHost   string
	dev            uint64

	scratch *vfs.ShareRoot

	mu    sync.Mutex
	parts map[string]partHandle
}

var _ vfs.Root = (*Root)(nil)

// Open constructs a Root, admitting the scratch directory the same way any
// other scratch space is admitted and probing the bucket once so a
// misconfigured endpoint, bucket or credential is refused here rather than
// on the first request a client happens to make.
func Open(ctx context.Context, opt Options) (*Root, error) {
	if opt.Config.Bucket == "" {
		return nil, errors.New("objstore: open: bucket is required")
	}
	endpoint, err := url.Parse(opt.Config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("objstore: open: invalid endpoint: %w", err)
	}

	scratch, err := vfs.OpenScratchRoot(opt.ScratchDir, opt.Policy)
	if err != nil {
		return nil, fmt.Errorf("objstore: open scratch root: %w", err)
	}

	client := opt.Client
	if client == nil {
		client = defaultHTTPClient()
	}
	logger := opt.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opt.Clock
	if clk == nil {
		clk = clock.System()
	}

	r := &Root{
		share:          opt.Share,
		cfg:            opt.Config,
		policy:         opt.Policy,
		logger:         logger,
		http:           client,
		clk:            clk,
		endpointScheme: endpoint.Scheme,
		endpointHost:   endpoint.Host,
		dev:            syntheticDevice(opt.Share),
		scratch:        scratch,
		parts:          make(map[string]partHandle),
		signer: &signer{
			accessKey: opt.Config.AccessKey,
			secret:    append([]byte(nil), opt.Secret.Reveal()...),
			region:    opt.Config.Region,
		},
	}

	probeCtx, cancel := context.WithTimeout(ctx, metadataRequestTimeout)
	defer cancel()
	if _, err := r.listObjectsV2(probeCtx, r.cfg.Prefix, "", "", 0); err != nil {
		err = errors.Join(err, scratch.Close())
		return nil, fmt.Errorf("objstore: open %s: %w", opt.Config.Describe(), err)
	}
	return r, nil
}

// objectKey maps a share-relative path onto the S3 key that stores it. The
// share root itself has no key of its own; every other path is the
// configured prefix joined with the path's own components.
func (r *Root) objectKey(p vfs.SafePath) string {
	if p.IsRoot() {
		return r.cfg.Prefix
	}
	if r.cfg.Prefix == "" {
		return p.String()
	}
	return r.cfg.Prefix + "/" + p.String()
}

// dirPrefix is objectKey with the trailing "/" every directory listing and
// every directory-marker key needs, folding the empty-prefix bucket root
// down to "" rather than "/", which ListObjectsV2 treats as a literal
// leading-slash prefix instead of "everything."
func (r *Root) dirPrefix(p vfs.SafePath) string {
	key := r.objectKey(p)
	if key == "" {
		return ""
	}
	return key + "/"
}

func (r *Root) syntheticIno(key string) uint64 {
	h := fnv.New64a()
	if _, err := h.Write([]byte(key)); err != nil {
		panicInfallibleWrite(err)
	}
	return h.Sum64()
}

func (r *Root) metadataCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), metadataRequestTimeout)
}

// mintScratchPath names a fresh, unpredictable control file inside scratch
// space. JoinControl is the only function permitted to produce the reserved
// prefix a scratch part file needs, which is why this goes through it
// rather than building the SafePath by hand.
func mintScratchPath() (vfs.SafePath, error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return vfs.SafePath{}, fmt.Errorf("objstore: mint a scratch name: %w", err)
	}
	return vfs.RootPath().JoinControl(".scpart-" + hex.EncodeToString(suffix[:]))
}

func (r *Root) newScratchFile() (*vfs.File, vfs.SafePath, error) {
	p, err := mintScratchPath()
	if err != nil {
		return nil, vfs.SafePath{}, err
	}
	f, err := r.scratch.CreatePart(p)
	if err != nil {
		return nil, vfs.SafePath{}, err
	}
	return f, p, nil
}

// ID is the share this root serves.
func (r *Root) ID() vfs.ShareID { return r.share }

func (r *Root) Policy() vfs.SharePolicy { return r.policy }

// Dev is this backend's synthetic device, the same for every path this root
// resolves: an S3 bucket carries no nested-mount concept a rename could
// cross, so there is only ever the one device to report.
func (r *Root) Dev() uint64 { return r.dev }

// DirDev is the same synthetic device Dev reports: see Dev's comment.
func (r *Root) DirDev(vfs.SafePath) (uint64, error) { return r.dev, nil }

// FsType classifies nothing: an S3 bucket is not a Linux filesystem type
// AdmitFsType or FsType.String has a case for, and adding one to vfs for a
// single caller here would give it a meaning it does not have. Describe on
// Config already carries the human answer.
func (r *Root) FsType() vfs.FsType { return vfs.FsType(0) }

// HasBtime is always false: S3 has no concept of a birth time distinct from
// its last-modified time.
func (r *Root) HasBtime() bool { return false }

// IsScratch is always false: a Root here serves a registered share, never
// server-owned scratch space.
func (r *Root) IsScratch() bool { return false }

// Alive issues the cheapest possible request that still proves the bucket
// answers under this share's credentials: a listing capped at zero results.
func (r *Root) Alive() error {
	ctx, cancel := r.metadataCtx()
	defer cancel()
	_, err := r.listObjectsV2(ctx, r.cfg.Prefix, "", "", 0)
	return err
}

// Close releases the scratch root. The bucket itself holds nothing open to
// release: every request this package makes is a single round trip.
func (r *Root) Close() error { return r.scratch.Close() }

// Space reports the scratch filesystem's own numbers, since S3 itself
// carries no quota this package can query: scratch space is what actually
// bounds the largest file this share can serve, so that is what is
// reported rather than an invented figure with no floor under it.
func (r *Root) Space(vfs.SafePath) (vfs.FsSpace, error) {
	return r.scratch.Space(vfs.RootPath())
}

// SetTimes is a no-op returning nil. S3 has no API to set an object's
// modification time to an arbitrary value; the upload engine calls this as
// a best-effort courtesy once a transfer finishes, and failing the whole
// upload over a timestamp this store cannot honor would be a worse outcome
// than silently keeping the time the PUT itself recorded.
func (r *Root) SetTimes(vfs.SafePath, int64) error { return nil }

// Stat answers for the share root synthetically, without a round trip: a
// registered share's root always exists, and Alive is the call that
// actually probes the bucket. Everything else is a HeadObject first, since
// a file is the common case, falling back to a directory probe using the
// same rule ReadDir applies: a prefix with a marker or any child at all is
// a directory, whether or not the marker itself exists.
func (r *Root) Stat(p vfs.SafePath) (vfs.Stat, error) {
	if p.IsRoot() {
		return vfs.Stat{Dev: r.dev, Ino: r.syntheticIno(""), Kind: vfs.KindDir, Mode: r.policy.ModeDir, Nlink: 1}, nil
	}
	ctx, cancel := r.metadataCtx()
	defer cancel()

	key := r.objectKey(p)
	found, size, mtimeNs, err := r.headObject(ctx, key)
	if err != nil {
		return vfs.Stat{}, fmt.Errorf("stat: %w", err)
	}
	if found {
		return vfs.Stat{
			Dev: r.dev, Ino: r.syntheticIno(key), Size: size, MtimeNs: mtimeNs,
			Kind: vfs.KindFile, Mode: r.policy.ModeFile, Nlink: 1,
		}, nil
	}
	isDir, dirMtimeNs, err := r.isDirectory(ctx, key)
	if err != nil {
		return vfs.Stat{}, fmt.Errorf("stat: %w", err)
	}
	if !isDir {
		return vfs.Stat{}, fmt.Errorf("stat: %w", vfs.ErrNotFound)
	}
	return vfs.Stat{
		Dev: r.dev, Ino: r.syntheticIno(key + "/"), MtimeNs: dirMtimeNs,
		Kind: vfs.KindDir, Mode: r.policy.ModeDir, Nlink: 1,
	}, nil
}

// ReadDirFunc merges one prefix's CommonPrefixes and Contents into the
// directory model documented on this package: a CommonPrefix is always a
// directory entry, whether or not a marker object backs it, and a Contents
// key ending in "/" is a marker, either this directory's own or a child
// directory's, and is never shown as a file.
func (r *Root) ReadDirFunc(p vfs.SafePath, policy vfs.ReservedPolicy, fn func(vfs.DirEntry) bool) error {
	dirKey := r.dirPrefix(p)
	token := ""
	for {
		ctx, cancel := r.metadataCtx()
		result, err := r.listObjectsV2(ctx, dirKey, "/", token, 1000)
		cancel()
		if err != nil {
			return err
		}

		for _, cp := range result.CommonPrefixes {
			rest, err := validateListedKey(cp.Prefix, dirKey)
			if err != nil {
				return err
			}
			name := strings.TrimSuffix(rest, "/")
			if name == "" {
				continue
			}
			if policy == vfs.HideReserved && vfs.IsReservedName(name) {
				continue
			}
			if !fn(vfs.DirEntry{Name: name, Kind: vfs.KindDir, Ino: r.syntheticIno(dirKey + name + "/")}) {
				return nil
			}
		}
		for _, c := range result.Contents {
			rest, err := validateListedKey(c.Key, dirKey)
			if err != nil {
				return err
			}
			if rest == "" || strings.HasSuffix(rest, "/") {
				continue
			}
			if policy == vfs.HideReserved && vfs.IsReservedName(rest) {
				continue
			}
			if !fn(vfs.DirEntry{Name: rest, Kind: vfs.KindFile, Ino: r.syntheticIno(c.Key)}) {
				return nil
			}
		}

		if !result.IsTruncated || result.NextContinuationToken == "" {
			return nil
		}
		token = result.NextContinuationToken
	}
}

// ReadDir collects entries into a slice, refusing past
// limits.DirEntriesBuffered exactly as vfs.ShareRoot.ReadDir does, for the
// same reason: an unbounded collection here would be bounded only by
// memory, against a listing this package does not control the size of.
func (r *Root) ReadDir(p vfs.SafePath, policy vfs.ReservedPolicy) ([]vfs.DirEntry, error) {
	out := make([]vfs.DirEntry, 0, 64)
	over := false
	err := r.ReadDirFunc(p, policy, func(e vfs.DirEntry) bool {
		if len(out) >= limits.DirEntriesBuffered {
			over = true
			return false
		}
		out = append(out, e)
		return true
	})
	if err != nil {
		return nil, err
	}
	if over {
		return nil, limits.Exceed("directory entries buffered", limits.DirEntriesBuffered, int64(limits.DirEntriesBuffered)+1)
	}
	return out, nil
}

// OpenRead materializes key whole into an unlinked scratch file and hands
// back the descriptor: the file has no name left in scratch space by the
// time this returns, so the space it holds is freed the moment the caller
// closes the handle, with nothing else needing to sweep it.
func (r *Root) OpenRead(p vfs.SafePath, _ vfs.AccessIntent) (*vfs.File, error) {
	key := r.objectKey(p)
	f, scratchPath, err := r.newScratchFile()
	if err != nil {
		return nil, err
	}
	if _, err := r.getObject(context.Background(), key, f); err != nil {
		err = errors.Join(err, r.scratch.Unlink(scratchPath), f.Close())
		return nil, fmt.Errorf("open read: %w", err)
	}
	if err := r.scratch.Unlink(scratchPath); err != nil {
		return nil, fmt.Errorf("open read: unlink scratch file: %w", errors.Join(err, f.Close()))
	}
	return f, nil
}

// CreatePart hands the caller a fresh scratch file and remembers it against
// p, the same SafePath PublishPart is later called with, since that is the
// only handle this package has on which upload the eventual publish means.
func (r *Root) CreatePart(p vfs.SafePath) (*vfs.File, error) {
	f, scratchPath, err := r.newScratchFile()
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.parts[p.String()] = partHandle{scratch: scratchPath}
	r.mu.Unlock()
	return f, nil
}

// PublishPart uploads the part file CreatePart(part) produced as dest, then
// discards the scratch file: there is nothing left to publish a second way,
// unlike a local share where the same file is renamed into place.
//
// The scratch file is reopened by name rather than read through the handle
// CreatePart returned. That handle belongs to the caller, and the upload
// engine syncs and closes it before publishing, exactly as a local share
// wants; reading it here answered "bad file descriptor" and lost every
// upload into a bucket.
func (r *Root) PublishPart(part, dest vfs.SafePath, replacing bool) (dur vfs.Durable, err error) {
	r.mu.Lock()
	ph, ok := r.parts[part.String()]
	if ok {
		delete(r.parts, part.String())
	}
	r.mu.Unlock()
	if !ok {
		return vfs.Durable{}, fmt.Errorf("publish part: %w", vfs.ErrNotFound)
	}
	defer func() { err = errors.Join(err, r.scratch.Unlink(ph.scratch)) }()

	staged, err := r.scratch.OpenRead(ph.scratch, vfs.IntentRead)
	if err != nil {
		return vfs.Durable{}, fmt.Errorf("publish part: reopen the staged file: %w", err)
	}
	defer func() { err = errors.Join(err, staged.Close()) }()

	st, err := staged.Stat()
	if err != nil {
		return vfs.Durable{}, fmt.Errorf("publish part: %w", err)
	}

	ctx, cancel := r.metadataCtx()
	defer cancel()
	key := r.objectKey(dest)
	existed, err := r.exists(ctx, key)
	if err != nil {
		return vfs.Durable{}, fmt.Errorf("publish part: %w", err)
	}
	if existed && !replacing {
		return vfs.Durable{}, fmt.Errorf("publish part: %w", vfs.ErrExists)
	}
	if err = r.putObject(context.Background(), key, staged, st.Size); err != nil {
		return vfs.Durable{}, fmt.Errorf("publish part: %w", err)
	}
	return vfs.Durable{Replaced: existed}, nil
}

// WriteDurable runs write against a scratch file, then uploads it whole:
// there is no atomic rename to fall back on against a bucket, so the
// closest available guarantee is that a failed write or a failed upload
// never touches the object write is replacing.
func (r *Root) WriteDurable(p vfs.SafePath, opt vfs.DurableOpts, write func(*vfs.File) error) (dur vfs.Durable, err error) {
	f, scratchPath, err := r.newScratchFile()
	if err != nil {
		return vfs.Durable{}, err
	}
	defer func() {
		err = errors.Join(err, r.scratch.Unlink(scratchPath), f.Close())
	}()

	if err = write(f); err != nil {
		return vfs.Durable{}, err
	}
	if err = f.SyncData(); err != nil {
		return vfs.Durable{}, fmt.Errorf("write durable: sync scratch file: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		return vfs.Durable{}, fmt.Errorf("write durable: %w", err)
	}

	ctx, cancel := r.metadataCtx()
	defer cancel()
	key := r.objectKey(p)
	existed, err := r.exists(ctx, key)
	if err != nil {
		return vfs.Durable{}, fmt.Errorf("write durable: %w", err)
	}
	if existed && opt.NoClobber {
		return vfs.Durable{}, fmt.Errorf("write durable: %w", vfs.ErrExists)
	}
	if err = r.putObject(context.Background(), key, f, st.Size); err != nil {
		return vfs.Durable{}, fmt.Errorf("write durable: %w", err)
	}
	return vfs.Durable{Replaced: existed}, nil
}

// Mkdir writes the zero-byte directory marker. It refuses only when a
// marker already occupies the name; a prefix that already has children but
// no marker of its own already lists as a directory, and writing the
// marker on top of that is the expected way it gains one.
func (r *Root) Mkdir(p vfs.SafePath) error {
	if p.IsRoot() {
		return fmt.Errorf("mkdir: %w", vfs.ErrExists)
	}
	ctx, cancel := r.metadataCtx()
	defer cancel()
	markerKey := r.dirPrefix(p)
	markerExists, err := r.exists(ctx, markerKey)
	if err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if markerExists {
		return fmt.Errorf("mkdir: %w", vfs.ErrExists)
	}
	if err := r.putEmptyObject(ctx, markerKey); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return nil
}

// Rmdir refuses a directory that has any child object or child prefix,
// counting neither the directory's own marker nor an absent one against
// it, and otherwise deletes the marker if one is present.
func (r *Root) Rmdir(p vfs.SafePath) error {
	if p.IsRoot() {
		return fmt.Errorf("rmdir: %w", vfs.ErrDenied)
	}
	ctx, cancel := r.metadataCtx()
	defer cancel()
	dirKey := r.dirPrefix(p)
	result, err := r.listObjectsV2(ctx, dirKey, "/", "", 2)
	if err != nil {
		return fmt.Errorf("rmdir: %w", err)
	}
	markerFound := false
	nonEmpty := len(result.CommonPrefixes) > 0
	for _, c := range result.Contents {
		if c.Key == dirKey {
			markerFound = true
			continue
		}
		nonEmpty = true
	}
	if nonEmpty {
		return fmt.Errorf("rmdir: %w", vfs.ErrNotEmpty)
	}
	if !markerFound {
		return fmt.Errorf("rmdir: %w", vfs.ErrNotFound)
	}
	if err := r.deleteObjectForce(ctx, dirKey); err != nil {
		return fmt.Errorf("rmdir: %w", err)
	}
	return nil
}

// Unlink deletes a file. A name that turns out to be a directory is refused
// with ErrIsDirectory, matching ShareRoot's own EISDIR mapping, rather than
// with ErrNotFound, which would be true only of the exact key and false of
// what the caller actually named.
func (r *Root) Unlink(p vfs.SafePath) error {
	if p.IsRoot() {
		return fmt.Errorf("unlink: %w", vfs.ErrDenied)
	}
	ctx, cancel := r.metadataCtx()
	defer cancel()
	key := r.objectKey(p)
	found, err := r.exists(ctx, key)
	if err != nil {
		return fmt.Errorf("unlink: %w", err)
	}
	if !found {
		isDir, _, err := r.isDirectory(ctx, key)
		if err != nil {
			return fmt.Errorf("unlink: %w", err)
		}
		if isDir {
			return fmt.Errorf("unlink: %w", vfs.ErrIsDirectory)
		}
		return fmt.Errorf("unlink: %w", vfs.ErrNotFound)
	}
	if err := r.deleteObjectForce(ctx, key); err != nil {
		return fmt.Errorf("unlink: %w", err)
	}
	return nil
}

// Rename moves a file with one CopyObject and one DeleteObject, or a
// directory by doing the same over every key the source prefix lists,
// bounded by maxRenameEntries so a tree larger than this package is willing
// to walk is refused before anything is copied, rather than partway through.
func (r *Root) Rename(from, to vfs.SafePath, noReplace bool) error {
	fromStat, err := r.Stat(from)
	if err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	if fromStat.Kind.IsDir() {
		return r.renameDir(from, to, noReplace)
	}
	return r.renameFile(from, to, noReplace)
}

func (r *Root) renameFile(from, to vfs.SafePath, noReplace bool) error {
	ctx, cancel := r.metadataCtx()
	defer cancel()
	fromKey, toKey := r.objectKey(from), r.objectKey(to)
	if noReplace {
		exists, err := r.exists(ctx, toKey)
		if err != nil {
			return fmt.Errorf("rename: %w", err)
		}
		if exists {
			return fmt.Errorf("rename: %w", vfs.ErrExists)
		}
	}
	if err := r.copyObject(ctx, fromKey, toKey); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	if err := r.deleteObjectForce(ctx, fromKey); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func (r *Root) renameDir(from, to vfs.SafePath, noReplace bool) error {
	ctx, cancel := r.metadataCtx()
	defer cancel()
	fromPrefix, toPrefix := r.dirPrefix(from), r.dirPrefix(to)

	if noReplace {
		destExists, _, err := r.isDirectory(ctx, r.objectKey(to))
		if err != nil {
			return fmt.Errorf("rename: %w", err)
		}
		if destExists {
			return fmt.Errorf("rename: %w", vfs.ErrExists)
		}
	}

	keys, err := r.listAllUnderPrefix(ctx, fromPrefix)
	if err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	for _, k := range keys {
		dest := toPrefix + strings.TrimPrefix(k, fromPrefix)
		if err := r.copyObject(ctx, k, dest); err != nil {
			return fmt.Errorf("rename: %w", err)
		}
	}
	for _, k := range keys {
		if err := r.deleteObjectForce(ctx, k); err != nil {
			return fmt.Errorf("rename: %w", err)
		}
	}
	return nil
}

// listAllUnderPrefix lists every key at or beneath prefix, flat rather than
// delimited, refusing once the count crosses maxRenameEntries rather than
// after copying that many objects.
func (r *Root) listAllUnderPrefix(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		result, err := r.listObjectsV2(ctx, prefix, "", token, 1000)
		if err != nil {
			return nil, err
		}
		for _, c := range result.Contents {
			keys = append(keys, c.Key)
			if len(keys) > maxRenameEntries {
				return nil, fmt.Errorf("more than %d entries under %q, refusing to rename", maxRenameEntries, prefix)
			}
		}
		if !result.IsTruncated || result.NextContinuationToken == "" {
			return keys, nil
		}
		token = result.NextContinuationToken
	}
}
