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
		State:        stubState{id: "instance01"},
		Authenticate: func(*http.Request) (Principal, bool) { return Principal{User: 1}, true },
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
		State:        stubState{id: "instance01"},
		Authenticate: func(*http.Request) (Principal, bool) { return Principal{User: 1}, true },
		Warn:         func(string, ...any) { warned++ },
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

// An unauthenticated request to a surface that needs a principal is refused
// with the sentinel, which is the one code v1 leaks into HTTP.
func TestAnUnauthenticatedRequestIsRefused(t *testing.T) {
	l := New(Deps{State: stubState{id: "instance01"}})
	for _, path := range []string{
		"/ocs/v2.php/cloud/user",
		"/ocs/v2.php/search/providers",
		"/ocs/v2.php/apps/files/api/v1/favorites",
	} {
		rec := serve(t, l, "GET", path+"?format=json")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s answered %d with no principal, want 401", path, rec.Code)
		}
	}

	// The unauthenticated surfaces still answer, which is what makes a client
	// able to discover this server before it has credentials.
	for _, path := range []string{
		"/ocs/v2.php/cloud/capabilities",
		"/ocs/v2.php/core/navigation/apps",
	} {
		rec := serve(t, l, "GET", path+"?format=json")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s answered %d with no principal, want 200", path, rec.Code)
		}
	}
}

// The captive-portal probe is unauthenticated and empty. Anything else makes
// the Android client read this as "no internet" and park every upload as
// pending without ever issuing a request.
func TestTheCaptivePortalProbeIsEmptyAndUnauthenticated(t *testing.T) {
	l := New(Deps{State: stubState{id: "instance01"}})
	rec := serve(t, l, "GET", "/index.php/204")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("the probe answered %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("the probe answered %d bytes, want none", rec.Body.Len())
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

// A port this build has not wired up must produce a refusal a client can act
// on, never a panic and never an answer a client would believe. This is what
// makes an unfinished seam safe to ship behind the tag.
func TestAnUnwiredPortRefusesRatherThanCrashing(t *testing.T) {
	// Authenticated, so the request reaches the surface, but every port nil.
	l := New(Deps{
		Authenticate: func(*http.Request) (Principal, bool) { return Principal{User: 1}, true },
	})

	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/ocs/v2.php/cloud/user"},
		{"GET", "/ocs/v2.php/cloud/users/alice"},
		{"GET", "/ocs/v2.php/search/providers/files/search?term=x"},
		{"GET", "/ocs/v2.php/apps/files/api/v1/recent"},
		{"GET", "/ocs/v2.php/apps/files/api/v1/favorites"},
		{"POST", "/ocs/v2.php/apps/dav/api/v1/direct"},
		{"DELETE", "/ocs/v2.php/core/apppassword"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			sep := "&"
			if !strings.Contains(tc.path, "?") {
				sep = "?"
			}
			rec := serve(t, l, tc.method, tc.path+sep+"format=json")
			// Any answer is acceptable except a success carrying data a
			// client would act on, and except a crash, which the recorder
			// would never reach.
			if rec.Code == http.StatusOK {
				doc := body(t, rec)
				ocs, ok := doc["ocs"].(map[string]any)
				if !ok {
					t.Fatalf("a 200 with no envelope: %s", rec.Body)
				}
				meta, ok := ocs["meta"].(map[string]any)
				if !ok {
					t.Fatalf("a 200 with no meta: %s", rec.Body)
				}
				if meta["status"] == "ok" {
					// The favourites surface answers an empty list with no
					// store, which is honest: nothing is starred.
					if !strings.Contains(tc.path, "favorites") {
						t.Fatalf("an unwired port answered success: %s", rec.Body)
					}
				}
			}
		})
	}
}

// The login flow's routes are absent rather than broken when it is not wired.
func TestTheLoginFlowRoutesRefuseWhenUnwired(t *testing.T) {
	l := New(Deps{
		Authenticate: func(*http.Request) (Principal, bool) { return Principal{User: 1}, true },
	})
	for _, p := range []string{
		"/index.php/login/v2",
		"/index.php/login/v2/poll",
		"/index.php/login/v2/grant",
	} {
		rec := serve(t, l, "POST", p)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s answered %d with no flow, want 404", p, rec.Code)
		}
	}
}
