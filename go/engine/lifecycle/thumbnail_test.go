//go:build linux

package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// buildJailedWorker compiles the shipped decoder once per test binary.
//
// A closure over sync.OnceValues rather than three package variables: the
// cache is what the memoisation is for, and nothing outside this file has any
// business reading it.
var buildJailedWorker = sync.OnceValues(func() (string, error) { //nolint:gochecknoglobals // one build per test binary, which is what the memoisation is for.
	dir, err := os.MkdirTemp("", "jailedworker")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "jailedworker")
	//nolint:gosec // G204: every argument is this test's own constant.
	cmd := exec.Command("go", "build", "-o", bin,
		"github.com/heavycaffeiner/stowcloud/go/engine/service/preview/worker/jailedworker")
	if out, berr := cmd.CombinedOutput(); berr != nil {
		return "", errors.New(string(out))
	}
	return bin, nil
})

// jailedWorker returns the shipped decoder's path.
//
// The real one, not a stand-in. The bytes this route returns are produced by
// a process in a sandbox, and a test driving a substitute would prove the
// handler works while saying nothing about whether the product's decoder can
// be reached at all.
func jailedWorker(t *testing.T) string {
	t.Helper()
	bin, err := buildJailedWorker()
	if err != nil {
		t.Skipf("the decoder could not be built: %v", err)
	}
	return bin
}

// samplePNG encodes an image with a known colour in a known corner.
//
// Asymmetric on purpose: a thumbnail that came back rotated or mirrored would
// still be a valid PNG of the right size, and a uniform fill would hide it.
func samplePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{R: 20, G: 20, B: 20, A: 255}
			if x < w/2 && y < h/2 {
				c = color.RGBA{R: 240, G: 30, B: 30, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding the sample: %v", err)
	}
	return buf.Bytes()
}

// thumbShare serves a share holding one image, with the decoder wired.
func thumbShare(t *testing.T, perms acl.Perms, img []byte) (base, token, share, host string) {
	t.Helper()
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{
		DataDir:       t.TempDir(),
		PreviewWorker: jailedWorker(t),
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	if e.Preview == nil {
		t.Skip("this host builds no decoder pool, so there is nothing to drive")
	}

	id, err := e.Auth.CreateUser(ctx, "alice", "Alice",
		secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatal(err)
	}

	host = t.TempDir()
	if werr := os.WriteFile(filepath.Join(host, "photo.png"), img, 0o600); werr != nil {
		t.Fatal(werr)
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "pics", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &id, Share: sh.ID, Allow: perms, Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}

	appPW, err := e.Auth.CreateAppPassword(ctx, id, "test",
		auth.Scope{Perms: auth.SyncScopePerms}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return serve(t, e), appPW, sh.Name, host
}

// thumbnail requests one, at the named size when given.
func thumbnail(t *testing.T, base, token, path, size string) (int, http.Header, []byte) {
	t.Helper()

	url := base + "/api/v1/files/thumbnail?path=" + urlEscape(path)
	if size != "" {
		url += "&size=" + size
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if token != "" {
		req.SetBasicAuth("ignored", token)
	}

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	return resp.StatusCode, resp.Header, readAll(t, resp)
}

// A thumbnail comes back as a PNG the standard library can decode.
//
// Decoded rather than merely counted, because a truncated or misdeclared body
// is still bytes: the interface would show a broken image and the route would
// have reported success.
func TestAThumbnailIsADecodablePNG(t *testing.T) {
	base, token, share, _ := thumbShare(t, everyPerm(), samplePNG(t, 400, 300))

	status, header, body := thumbnail(t, base, token, "/"+share+"/photo.png", "")
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, body)
	}

	if ct := header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("the thumbnail is typed %q", ct)
	}
	// Without a length the transfer is chunked, so a client cannot tell a
	// complete image from one whose connection ended early.
	if header.Get("Content-Length") == "" {
		t.Error("no Content-Length, so a truncated thumbnail is undetectable")
	}
	// Private, not public. A shared proxy honouring a public directive would
	// serve one account's thumbnail to the next caller asking for the same
	// URL, and the URL carries only a path.
	cc := header.Get("Cache-Control")
	if !strings.Contains(cc, "private") {
		t.Errorf("Cache-Control is %q, so a shared cache may keep this thumbnail", cc)
	}
	if strings.Contains(cc, "public") {
		t.Errorf("Cache-Control is %q, which invites a shared cache to serve it to another account", cc)
	}

	// Set by the chain rather than the handler, which is why this asserts on
	// the delivered response instead of on what the handler writes.
	if header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("no nosniff, so a browser may guess a type other than the declared one")
	}

	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("the body is not a PNG: %v", err)
	}
	got := img.Bounds().Size()
	if got.X <= 0 || got.Y <= 0 {
		t.Fatalf("the thumbnail is %dx%d", got.X, got.Y)
	}
	// Smaller than the original in at least one dimension, which is what
	// makes it a thumbnail rather than the file re-encoded.
	if got.X >= 400 && got.Y >= 300 {
		t.Errorf("the thumbnail is %dx%d, no smaller than the 400x300 original", got.X, got.Y)
	}

	// The declared length is the delivered length. A body shorter than its
	// header is exactly the failure the header exists to make visible.
	if cl := header.Get("Content-Length"); cl != "" {
		if want := strconv.Itoa(len(body)); cl != want {
			t.Errorf("Content-Length is %s and %s bytes arrived", cl, want)
		}
	}
}

