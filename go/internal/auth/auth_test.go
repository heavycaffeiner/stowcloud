package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// argon2idKey derives a raw key without the gate, for tests that need a
// specific parameter set.
func argon2idKey(pw secret.Secret, salt []byte, t, m uint32, p uint8) []byte {
	return argon2.IDKey(pw.Reveal(), salt, t, m, p, 32)
}

func base32Decode(s string) ([]byte, error) {
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
}

func mustFold(t *testing.T, s string) string {
	t.Helper()
	f, err := crockfordFold(s)
	if err != nil {
		t.Fatalf("fold %q: %v", s, err)
	}
	return f
}

const insTestShareLink = `
INSERT INTO share_link(token_hash, token_enc, token_key_ver, share, path,
                       owner, perms, created_ns)
VALUES (?, ?, ?, 1, '', ?, 0, 0)`

// openService builds a Service over a fresh data directory and opens its
// master key, so every test starts from a working, self-consistent store.
func openService(t *testing.T, clk clock.Clock) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(dir, "state.db")))
	if err != nil {
		t.Fatalf("opening state.db: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing state.db: %v", cerr)
		}
	})
	if clk == nil {
		clk = clock.System()
	}
	s := New(Config{Store: state.New(f), StoreDir: dir, Clock: clk, PassdbPath: filepath.Join(dir, "passdb")})
	if _, err := s.OpenMasterKey(context.Background()); err != nil {
		t.Fatalf("opening the master key: %v", err)
	}
	return s, dir
}

// reopenService opens a fresh Service over an existing directory, which is
// how a restart is simulated.
func reopenService(t *testing.T, dir string, clk clock.Clock) *Service {
	t.Helper()
	f, err := dbfile.Open(context.Background(), state.Spec(filepath.Join(dir, "state.db")))
	if err != nil {
		t.Fatalf("reopening state.db: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing state.db: %v", cerr)
		}
	})
	if clk == nil {
		clk = clock.System()
	}
	s := New(Config{Store: state.New(f), StoreDir: dir, Clock: clk, PassdbPath: filepath.Join(dir, "passdb")})
	if _, err := s.OpenMasterKey(context.Background()); err != nil {
		t.Fatalf("reopening the master key: %v", err)
	}
	return s
}

// mutableClock is a clock a test can advance, so an expiry is a fact about
// the data rather than about how long the test took.
type mutableClock struct {
	mu sync.Mutex
	t  time.Time
}

func (m *mutableClock) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.t
}

func (m *mutableClock) Since(t time.Time) time.Duration { return m.Now().Sub(t) }
func (m *mutableClock) Nanos() int64                    { return m.Now().UnixNano() }

func (m *mutableClock) advance(d time.Duration) {
	m.mu.Lock()
	m.t = m.t.Add(d)
	m.mu.Unlock()
}

// A session's absolute lifetime is what dies at the deadline, not the idle
// window: advancing the clock past the configured lifetime revokes it even
// while it would still be idle-fresh.
func TestSessionAbsoluteLifetimeIsHonoured(t *testing.T) {
	clk := &mutableClock{t: time.Unix(0, 0)}
	s, _ := openService(t, clk)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw-correct-horse"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sess, err := s.CreateSession(ctx, 1, "127.0.0.1", "ua", 1, time.Minute)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.LookupSession(ctx, sess.Token); err != nil {
		t.Fatalf("LookupSession within the lifetime: %v", err)
	}
	clk.advance(2 * time.Minute)
	if _, err := s.LookupSession(ctx, sess.Token); !errors.Is(err, ErrCredentials) {
		t.Errorf("a session outlived its configured absolute lifetime: %v", err)
	}
}

