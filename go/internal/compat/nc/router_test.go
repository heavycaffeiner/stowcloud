//go:build compat_nc

package nc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The mounts, and the shapes a client reads off them.

type stubState struct{ id string }

func (s stubState) InstanceID(context.Context) (string, error) { return s.id, nil }
func (s stubState) Favorites(context.Context, ncport.UserID) ([]ncport.Favorite, error) {
	return nil, nil
}
func (s stubState) SetFavorite(context.Context, ncport.UserID, ncport.Favorite, bool) error {
	return nil
}

func testLayer(t *testing.T) *Layer {
	t.Helper()
	return New(Deps{
		State: stubState{id: "instance01"},
		Caps: CapsConfig{
			VersionMajor: 31, VersionMinor: 0, VersionMicro: 4,
			VersionString:       "31.0.4",
			PollIntervalSeconds: 60,
			ChunkSizeAdvisory:   10 << 20, ChunkParallelAdvisory: 5,
			ShareeMinSearch:             3,
			BlacklistedFiles:            []string{"CON"},
			ForbiddenFilenameCharacters: []string{"/"},
			ThemingName:                 "Stowcloud",
			ThemingColor:                "#0082c9",
			CanonicalURL:                "https://files.example",
		},
	})
}

// serve routes one request through the layer's own mounts, which is what the
// assembly registers.
func serve(t *testing.T, l *Layer, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	for _, m := range l.Mounts() {
		mux.Handle(m.Method+" "+m.Pattern, m.Handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func body(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("the response is not JSON: %v\n%s", err, rec.Body.String())
	}
	return out
}

// A client reads this before it has credentials, so it must carry nothing a
// stranger should not see.
func TestTheStatusDocumentIsAnswerable(t *testing.T) {
	rec := serve(t, testLayer(t), "GET", "/status.php")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body)
	}
	doc := body(t, rec)
	if doc["installed"] != true || doc["maintenance"] != false {
		t.Fatalf("the status document is %v", doc)
	}
	if doc["version"] != "31.0.4" {
		t.Fatalf("version = %v", doc["version"])
	}
	// It is not the capabilities document, which needs a principal.
	if _, present := doc["capabilities"]; present {
		t.Fatal("the pre-login status document carries the capabilities")
	}
}

func TestCapabilitiesAnswerOnBothVersions(t *testing.T) {
	l := testLayer(t)
	for _, base := range []string{"/ocs/v1.php", "/ocs/v2.php"} {
		rec := serve(t, l, "GET", base+"/cloud/capabilities?format=json")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s answered %d\n%s", base, rec.Code, rec.Body)
		}
		doc := body(t, rec)
		ocs, ok := doc["ocs"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no ocs node", base)
		}
		data, ok := ocs["data"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no data node", base)
		}
		if _, present := data["capabilities"]; !present {
			t.Fatalf("%s: no capabilities node: %v", base, data)
		}
	}
}

// The absent keys are the load-bearing part: a client gates the whole feature
// on the key's presence and never on its contents, so any value turns it on.
func TestTheCapabilitiesOmitTheKeysThatCannotSayNo(t *testing.T) {
	rec := serve(t, testLayer(t), "GET", "/ocs/v2.php/cloud/capabilities?format=json")
	doc := body(t, rec)
	ocs := doc["ocs"].(map[string]any)            //nolint:errcheck // asserted above.
	data := ocs["data"].(map[string]any)          //nolint:errcheck // as above.
	caps := data["capabilities"].(map[string]any) //nolint:errcheck // as above.

	for _, absent := range []string{"activity", "external", "governance"} {
		if _, present := caps[absent]; present {
			t.Fatalf("%q is present; any value turns the feature on and the "+
				"client then polls an endpoint answered with 404", absent)
		}
	}

	dav, ok := caps["dav"].(map[string]any)
	if !ok {
		t.Fatalf("no dav node: %v", caps)
	}
	// bulkupload's presence, whatever the value, makes the client bundle
	// small files into a multipart post this does not serve.
	if _, present := dav["bulkupload"]; present {
		t.Fatal("bulkupload is present, which turns the bundling on")
	}
	// The chunking marker is a string, because the client compares it
	// bytewise.
	if dav["chunking"] != "1.0" {
		t.Fatalf("chunking = %v (%T), want the string \"1.0\"", dav["chunking"], dav["chunking"])
	}
}