// The image survives the re-encode with its geometry intact.
//
// The sample is red in one corner and dark everywhere else. A thumbnail that
// came back rotated, mirrored, or of a different file would still decode, so
// the corner is what proves the pixels are the ones that went in.
func TestAThumbnailKeepsTheImageOrientation(t *testing.T) {
	base, token, share, _ := thumbShare(t, everyPerm(), samplePNG(t, 400, 300))

	status, _, body := thumbnail(t, base, token, "/"+share+"/photo.png", "")
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, body)
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("the body is not a PNG: %v", err)
	}

	b := img.Bounds()
	sample := func(fx, fy float64) color.Color {
		x := b.Min.X + int(float64(b.Dx())*fx)
		y := b.Min.Y + int(float64(b.Dy())*fy)
		return img.At(x, y)
	}
	reddish := func(c color.Color) bool {
		r, g, bl, _ := c.RGBA()
		return r > g*2 && r > bl*2
	}

	if !reddish(sample(0.25, 0.25)) {
		t.Error("the top left is not the colour that went in, so the image was flipped or replaced")
	}
	if reddish(sample(0.75, 0.75)) {
		t.Error("the bottom right carries the top left's colour, so the image was flipped")
	}
}

// A file nothing can decode is refused rather than answered with a broken image.
func TestAThumbnailOfAnUndecodableFileIsRefused(t *testing.T) {
	base, token, share, host := thumbShare(t, everyPerm(), samplePNG(t, 64, 64))

	if werr := os.WriteFile(filepath.Join(host, "notes.txt"),
		[]byte("this is not an image"), 0o600); werr != nil {
		t.Fatal(werr)
	}

	status, _, _ := thumbnail(t, base, token, "/"+share+"/notes.txt", "")
	if status == http.StatusOK {
		t.Error("a text file produced a thumbnail")
	}
}

// A thumbnail needs the permission the file itself needs.
//
// It derives from the bytes, so serving one to an account that may not read
// them hands over a downscaled copy of a file it cannot open.
func TestAThumbnailNeedsTheFilesPermission(t *testing.T) {
	// Listing without downloading: the grant shows the name and withholds the
	// bytes, which is exactly the case a thumbnail would leak.
	base, token, share, _ := thumbShare(t, acl.Read, samplePNG(t, 200, 200))

	status, _, _ := thumbnail(t, base, token, "/"+share+"/photo.png", "")
	if status == http.StatusOK {
		t.Error("an account that cannot download the file received a thumbnail of it")
	}
}

// A thumbnail is not served for a path outside the share.
func TestAThumbnailCannotEscapeTheShare(t *testing.T) {
	base, token, share, _ := thumbShare(t, everyPerm(), samplePNG(t, 64, 64))

	for _, p := range []string{
		"/" + share + "/../../etc/passwd",
		"/" + share + "/%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		"/../etc/passwd",
	} {
		status, _, _ := thumbnail(t, base, token, p, "")
		if status == http.StatusOK {
			t.Errorf("%q produced a thumbnail", p)
		}
	}
}

