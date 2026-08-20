//go:build compat_nc

package nc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The share shape's field types are not uniform in the reference and clients
// depend on the inconsistency, so making them uniform is a change every client
// would notice.

func shareField(t *testing.T, s Share, key string) Val {
	t.Helper()
	got, ok := FormatShare(s).Get(key)
	if !ok {
		t.Fatalf("the share has no %q", key)
	}
	return got
}

func TestTheShareFieldTypesAreTheReferencesAndNotUniform(t *testing.T) {
	s := Share{ID: 7, Kind: GranteeUser, Perms: ncport.Read, Grantee: "bob"}

	// The id is a string, because the reference's own accessor returns one.
	if got := shareField(t, s, "id"); got.Kind != KindString || got.Str != "7" {
		t.Fatalf("id = %+v, want the string \"7\"", got)
	}
	// These three are real booleans.
	for _, key := range []string{"can_edit", "can_delete", "has_preview"} {
		if got := shareField(t, s, key); got.Kind != KindBool {
			t.Fatalf("%s is %v, want a boolean", key, got.Kind)
		}
	}
	// And these two are integers sitting right beside them.
	for _, key := range []string{"mail_send", "hide_download"} {
		if got := shareField(t, s, key); got.Kind != KindInt {
			t.Fatalf("%s is %v, want an integer", key, got.Kind)
		}
	}
	// The parent is hardcoded absent and never overwritten.
	if got := shareField(t, s, "parent"); got.Kind != KindNull {
		t.Fatalf("parent = %+v, want null", got)
	}
}

// The password is never the real password.
func TestALinkPasswordIsNeverReported(t *testing.T) {
	withPassword := Share{ID: 1, Kind: GranteeLink, HasPassword: true, Token: "tok"}
	got := shareField(t, withPassword, "password")
	if got.Str != redactedPassword {
		t.Fatalf("password = %q, want the placeholder", got.Str)
	}
	// And the same value appears in the other field a client reads.
	if shareField(t, withPassword, "share_with").Str != redactedPassword {
		t.Fatal("share_with carries something other than the placeholder")
	}

	none := Share{ID: 1, Kind: GranteeLink, Token: "tok"}
	if got := shareField(t, none, "password"); got.Kind != KindNull {
		t.Fatalf("a link with no password reports %+v, want null", got)
	}
}

// The expiry format is the reference's and deliberately not a full timestamp:
// clients parse it with a fixed pattern, and a separator or a zone breaks them.
func TestTheExpiryFormatIsTheOneClientsParse(t *testing.T) {
	at := int64(1751234567)
	got := shareField(t, Share{ID: 1, ExpiresS: &at}, "expiration")
	if strings.ContainsAny(got.Str, "TZ+") {
		t.Fatalf("expiration = %q, which carries a separator or a zone", got.Str)
	}
	if len(got.Str) != len("2006-01-02 15:04:05") {
		t.Fatalf("expiration = %q, want the fixed-width form", got.Str)
	}
	if got := shareField(t, Share{ID: 1}, "expiration"); got.Kind != KindNull {
		t.Fatalf("an unexpiring share reports %+v, want null", got)
	}
}

// The type is never sniffed or guessed: a server asserting one risks it being
// trusted for a serving decision.
func TestTheContentTypeIsNeverGuessed(t *testing.T) {
	file := shareField(t, Share{ID: 1, Path: "a.jpg"}, "mimetype")
	if file.Str != "application/octet-stream" {
		t.Fatalf("a .jpg reports %q; the extension was guessed from", file.Str)
	}
	dir := shareField(t, Share{ID: 1, IsDir: true}, "mimetype")
	if dir.Str != "httpd/unix-directory" {
		t.Fatalf("a folder reports %q", dir.Str)
	}
}

// A readable share whose download is withheld reports the flag, which is what
// hides the button in the client.
func TestHideDownloadFollowsThePermission(t *testing.T) {
	readOnly := Share{ID: 1, Perms: ncport.Read}
	if got := shareField(t, readOnly, "hide_download"); got.Int != 1 {
		t.Fatalf("hide_download = %d for a share with no download right", got.Int)
	}
	full := Share{ID: 1, Perms: ncport.Read | ncport.Download}
	if got := shareField(t, full, "hide_download"); got.Int != 0 {
		t.Fatalf("hide_download = %d for a downloadable share", got.Int)
	}
}

// The last three fields are in this order, because a client reading the XML
// form reads it positionally.
func TestTheTrailingFieldsAreInOrder(t *testing.T) {
	v := FormatShare(Share{ID: 1})
	n := len(v.Map)
	want := []string{"mail_send", "hide_download", "attributes"}
	for i, key := range want {
		got := v.Map[n-3+i].Key
		if got != key {
			t.Fatalf("trailing field %d is %q, want %q", i, got, key)
		}
	}
}

