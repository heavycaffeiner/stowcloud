//go:build linux && compat_nc

package lifecycle_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// What a client reads before and during sign-in.
//
// Three of these are strict in a way that is invisible from a status code: the
// status probe's version has to parse, the capabilities document has to decode
// under a strict typed decoder, and the login flow's JSON carries no envelope
// at all.

// The probe answers a stranger, and the fields it carries are the ones a
// client parses without guarding them.
func TestTheStatusProbeCarriesWhatClientsParseUnguarded(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, body := ncAnonymous(t, "GET", f.base+"/status.php", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("answered %d, want 200", resp.StatusCode)
	}

	var doc struct {
		Installed       bool   `json:"installed"`
		Maintenance     bool   `json:"maintenance"`
		NeedsDBUpgrade  bool   `json:"needsDbUpgrade"`
		Version         string `json:"version"`
		VersionString   string `json:"versionstring"`
		Edition         string `json:"edition"`
		ProductName     string `json:"productname"`
		ExtendedSupport bool   `json:"extendedSupport"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the document does not parse: %v\n%s", err, body)
	}
	if !doc.Installed || doc.Maintenance || doc.NeedsDBUpgrade {
		t.Errorf("the flags are %#v", doc)
	}
	if parts := strings.Split(doc.Version, "."); len(parts) < 3 {
		t.Errorf("version %q does not split into a numeric triple", doc.Version)
	}
	if doc.VersionString == "" || doc.ProductName == "" {
		t.Errorf("version string is %q and product name is %q", doc.VersionString, doc.ProductName)
	}
	if strings.Contains(strings.ToLower(doc.ProductName), "owncloud") {
		t.Errorf("product name %q makes one client refuse to proceed", doc.ProductName)
	}
}

// The connectivity probe is empty on purpose. Anything with a body, and above
// all a redirect, reads as a captive portal and one client abandons the whole
// connection check.
func TestTheConnectivityProbeIsEmpty(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	for _, path := range []string{"/index.php/204", "/204"} {
		resp, body := ncAnonymous(t, "GET", f.base+path, nil)
		if resp.StatusCode != 204 {
			t.Errorf("%s answered %d, want 204", path, resp.StatusCode)
		}
		if len(body) != 0 {
			t.Errorf("%s carried %d bytes", path, len(body))
		}
	}
}

// The capabilities document has to survive a strict typed decode: one client
// decodes it with a generated decoder and, on any type mismatch, runs with no
// capabilities at all and silently disables every feature.
func TestCapabilitiesDecodeUnderStrictTypes(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	for _, path := range []string{
		"/ocs/v1.php/cloud/capabilities",
		"/ocs/v2.php/cloud/capabilities",
		"/index.php/ocs/v2.php/cloud/capabilities",
	} {
		req := jsonRequest(t, f.base+path)
		resp, body := doAnonymous(t, req)
		if resp.StatusCode != 200 {
			t.Fatalf("%s answered %d\n%s", path, resp.StatusCode, body)
		}

		var doc struct {
			OCS struct {
				Meta struct {
					StatusCode int `json:"statuscode"`
				} `json:"meta"`
				Data struct {
					Version struct {
						Major  int    `json:"major"`
						Minor  int    `json:"minor"`
						Micro  int    `json:"micro"`
						String string `json:"string"`
					} `json:"version"`
					Capabilities struct {
						Files struct {
							BigFileChunking bool `json:"bigfilechunking"`
							Undelete        bool `json:"undelete"`
						} `json:"files"`
						FilesSharing struct {
							APIEnabled bool `json:"api_enabled"`
							Public     struct {
								Enabled bool `json:"enabled"`
							} `json:"public"`
						} `json:"files_sharing"`
						Core struct {
							WebdavRoot string `json:"webdav-root"`
						} `json:"core"`
					} `json:"capabilities"`
				} `json:"data"`
			} `json:"ocs"`
		}
		// A typed decode, which is the whole point: the fields these clients
		// declare have to carry the declared types, and a number rendered as
		// a string makes the strict decoder in one of them throw away the
		// entire document.
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s: the document does not decode: %v\n%s", path, err, body)
		}

		if doc.OCS.Data.Version.Major == 0 {
			t.Errorf("%s: version.major decoded as 0, so it is not a JSON number", path)
		}
		if doc.OCS.Data.Version.String == "" {
			t.Errorf("%s: version.string is empty", path)
		}
		if !doc.OCS.Data.Capabilities.FilesSharing.APIEnabled {
			t.Errorf("%s: the share API is not advertised", path)
		}
		if !doc.OCS.Data.Capabilities.FilesSharing.Public.Enabled {
			t.Errorf("%s: public links are not advertised", path)
		}
		if !doc.OCS.Data.Capabilities.Files.BigFileChunking {
			t.Errorf("%s: chunked upload is not advertised", path)
		}
		if got := doc.OCS.Data.Capabilities.Core.WebdavRoot; got != "remote.php/webdav" {
			t.Errorf("%s: webdav-root is %q", path, got)
		}
	}
}

// A version-one envelope answers 200 whatever the outcome, and a
// version-two envelope mirrors the code. A client reads one or the other and
// gets the wrong idea from the other one's convention.
func TestTheTwoEnvelopeVersionsMapStatusDifferently(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	v1, _ := f.request(t, "GET", f.base+"/ocs/v1.php/apps/files_sharing/api/v1/shares/424242", nil, nil)
	if v1.StatusCode != 200 {
		t.Errorf("a version-one refusal answered %d, want 200", v1.StatusCode)
	}
	v2, _ := f.request(t, "GET", f.base+"/ocs/v2.php/apps/files_sharing/api/v1/shares/424242", nil, nil)
	if v2.StatusCode != 404 {
		t.Errorf("a version-two refusal answered %d, want 404", v2.StatusCode)
	}
}

// The account record, with the quota figures a client compares an upload
// against before it starts one.
func TestTheAccountRecordCarriesQuota(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, body := f.request(t, "GET", f.base+"/ocs/v2.php/cloud/user?format=json", nil,
		map[string]string{"Accept": "application/json"})
	if resp.StatusCode != 200 {
		t.Fatalf("answered %d\n%s", resp.StatusCode, body)
	}

	var doc struct {
		OCS struct {
			Meta struct {
				StatusCode int `json:"statuscode"`
			} `json:"meta"`
			Data struct {
				ID          string `json:"id"`
				DisplayName string `json:"display-name"`
				Enabled     bool   `json:"enabled"`
				Quota       struct {
					Free  int64   `json:"free"`
					Used  int64   `json:"used"`
					Total int64   `json:"total"`
					Quota int64   `json:"quota"`
					Rel   float64 `json:"relative"`
				} `json:"quota"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the document does not parse: %v\n%s", err, body)
	}
	if doc.OCS.Meta.StatusCode != 200 {
		t.Errorf("the envelope reports %d, and one client accepts only 200 here",
			doc.OCS.Meta.StatusCode)
	}
	if doc.OCS.Data.ID != f.login {
		t.Errorf("id is %q, want %q", doc.OCS.Data.ID, f.login)
	}
	if !doc.OCS.Data.Enabled {
		t.Error("the account reports itself disabled")
	}
	if doc.OCS.Data.Quota.Free <= 0 {
		t.Errorf("free space is %d, and one client refuses to start an upload against that",
			doc.OCS.Data.Quota.Free)
	}
}