// The tier-3 app-password cache carries the scope along with the principal, so
// a cache hit answers every question the enforcement layer asks.
func TestAppPasswordScopeSurvivesTheCache(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw-correct-horse"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tok, err := s.CreateAppPassword(ctx, 1, "desktop", Scope{Perms: 1, Shares: []string{"photos"}}, 0)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	_, first, err := s.VerifyAppPassword(ctx, tok)
	if err != nil {
		t.Fatalf("first VerifyAppPassword: %v", err)
	}
	_, second, err := s.VerifyAppPassword(ctx, tok)
	if err != nil {
		t.Fatalf("cached VerifyAppPassword: %v", err)
	}
	if len(second.Shares) != 1 || second.Shares[0] != "photos" || second.Perms != 1 {
		t.Errorf("the cached scope was lost or wrong: %+v, want photos/1", second)
	}
	if first.Perms != second.Perms || len(first.Shares) != len(second.Shares) {
		t.Errorf("the cached scope differs from the freshly read one: %+v vs %+v", first, second)
	}
}

func TestPasswordPHC(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	pw := secret.New([]byte("correct horse battery staple"))

	enc, err := s.Hash(ctx, pw)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(enc, "$argon2id$") {
		t.Errorf("PHC form %q does not start with $argon2id$", enc)
	}

	ok, stale, err := s.Verify(ctx, enc, pw)
	if err != nil || !ok || stale {
		t.Errorf("Verify(correct) = ok %v stale %v err %v", ok, stale, err)
	}
	ok, _, err = s.Verify(ctx, enc, secret.New([]byte("wrong")))
	if err != nil || ok {
		t.Errorf("Verify(wrong) = ok %v err %v", ok, err)
	}
	if Stale(enc) {
		t.Error("a hash made under CurrentParams is reported stale")
	}
}

// A hash made under weaker parameters still verifies (the stored hash names
// its own costs), and is reported stale so the caller rehashes on success.
func TestVerifyAgainstOlderParameters(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	pw := secret.New([]byte("old-school"))

	// Derive with a deliberately weaker memory cost, then render its PHC.
	salt := []byte("0123456789abcdef")
	key := argon2idKey(pw, salt, 20, 1024, 1)
	enc := encodePHC(Params{MemoryKiB: 1024, Iterations: 20, Parallelism: 1, KeyLen: 32}, salt, key)

	ok, stale, err := s.Verify(ctx, enc, pw)
	if err != nil || !ok {
		t.Fatalf("Verify with older params = ok %v err %v", ok, err)
	}
	if !stale {
		t.Error("a hash with non-current params is not reported stale")
	}
}

// The enumeration defence: an unknown user and a known user with a wrong
// password must answer with the same error and a duration inside a band, so a
// lookup that short-circuits is caught rather than trusted.
func TestEnumerationIsIndistinguishable(t *testing.T) {
	s, _ := openService(t, clock.System())
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("right-password"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	measure := func(name, pw, ip string) (time.Duration, error) {
		var best time.Duration
		var err error
		for i := 0; i < 2; i++ {
			start := s.clk.Now()
			_, err = s.Login(ctx, LoginRequest{Name: name, Password: secret.New([]byte(pw)), IP: ip}, time.Hour)
			elapsed := s.clk.Since(start)
			if i == 0 || elapsed < best {
				best = elapsed
			}
		}
		return best, err
	}

	dWrong, errWrong := measure("alice", "wrong-password", "10.0.0.1")
	if !errors.Is(errWrong, ErrCredentials) {
		t.Fatalf("wrong password error = %v, want ErrCredentials", errWrong)
	}
	dUnknown, errUnknown := measure("nobody", "wrong-password", "10.0.0.2")
	if !errors.Is(errUnknown, ErrCredentials) {
		t.Fatalf("unknown user error = %v, want ErrCredentials", errUnknown)
	}

	// Both ran a full Argon2 verify, so the unknown one cannot be a lookup
	// that short-circuited. The band is wide enough to survive scheduler
	// noise and narrow enough to catch a path that skips the KDF.
	if dUnknown < dWrong/3 {
		t.Errorf("unknown-user login (%.0fms) is far faster than a wrong password (%.0fms); that is an account oracle",
			float64(dUnknown)/1e6, float64(dWrong)/1e6)
	}
	if dUnknown > dWrong*3 {
		t.Errorf("unknown-user login (%.0fms) is far slower than a wrong password (%.0fms)",
			float64(dUnknown)/1e6, float64(dWrong)/1e6)
	}
}

// The concurrency proof: peak in-flight Argon2 invocations never exceed the
// gate, even when eight hash requests are submitted at once.
func TestArgon2NeverRunsBeyondTheGate(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	const workers = 8
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		task.Go(ctx, "gate proof", func() {
			_, err := s.Hash(ctx, secret.New([]byte("some password")))
			errs <- err
		})
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Hash: %v", err)
		}
	}
	if peak := s.gate.PeakConcurrency(); peak > GateConcurrency {
		t.Errorf("peak concurrency %d exceeds the gate of %d", peak, GateConcurrency)
	}
}