// The stub payloads answer with the shape the client's typed layer expects.
func TestTheStubEndpointsAnswerTheirShapes(t *testing.T) {
	l := testLayer(t)
	for _, tc := range []struct {
		path     string
		wantList bool
	}{
		{"/ocs/v2.php/apps/notifications/api/v2/notifications", true},
		{"/ocs/v2.php/apps/user_status/api/v1/statuses", true},
		{"/ocs/v2.php/core/navigation/apps", true},
		{"/ocs/v2.php/core/autocomplete/get", true},
		{"/ocs/v2.php/apps/provisioning_api/api/v1/config/anything", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := serve(t, l, "GET", tc.path+"?format=json")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d\n%s", rec.Code, rec.Body)
			}
			doc := body(t, rec)
			ocs, ok := doc["ocs"].(map[string]any)
			if !ok {
				t.Fatalf("no ocs node: %v", doc)
			}
			switch data := ocs["data"].(type) {
			case []any:
				if !tc.wantList {
					t.Fatalf("got an array where the client reads a record")
				}
				if len(data) != 0 {
					t.Fatalf("got %v, want an empty array", data)
				}
			case map[string]any:
				if tc.wantList {
					t.Fatalf("got a record where the client reads an array")
				}
				if len(data) != 0 {
					t.Fatalf("got %v, want an empty object", data)
				}
			default:
				t.Fatalf("data is %T", ocs["data"])
			}
		})
	}
}

// The four with a recorded crash answer 404 rather than an empty success, on
// both versions, because a client may call either.
func TestTheRecordedCrashPathsAnswer404(t *testing.T) {
	l := testLayer(t)
	for _, p := range NotFoundPaths() {
		rec := serve(t, l, "GET", p+"?format=json")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d, want 404\n%s", p, rec.Code, rec.Body)
		}
		// And not an OCS envelope: the client's guarded path keys on the
		// status, and a body it might parse is what caused the crash.
		if strings.Contains(rec.Body.String(), `"ocs"`) {
			t.Fatalf("%s answered an envelope:\n%s", p, rec.Body)
		}
	}
}

// An unrouted request is a refusal produced before any handler runs, and it is
// logged because this case existed once and was invisible.
func TestAnUnroutedOCSRequestIsRefusedAndLogged(t *testing.T) {
	warned := 0
	l := New(Deps{
		State: stubState{id: "instance01"},
		Warn:  func(string, ...any) { warned++ },
	})

	rec := serve(t, l, "GET", "/ocs/v2.php/apps/nothing/here?format=json")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404\n%s", rec.Code, rec.Body)
	}
	if warned == 0 {
		t.Fatal("an unrouted request was refused silently")
	}
	// The refusal is an envelope, because the client asked an OCS endpoint.
	doc := body(t, rec)
	if _, present := doc["ocs"]; !present {
		t.Fatalf("the refusal is not an envelope: %v", doc)
	}
}

// The v1 envelope on a refusal keeps HTTP 200, which is the version's rule and
// the reason the two are mounted separately.
func TestV1KeepsItsStatusOnARefusal(t *testing.T) {
	l := testLayer(t)
	rec := serve(t, l, "GET", "/ocs/v1.php/apps/nothing/here?format=json")
	if rec.Code != http.StatusOK {
		t.Fatalf("v1 answered %d, want 200 even for a refusal", rec.Code)
	}
	doc := body(t, rec)
	ocs := doc["ocs"].(map[string]any)   //nolint:errcheck // the shape is asserted by the decode.
	meta := ocs["meta"].(map[string]any) //nolint:errcheck // as above.
	if meta["statuscode"] != float64(404) {
		t.Fatalf("statuscode = %v, want the real code in the body", meta["statuscode"])
	}
}

// The format parameter is honoured on a real route, not only in the
// negotiation helper.
func TestTheFormatParameterReachesTheResponse(t *testing.T) {
	l := testLayer(t)

	jsonRec := serve(t, l, "GET", "/ocs/v2.php/cloud/capabilities?format=json")
	if ct := jsonRec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("Content-Type = %q", ct)
	}

	xmlRec := serve(t, l, "GET", "/ocs/v2.php/cloud/capabilities")
	if ct := xmlRec.Header().Get("Content-Type"); !strings.Contains(ct, "xml") {
		t.Fatalf("the default is %q, want XML", ct)
	}
	if !strings.HasPrefix(xmlRec.Body.String(), "<?xml") {
		t.Fatalf("the XML form does not start with a declaration:\n%s", xmlRec.Body)
	}
}