// The device login, end to end: begin, approve from a browser session, then
// poll and receive exactly one credential that works.
func TestTheDeviceLoginFlowDeliversAWorkingCredential(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	resp, body := ncAnonymous(t, "POST", f.base+"/index.php/login/v2", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("beginning answered %d\n%s", resp.StatusCode, body)
	}
	var begun struct {
		Poll struct {
			Token    string `json:"token"`
			Endpoint string `json:"endpoint"`
		} `json:"poll"`
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &begun); err != nil {
		t.Fatalf("the flow document does not parse: %v\n%s", err, body)
	}
	if begun.Poll.Token == "" || begun.Poll.Endpoint == "" || begun.Login == "" {
		t.Fatalf("the flow document is %#v", begun)
	}
	if !strings.Contains(begun.Login, "/login/v2/flow/") {
		t.Errorf("the login URL is %q", begun.Login)
	}

	// Nothing is approved yet, so the poll answers the "not yet" the client
	// loops against rather than an error.
	pending, _ := ncAnonymous(t, "POST", begun.Poll.Endpoint+"?token="+url.QueryEscape(begun.Poll.Token), nil)
	if pending.StatusCode != 404 {
		t.Errorf("an unapproved poll answered %d, want 404", pending.StatusCode)
	}

	// The person approves in a browser: a session cookie and the CSRF header,
	// which is what the consent page sends.
	loginToken := begun.Login[strings.LastIndex(begun.Login, "/")+1:]
	form := url.Values{"token": {loginToken}}
	req := postForm(t, f.base+"/index.php/login/v2/grant", form)
	f.sess.attach(req)
	granted, err := ncClient().Do(req)
	if err != nil {
		t.Fatalf("approving: %v", err)
	}
	if cerr := granted.Body.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	if granted.StatusCode != 204 && granted.StatusCode != 200 {
		t.Fatalf("the approval answered %d", granted.StatusCode)
	}

	resp, body = ncAnonymous(t, "POST",
		begun.Poll.Endpoint+"?token="+url.QueryEscape(begun.Poll.Token), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("the poll after approval answered %d\n%s", resp.StatusCode, body)
	}
	var delivered struct {
		Server      string `json:"server"`
		LoginName   string `json:"loginName"`
		AppPassword string `json:"appPassword"`
	}
	if uerr := json.Unmarshal(body, &delivered); uerr != nil {
		t.Fatalf("the delivery does not parse: %v\n%s", uerr, body)
	}
	if delivered.LoginName != f.login || delivered.AppPassword == "" || delivered.Server == "" {
		t.Fatalf("the delivery is %#v", delivered)
	}

	// The credential works, which is the only thing that makes the flow worth
	// having.
	minted := ncFixture{base: f.base, share: f.share, host: f.host,
		login: delivered.LoginName, token: delivered.AppPassword, user: f.user, e: f.e}
	listed, listing := minted.request(t, "PROPFIND", minted.davRoot(),
		strings.NewReader(propfindBody),
		map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if listed.StatusCode != 207 {
		t.Fatalf("the delivered credential answered %d to a listing\n%s", listed.StatusCode, listing)
	}

	// And it is delivered once: a second poll finds nothing.
	second, _ := ncAnonymous(t, "POST",
		begun.Poll.Endpoint+"?token="+url.QueryEscape(begun.Poll.Token), nil)
	if second.StatusCode == 200 {
		t.Error("the credential was delivered twice")
	}
}

// A device credential cannot approve a pairing: that would let a token mint
// another token without a person in the loop.
func TestADeviceCredentialCannotApproveAPairing(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("hello"))

	_, body := ncAnonymous(t, "POST", f.base+"/index.php/login/v2", nil)
	var begun struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &begun); err != nil {
		t.Fatalf("the flow document does not parse: %v", err)
	}
	token := begun.Login[strings.LastIndex(begun.Login, "/")+1:]

	resp, _ := f.request(t, "POST", f.base+"/index.php/login/v2/grant",
		strings.NewReader("token="+url.QueryEscape(token)),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if resp.StatusCode == 200 || resp.StatusCode == 204 {
		t.Fatal("a device credential approved its own pairing")
	}
}