// The gate itself blocks: with all permits held, a fifth acquire waits until
// one is released.
func TestGateBlocksWhenFull(t *testing.T) {
	g := NewGate()
	ctx := context.Background()
	var releases []func()
	for i := 0; i < GateConcurrency; i++ {
		r, err := g.Acquire(ctx)
		if err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
		releases = append(releases, r)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a fifth acquire did not block: err = %v", err)
	}
	releases[0]()
	r, err := g.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	r()
}

func readPassdb(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the passdb: %v", err)
	}
	return string(b)
}

// Every one of the six credential-changing paths reaches the SMB passdb sink.
//
// The property is the file's contents after each one, not the database row.
// smbd authenticates against the last file published to it, so a revocation
// that stops at the database leaves the sidecar serving the revoked
// credential.
func TestEveryCredentialPathRepublishesThePassdb(t *testing.T) {
	s, dir := openService(t, nil)
	ctx := context.Background()
	passdb := filepath.Join(dir, "passdb")
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw1-correct-horse"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Published or absent, rather than a marker: a marker is a line the import
	// tool still reads, and the absence is what revokes.
	check := func(what string, wantPublished bool) {
		t.Helper()
		content := readPassdb(t, passdb)
		got := strings.Contains(content, "alice:")
		if got != wantPublished {
			t.Errorf("after %s the passdb published=%v, want %v:\n%s",
				what, got, wantPublished, content)
		}
	}

	// 1. set password.
	if err := s.SetPassword(ctx, 1, secret.New([]byte("pw2-correct-horse"))); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	check("a password change", true)

	// 2. An OIDC link closes local password login, so the credential the file
	// carries is no longer one the account should authenticate with.
	if err := s.LinkOIDC(ctx, 1); err != nil {
		t.Fatalf("LinkOIDC: %v", err)
	}
	check("an OIDC link", false)

	// 3. Unlinking restores it.
	if err := s.UnlinkOIDC(ctx, 1); err != nil {
		t.Fatalf("UnlinkOIDC: %v", err)
	}
	check("an OIDC unlink", true)

	// 4. The per-account SMB toggle.
	if err := s.SetSMBAccess(ctx, 1, false, false); err != nil {
		t.Fatalf("SetSMBAccess off: %v", err)
	}
	check("SMB being turned off", false)
	if err := s.SetSMBAccess(ctx, 1, false, true); err != nil {
		t.Fatalf("SetSMBAccess on: %v", err)
	}
	check("SMB being turned on", true)

	// 5. Enrolling a second factor drops the hash derived from the account
	// password, because keeping it would let that password go on working over
	// the older protocol, bypassing the factor just added.
	secretB32, err := s.GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	if err := s.EnrollTOTP(ctx, 1, secretB32); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	check("a TOTP enrolment", false)

	// 6. Removing it does not resurrect the dropped credential: the hash is
	// gone, and only setting a password again mints one.
	if err := s.DisableTOTP(ctx, 1); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	check("a TOTP removal", false)
	if err := s.SetPassword(ctx, 1, secret.New([]byte("pw3-correct-horse"))); err != nil {
		t.Fatalf("SetPassword after TOTP: %v", err)
	}
	check("a password set after TOTP", true)
}