// An unknown size is refused rather than rounded to the nearest.
func TestAnUnknownThumbnailSizeIsRefused(t *testing.T) {
	base, token, share, _ := thumbShare(t, everyPerm(), samplePNG(t, 200, 200))

	status, _, _ := thumbnail(t, base, token, "/"+share+"/photo.png", "enormous")
	if status == http.StatusOK {
		t.Error("a size nobody defined was accepted")
	}
}

// The three defined sizes differ, so asking for one is not asking for another.
func TestTheThumbnailSizesDiffer(t *testing.T) {
	base, token, share, _ := thumbShare(t, everyPerm(), samplePNG(t, 800, 600))

	seen := make(map[string]image.Point, 3)
	for _, size := range []string{"small", "medium", "large"} {
		status, _, body := thumbnail(t, base, token, "/"+share+"/photo.png", size)
		if status != http.StatusOK {
			t.Fatalf("%s answered %d", size, status)
		}
		img, err := png.Decode(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("%s is not a PNG: %v", size, err)
		}
		seen[size] = img.Bounds().Size()
	}

	if seen["small"] == seen["large"] {
		t.Errorf("small and large are both %v, so the size is ignored", seen["small"])
	}
	if seen["small"].X > seen["large"].X {
		t.Errorf("small is %v and large is %v, which is backwards", seen["small"], seen["large"])
	}
}

// A thumbnail needs a credential.
func TestAThumbnailNeedsACredential(t *testing.T) {
	base, _, share, _ := thumbShare(t, everyPerm(), samplePNG(t, 64, 64))

	status, _, _ := thumbnail(t, base, "", "/"+share+"/photo.png", "")
	if status == http.StatusOK {
		t.Error("a thumbnail was served without a credential")
	}
}

func TestThumbnailSettingCanBeToggledOff(t *testing.T) {
	ctx := context.Background()
	e, err := lifecycle.Open(ctx, lifecycle.Options{
		DataDir:       t.TempDir(),
		PreviewWorker: jailedWorker(t),
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	if e.Preview == nil {
		t.Skip("this host builds no decoder pool, so there is nothing to drive")
	}

	_, err = e.Auth.CreateAdmin(ctx, "admin", "Admin", secret.New([]byte("admin-password-123")))
	if err != nil {
		t.Fatal(err)
	}

	userID, err := e.Auth.CreateUser(ctx, "alice", "Alice", secret.New([]byte("a-long-enough-password")))
	if err != nil {
		t.Fatal(err)
	}
	host := t.TempDir()
	if werr := os.WriteFile(filepath.Join(host, "photo.png"), samplePNG(t, 64, 64), 0o600); werr != nil {
		t.Fatal(werr)
	}
	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "pics", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	holder := int64(userID)
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &holder, Share: sh.ID, Allow: everyPerm(), Inherit: true,
	}); gerr != nil {
		t.Fatal(gerr)
	}
	appPW, _, err := e.Auth.CreateSyncCredential(ctx, int64(userID), "sync")
	if err != nil {
		t.Fatal(err)
	}
	base := serve(t, e)

	status, _, _ := thumbnail(t, base, appPW, "/pics/photo.png", "")
	if status != http.StatusOK {
		t.Fatalf("thumbnail failed before toggle: %d", status)
	}

	// Sign in as admin to toggle settings
	loginResp := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "admin", "password": "admin-password-123"})
	cookie := loginResp.sessionCookie()
	csrf := loginResp.field("csrf")

	code, resp := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/thumbnail",
		cookie, csrf, map[string]any{"enabled": false})
	if code != http.StatusOK {
		t.Fatalf("patching settings failed: %d %+v", code, resp)
	}

	statusAfter, _, _ := thumbnail(t, base, appPW, "/pics/photo.png", "")
	if statusAfter != http.StatusNotFound {
		t.Errorf("thumbnail was served after being disabled: %d, want 404", statusAfter)
	}
}