func TestOnlyTheOfferedShareTypesAreAccepted(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want GranteeKind
	}{
		{ShareTypeUser, GranteeUser},
		{ShareTypeGroup, GranteeGroup},
		{ShareTypePublicLink, GranteeLink},
	} {
		got, err := KindOfShareType(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("KindOfShareType(%d) = %v, %v", tc.in, got, err)
		}
	}
	for _, bad := range []int64{-1, 2, 4, 99} {
		if _, err := KindOfShareType(bad); err == nil {
			t.Errorf("share type %d was accepted", bad)
		}
	}
}

// A malformed path lists everything the caller owns rather than filtering to
// nothing, which is what the reference does with an absent path. Treating it
// as a filter nobody matches would silently hide every share.
func TestAnUnusableFilterPathListsEverything(t *testing.T) {
	for _, bad := range []string{"../secret", "a/../..", "  "} {
		if got := NormaliseClientPath(bad); got != "" {
			t.Fatalf("NormaliseClientPath(%q) = %q, want it treated as absent", bad, got)
		}
	}
	// A folder path arrives from one client with a trailing separator.
	if got := NormaliseClientPath("/docs/photos/"); got != "docs/photos" {
		t.Fatalf("NormaliseClientPath = %q", got)
	}
}

// The three flags are compared against the literal string, so other spellings
// are false there and are false here too.
func TestTheFilterFlagsAreLiteral(t *testing.T) {
	form := map[string]string{
		"reshares": "1", "subfiles": "TRUE", "shared_with_me": "true",
	}
	f := ParseShareFilter(func(k string) string { return form[k] })
	if f.Reshares || f.Subfiles {
		t.Fatalf("a non-literal spelling was read as true: %+v", f)
	}
	if !f.SharedWithMe {
		t.Fatal("the literal spelling was not read as true")
	}
}

// An absent permission set means "leave it alone" on an update, where an empty
// one would mean "remove everything".
func TestAnAbsentPermissionSetIsNotZero(t *testing.T) {
	form := map[string]string{"path": "a.txt"}
	has := func(k string) bool { _, ok := form[k]; return ok }
	get := func(k string) string { return form[k] }

	r := ShareRequestFromForm(get, has)
	if r.Permissions != nil {
		t.Fatalf("an absent permission set became %d", *r.Permissions)
	}

	form["permissions"] = "0"
	r = ShareRequestFromForm(get, has)
	if r.Permissions == nil || *r.Permissions != 0 {
		t.Fatalf("an explicit zero was lost: %v", r.Permissions)
	}
}

// The thumbnail endpoints never serve bytes from this origin.
func TestAThumbnailRedirectsAndServesNoBytes(t *testing.T) {
	l := New(Deps{
		Authenticate: func(*http.Request) (Principal, bool) { return Principal{User: 1}, true },
		Preview:      stubPreview{url: "https://content.example/thumb?sig=abc"},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/index.php/core/preview?file=a.jpg&x=64&y=64", nil)
	l.preview(rec, req, 1, ParsePreviewQuery(req.URL.Query()))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want a redirect\n%s", rec.Code, rec.Body)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("the app origin served %d bytes of image data", rec.Body.Len())
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "https://content.example/") {
		t.Fatalf("Location = %q, want the content origin", loc)
	}
	// The signed URL is short-lived and scoped to one caller, so no shared
	// cache may keep it.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
}

// A placeholder is refused rather than drawn: serving one would mean serving
// bytes from the app origin for a file that has no preview.
func TestAPlaceholderIsRefusedRatherThanServed(t *testing.T) {
	l := New(Deps{
		Authenticate: func(*http.Request) (Principal, bool) { return Principal{User: 1}, true },
		Preview:      stubPreview{},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/index.php/core/preview?file=a.jpg&forceIcon=1", nil)
	l.preview(rec, req, 1, ParsePreviewQuery(req.URL.Query()))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec.Body.Len() > 100 {
		t.Fatalf("the refusal carried %d bytes", rec.Body.Len())
	}
}

func TestAThumbnailSizeIsClampedRatherThanRefused(t *testing.T) {
	q := ParsePreviewQuery(map[string][]string{"x": {"999999"}, "y": {"0"}})
	if q.Width != previewMaxSize {
		t.Fatalf("width = %d, want it clamped to %d", q.Width, previewMaxSize)
	}
	if q.Height != previewDefaultSize {
		t.Fatalf("height = %d, want the default", q.Height)
	}
}

// stubPreview mints a fixed URL, or none when it has none to give.
type stubPreview struct{ url string }

func (s stubPreview) SignedThumbURL(context.Context, ncport.UserID, string, int, int) (string, bool, error) {
	if s.url == "" {
		return "", false, nil
	}
	return s.url, true, nil
}

func (s stubPreview) SignedDownloadURL(context.Context, ncport.UserID, string) (string, bool, error) {
	if s.url == "" {
		return "", false, nil
	}
	return s.url, true, nil
}