// The published line is the format the import tool reads, and the two fields
// that decide whether it works at all are the uid and the disabled LANMAN
// marker.
func TestThePassdbLineIsTheFormatSmbdReads(t *testing.T) {
	s, dir := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw1-correct-horse"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.SetPassword(ctx, 1, secret.New([]byte("pw2-correct-horse"))); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	line := strings.TrimSuffix(readPassdb(t, filepath.Join(dir, "passdb")), "\n")
	fields := strings.Split(line, ":")
	// name, uid, lanman, nt, flags, timestamp, and the trailing empty field.
	if len(fields) != 7 {
		t.Fatalf("the line has %d fields, want 7: %q", len(fields), line)
	}
	if fields[0] != "alice" {
		t.Errorf("name = %q", fields[0])
	}
	// The uid has to be the account's row id offset by the shared base, and
	// the account file beside this one has to carry the same number: the
	// import tool resolves a line to an account through it, and imports
	// nothing at all when it names none.
	if got, want := fields[1], fmt.Sprint(SMBBaseUid+1); got != want {
		t.Errorf("uid = %q, want %q", got, want)
	}
	// A LANMAN hash present is a credential an attacker breaks offline in
	// minutes, so the field is always the disabled marker.
	if fields[2] != strings.Repeat("X", 32) {
		t.Errorf("the LANMAN field is not disabled: %q", fields[2])
	}
	if len(fields[3]) != 32 {
		t.Errorf("the NT field is %d characters, want 32: %q", len(fields[3]), fields[3])
	}
	if strings.ToUpper(fields[3]) != fields[3] {
		t.Errorf("the NT field is not upper-case hex: %q", fields[3])
	}
}

// The account file and the passdb have to name the same uid for the same
// account, or the import silently keeps nothing.
func TestTheAccountFileAgreesWithThePassdbOnEveryUid(t *testing.T) {
	s, dir := openService(t, nil)
	ctx := context.Background()
	for i, name := range []string{"alice", "bob", "carol"} {
		if _, err := s.CreateUser(ctx, name, name, secret.New([]byte("pw1-correct-horse"))); err != nil {
			t.Fatalf("CreateUser %s: %v", name, err)
		}
		// Creating an account only ever adds a hash, so it is not one of the
		// paths that republishes. A password set is.
		if err := s.SetPassword(ctx, int64(i+1), secret.New([]byte("pw2-correct-horse"))); err != nil {
			t.Fatalf("SetPassword %s: %v", name, err)
		}
	}

	passwd := filepath.Join(dir, "passwd")
	if err := s.PublishPasswdEntries(ctx, passwd, 1000); err != nil {
		t.Fatalf("PublishPasswdEntries: %v", err)
	}

	uidOf := func(content string, field int) map[string]string {
		out := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
			if line == "" {
				continue
			}
			f := strings.Split(line, ":")
			out[f[0]] = f[field]
		}
		return out
	}

	fromPassdb := uidOf(readPassdb(t, filepath.Join(dir, "passdb")), 1)
	pb, err := os.ReadFile(passwd)
	if err != nil {
		t.Fatalf("reading the passwd file: %v", err)
	}
	fromPasswd := uidOf(string(pb), 2)

	if len(fromPassdb) != 3 {
		t.Fatalf("the passdb carries %d accounts, want 3", len(fromPassdb))
	}
	for name, uid := range fromPassdb {
		if fromPasswd[name] != uid {
			t.Errorf("%s is uid %q in the passdb and %q in the passwd file", name, uid, fromPasswd[name])
		}
	}
	// And every uid is distinct, because the import matches by uid rather than
	// by name: several names on one uid all import as whichever the reverse
	// lookup answers with.
	seen := map[string]string{}
	for name, uid := range fromPassdb {
		if other, dup := seen[uid]; dup {
			t.Errorf("%s and %s share uid %s", name, other, uid)
		}
		seen[uid] = name
	}
}

// An account with no stored hash is left out rather than written with an empty
// credential field, which the import would read as a line.
func TestAnAccountWithNoHashIsNotPublished(t *testing.T) {
	s, dir := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw1-correct-horse"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.SetPassword(ctx, 1, secret.New([]byte("pw2-correct-horse"))); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if _, err := s.st.SQL().ExecContext(ctx, sqlDeleteSMBSecret, 1); err != nil {
		t.Fatalf("deleting the SMB secret: %v", err)
	}
	if err := s.republishPassdb(ctx); err != nil {
		t.Fatalf("republishPassdb: %v", err)
	}
	if got := readPassdb(t, filepath.Join(dir, "passdb")); strings.Contains(got, "alice") {
		t.Fatalf("an account with no hash was published: %q", got)
	}
}

// RFC 6238 SHA-1 test vector, plus the replay guard.
func TestTOTPKnownVectorAndReplayGuard(t *testing.T) {
	// The RFC's shared secret is the ASCII bytes 1..0. The tabulated time 59
	// seconds is counter 1 (floor(59/30)); counter 0 is the classic 755224.
	shared := []byte("12345678901234567890")
	if got := totpAt(shared, 0); got != 755224 {
		t.Errorf("TOTP at step 0 = %d, want 755224", got)
	}
	if got := totpAt(shared, 1); got != 287082 {
		t.Errorf("TOTP at step 1 = %d, want 287082", got)
	}

	s, _ := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw-correct-horse"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	secretB32, gerr := s.GenerateTOTPSecret()
	if gerr != nil {
		t.Fatalf("GenerateTOTPSecret: %v", gerr)
	}
	if err := s.EnrollTOTP(ctx, 1, secretB32); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	// Derive a code at a controlled step and verify it, then verify the same
	// code again, which must be refused as a replay.
	step := s.now() / (totpStepSeconds * 1e9)
	decoded, derr := base32Decode(secretB32)
	if derr != nil {
		t.Fatalf("decoding the secret: %v", derr)
	}
	code := printCode(totpAt(decoded, step))
	nowNs := step * totpStepSeconds * 1e9

	ok, err := s.VerifyTOTP(ctx, 1, code, nowNs)
	if err != nil || !ok {
		t.Fatalf("VerifyTOTP first use = ok %v err %v", ok, err)
	}
	ok, err = s.VerifyTOTP(ctx, 1, code, nowNs)
	if ok || !errors.Is(err, ErrCredentials) {
		t.Errorf("VerifyTOTP replay = ok %v err %v, want refused as ErrCredentials", ok, err)
	}
}

// A session is created, looked up, and dies when revoked.
func TestSessionLifecycle(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw-correct-horse"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sess, err := s.CreateSession(ctx, 1, "127.0.0.1", "ua", 1, time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	p, err := s.LookupSession(ctx, sess.Token)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if p.UserID != 1 {
		t.Errorf("LookupSession principal = %+v, want user 1", p)
	}
	if err := s.RevokeSession(ctx, sess.Token); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := s.LookupSession(ctx, sess.Token); !errors.Is(err, ErrCredentials) {
		t.Errorf("LookupSession after revoke = %v, want ErrCredentials", err)
	}
}

// An app password resolves, and revocation kills it through the tier-3 cache
// immediately rather than after the 60-second TTL.
func TestAppPasswordRevocationIsImmediate(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw-correct-horse"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tok, err := s.CreateAppPassword(ctx, 1, "desktop", Scope{Perms: 1}, 0)
	if err != nil {
		t.Fatalf("CreateAppPassword: %v", err)
	}
	if _, _, err := s.VerifyAppPassword(ctx, tok); err != nil {
		t.Fatalf("VerifyAppPassword: %v", err)
	}
	// The token is now in the tier-3 cache under the current generation.
	hash := sha256.Sum256([]byte(mustFold(t, tok)))
	if _, _, ok := s.cache.TokenLookup(hash, s.Generation()); !ok {
		t.Fatal("the token was not cached after its first verify")
	}
	// A folded re-type still verifies.
	if _, _, err := s.VerifyAppPassword(ctx, strings.ToLower(tok)); err != nil {
		t.Errorf("VerifyAppPassword with a folded retype: %v", err)
	}

	// Find its id and revoke it.
	var id int64
	var wipe int64
	if err := s.st.SQL().QueryRowContext(ctx, sqlReadAppPassword, hash[:]).
		Scan(&id, new(int64), new(string), new(uint16), new([]byte), new(any), &wipe); err != nil {
		t.Fatalf("reading the app password: %v", err)
	}
	if err := s.RevokeAppPassword(ctx, 1, id); err != nil {
		t.Fatalf("RevokeAppPassword: %v", err)
	}
	if _, _, err := s.VerifyAppPassword(ctx, tok); !errors.Is(err, ErrCredentials) {
		t.Errorf("VerifyAppPassword after revoke = %v, want ErrCredentials", err)
	}
}

// A recovery code is single-use: the first use consumes it, the second finds
// nothing.
func TestRecoveryCodeIsSingleUse(t *testing.T) {
	s, _ := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw-correct-horse"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	codes, err := s.GenerateRecoveryCodes(ctx, 1, 4)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	used, err := s.UseRecoveryCode(ctx, 1, strings.ToLower(codes[0]))
	if err != nil || !used {
		t.Fatalf("UseRecoveryCode first = used %v err %v", used, err)
	}
	used, err = s.UseRecoveryCode(ctx, 1, codes[0])
	if err != nil || used {
		t.Errorf("UseRecoveryCode second = used %v err %v, want refused", used, err)
	}
}

// insertShareLink seals a recoverable owner copy of a token under the active
// key and stores it, so rotation has a link ciphertext to re-seal.
func insertShareLink(t *testing.T, s *Service, userID int64) []byte {
	t.Helper()
	ctx := context.Background()
	token := []byte("the-recoverable-token")
	tokenHash := sha256.Sum256(token)
	key, ver := s.mk.Active()
	ct, err := sealLink(key, token, tokenHash[:], ver)
	if err != nil {
		t.Fatalf("sealLink: %v", err)
	}
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, insTestShareLink, tokenHash[:], ct, ver, userID)
		return err
	}); err != nil {
		t.Fatalf("inserting the share link: %v", err)
	}
	return tokenHash[:]
}

// A rotation re-seals every TOTP, SMB and share-link ciphertext under the new
// key, sets the database version, and compacts the ring to the new key only.
// A restart then opens with the new key.
func TestRotationResealsAndCompacts(t *testing.T) {
	s, dir := openService(t, nil)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "Alice", secret.New([]byte("pw1-correct-horse"))); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := s.SetPassword(ctx, 1, secret.New([]byte("pw2-correct-horse"))); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	// The second factor goes on a second account, because enrolling one drops
	// the NT hash derived from the account password and this needs one of each
	// ciphertext to re-seal.
	if _, err := s.CreateUser(ctx, "bob", "Bob", secret.New([]byte("pw1-correct-horse"))); err != nil {
		t.Fatalf("CreateUser bob: %v", err)
	}
	secretB32, gerr := s.GenerateTOTPSecret()
	if gerr != nil {
		t.Fatalf("GenerateTOTPSecret: %v", gerr)
	}
	if err := s.EnrollTOTP(ctx, 2, secretB32); err != nil {
		t.Fatalf("EnrollTOTP: %v", err)
	}
	tokenHash := insertShareLink(t, s, 1)

	rep, err := s.RotateMasterKey(ctx)
	if err != nil {
		t.Fatalf("RotateMasterKey: %v", err)
	}
	if rep.NewVersion != 2 || rep.OldVersion != 1 {
		t.Errorf("rotation versions = old %d new %d, want 1 -> 2", rep.OldVersion, rep.NewVersion)
	}
	if rep.SMBBrought != 1 || rep.TOTPBrought != 1 || rep.LinksBrought != 1 {
		t.Errorf("rotation counts = smb %d totp %d links %d, want 1 each",
			rep.SMBBrought, rep.TOTPBrought, rep.LinksBrought)
	}

	// The ring holds only the new key.
	if len(s.mk.order) != 1 || s.mk.order[0] != 2 {
		t.Errorf("ring after rotation has versions %v, want just 2", s.mk.order)
	}
	newKey, _ := s.mk.Active()

	// Every ciphertext opens under the new key.
	var nt []byte
	var ntVer uint32
	if err := s.st.SQL().QueryRowContext(ctx, sqlForEachSMB).Scan(new(int64), &nt, &ntVer); err != nil {
		t.Fatalf("reading the SMB secret: %v", err)
	}
	if _, err := openNT(newKey, nt, 1, 2); err != nil {
		t.Errorf("the rotated SMB secret does not open under the new key: %v", err)
	}
	var totp []byte
	var totpVer uint32
	if err := s.st.SQL().QueryRowContext(ctx, sqlForEachTOTP).Scan(new(int64), &totp, &totpVer); err != nil {
		t.Fatalf("reading the TOTP secret: %v", err)
	}
	if _, err := openTOTP(newKey, totp, 2, 2); err != nil {
		t.Errorf("the rotated TOTP secret does not open under the new key: %v", err)
	}
	var linkCT []byte
	var linkVer uint32
	if err := s.st.SQL().QueryRowContext(ctx, sqlForEachLink).Scan(new(int64), new([]byte), &linkCT, &linkVer); err != nil {
		t.Fatalf("reading the share link: %v", err)
	}
	if _, err := openLink(newKey, linkCT, tokenHash, 2); err != nil {
		t.Errorf("the rotated share-link ciphertext does not open under the new key: %v", err)
	}

	// A restart finds the key the database names.
	s2 := reopenService(t, dir, nil)
	if got := s2.mk.newest; got != 2 {
		t.Errorf("restart selected key version %d, want 2", got)
	}
}

// Fault injection: a crash at each boundary of the rotation protocol must
// leave startup able to find the key the committed database names.
func TestRotationFaultInjection(t *testing.T) {
	ctx := context.Background()

	// Crash after step 1: the ring holds old and new, the database never
	// committed the new version. Startup discards the new key.
	step1 := func(dir string) {
		s := reopenService(t, dir, nil)
		extended, _ := s.mk.withNewKey()
		if err := extended.persist(); err != nil {
			t.Fatalf("persisting the two-key ring: %v", err)
		}
	}
	fault := func(t *testing.T, dir string, wantVer uint32) {
		t.Helper()
		s := reopenService(t, dir, nil)
		if got := s.mk.newest; got != wantVer {
			t.Errorf("after the fault, startup selected key %d, want %d", got, wantVer)
		}
		if len(s.mk.order) != 1 {
			t.Errorf("after the fault, the ring holds %v, want a single key", s.mk.order)
		}
	}

	// Scenario one: crash after step 1, database still on version 1.
	dir1 := t.TempDir()
	f1, err := dbfile.Open(ctx, state.Spec(filepath.Join(dir1, "state.db")))
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f1.Close(); cerr != nil {
			t.Errorf("closing state.db: %v", cerr)
		}
	})
	st1 := state.New(f1)
	s1 := New(Config{Store: st1, StoreDir: dir1, Clock: clock.System()})
	if _, kerr := s1.OpenMasterKey(ctx); kerr != nil {
		t.Fatalf("OpenMasterKey: %v", kerr)
	}
	step1(dir1)
	fault(t, dir1, 1)

	// Scenario two: crash after step 2, the database named the new version
	// but the ring was never compacted. Startup finishes the compaction.
	dir2 := t.TempDir()
	f2, err := dbfile.Open(ctx, state.Spec(filepath.Join(dir2, "state.db")))
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f2.Close(); cerr != nil {
			t.Errorf("closing state.db: %v", cerr)
		}
	})
	st2 := state.New(f2)
	s2 := New(Config{Store: st2, StoreDir: dir2, Clock: clock.System()})
	if _, kerr := s2.OpenMasterKey(ctx); kerr != nil {
		t.Fatalf("OpenMasterKey: %v", kerr)
	}
	step1(dir2)
	if err := st2.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlWriteKeyVersion, 2)
		return err
	}); err != nil {
		t.Fatalf("writing the committed version: %v", err)
	}
	fault(t, dir2, 2)
}
