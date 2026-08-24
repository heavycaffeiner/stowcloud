//go:build linux && compat_nc

package nc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The envelope is reproduced exactly, including the parts that are wrong,
// because a client parses them. A wrong statuscode produces no error a user
// can see: the client treats it as "the call failed" and gives up silently.

func decodeJSON(t *testing.T, o OCS) map[string]any {
	t.Helper()
	raw, err := o.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var out map[string]any
	if uerr := json.Unmarshal([]byte(raw), &out); uerr != nil {
		t.Fatalf("the envelope is not valid JSON: %v\n%s", uerr, raw)
	}
	return out
}

func meta(t *testing.T, o OCS) map[string]any {
	t.Helper()
	doc := decodeJSON(t, o)
	ocs, ok := doc["ocs"].(map[string]any)
	if !ok {
		t.Fatalf("no ocs node: %v", doc)
	}
	m, ok := ocs["meta"].(map[string]any)
	if !ok {
		t.Fatalf("no meta node: %v", ocs)
	}
	return m
}

// The single most commonly botched value in an OCS reimplementation.
func TestSuccessIsOneHundredOnV1AndTwoHundredOnV2(t *testing.T) {
	if got := OCSv1.SuccessCode(); got != 100 {
		t.Fatalf("v1 success is %d, want 100", got)
	}
	if got := OCSv2.SuccessCode(); got != 200 {
		t.Fatalf("v2 success is %d, want 200", got)
	}

	m1 := meta(t, OK(OCSv1, FormatJSON, VEmptyList()))
	if m1["statuscode"] != float64(100) || m1["status"] != "ok" {
		t.Fatalf("v1 success meta = %v", m1)
	}
	m2 := meta(t, OK(OCSv2, FormatJSON, VEmptyList()))
	if m2["statuscode"] != float64(200) || m2["status"] != "ok" {
		t.Fatalf("v2 success meta = %v", m2)
	}
}

// v1 pins HTTP 200 for everything except unauthorised, which is the one status
// it is allowed to leak.
func TestV1PinsTwoHundredExceptUnauthorised(t *testing.T) {
	for _, code := range []int{100, 200, 400, 403, 404, 500, 998, 999} {
		if got := OCSv1.HTTPStatus(code); got != http.StatusOK {
			t.Fatalf("v1 maps %d to HTTP %d, want 200", code, got)
		}
	}
	if got := OCSv1.HTTPStatus(CodeUnauthorised); got != http.StatusUnauthorized {
		t.Fatalf("v1 maps 997 to HTTP %d, want 401", got)
	}
}

// v2 mirrors, with the sentinels remapped, in a specific evaluation order.
func TestV2MirrorsWithTheSentinelsRemapped(t *testing.T) {
	for _, tc := range []struct {
		code, want int
	}{
		{997, http.StatusUnauthorized},
		{998, http.StatusNotFound},
		{996, http.StatusInternalServerError},
		{999, http.StatusInternalServerError},
		// Below 200 lands on 400, which is where the v1 success code goes.
		{100, http.StatusBadRequest},
		{0, http.StatusBadRequest},
		{601, http.StatusBadRequest},
		// Otherwise the code itself.
		{200, http.StatusOK},
		{403, http.StatusForbidden},
		{404, http.StatusNotFound},
		{500, http.StatusInternalServerError},
	} {
		if got := OCSv2.HTTPStatus(tc.code); got != tc.want {
			t.Errorf("v2 maps %d to HTTP %d, want %d", tc.code, got, tc.want)
		}
	}
}

// A v2 404 shows 404, and a legacy 998 shows 998 with HTTP 404. Both matter:
// v2 does not remap the statuscode itself, only the HTTP status.
// A bare 401 on v1 comes back as HTTP 200 with a statuscode of 401, which
// some clients read as a soft failure and retry forever. The sentinel is the
// only code v1 promotes, so Unauthorized has to use it.
func TestUnauthorizedUsesTheSentinelSoV1LeaksIt(t *testing.T) {
	e := Unauthorized("no")
	if e.Code != CodeUnauthorised {
		t.Fatalf("Unauthorized uses code %d, want the sentinel %d", e.Code, CodeUnauthorised)
	}
	if got := Fail(OCSv1, FormatXML, e).HTTPStatus(); got != http.StatusUnauthorized {
		t.Fatalf("v1 answered HTTP %d, want 401", got)
	}
	if got := Fail(OCSv2, FormatXML, e).HTTPStatus(); got != http.StatusUnauthorized {
		t.Fatalf("v2 answered HTTP %d, want 401", got)
	}
	// A bare 401 would not leak, which is the trap being avoided.
	bare := &OCSError{Code: 401, Message: "no"}
	if got := Fail(OCSv1, FormatXML, bare).HTTPStatus(); got != http.StatusOK {
		t.Fatalf("a bare 401 on v1 answered HTTP %d; if this is 401 the "+
			"sentinel is no longer needed and the comment is stale", got)
	}
}

func TestV2KeepsTheRawStatuscode(t *testing.T) {
	m := meta(t, Fail(OCSv2, FormatJSON, NotFound("gone")))
	if m["statuscode"] != float64(404) {
		t.Fatalf("statuscode = %v, want 404", m["statuscode"])
	}

	legacy := meta(t, Fail(OCSv2, FormatJSON, &OCSError{Code: CodeNotFound, Message: "gone"}))
	if legacy["statuscode"] != float64(998) {
		t.Fatalf("statuscode = %v, want the legacy 998 preserved", legacy["statuscode"])
	}
	if got := Fail(OCSv2, FormatJSON, &OCSError{Code: CodeNotFound}).HTTPStatus(); got != 404 {
		t.Fatalf("the legacy sentinel produced HTTP %d, want 404", got)
	}
}

// v1 always emits five keys, with the pagination pair present as empty
// strings. v2 emits three. Clients that pattern-match the v1 envelope notice.
func TestV1EmitsFiveMetaKeysAndV2Three(t *testing.T) {
	m1 := meta(t, OK(OCSv1, FormatJSON, VEmptyList()))
	if len(m1) != 5 {
		t.Fatalf("v1 meta has %d keys, want 5: %v", len(m1), m1)
	}
	if m1["totalitems"] != "" || m1["itemsperpage"] != "" {
		t.Fatalf("the v1 pagination pair is not present-but-empty: %v", m1)
	}

	m2 := meta(t, OK(OCSv2, FormatJSON, VEmptyList()))
	if len(m2) != 3 {
		t.Fatalf("v2 meta has %d keys, want 3: %v", len(m2), m2)
	}
	if _, present := m2["totalitems"]; present {
		t.Fatalf("v2 emitted the pagination pair: %v", m2)
	}
}

// An error carries an empty data node rather than a missing one, because
// clients dereference it unconditionally.
func TestAnErrorCarriesAnEmptyDataNode(t *testing.T) {
	doc := decodeJSON(t, Fail(OCSv2, FormatJSON, BadRequest("no")))
	ocs, ok := doc["ocs"].(map[string]any)
	if !ok {
		t.Fatalf("no ocs node: %v", doc)
	}
	data, present := ocs["data"]
	if !present {
		t.Fatalf("an error envelope has no data node: %v", ocs)
	}
	arr, ok := data.([]any)
	if !ok || len(arr) != 0 {
		t.Fatalf("data = %v, want an empty array", data)
	}
}

// An array and an object are not interchangeable to a typed client.
func TestAnEmptyListAndAnEmptyMapRenderDifferently(t *testing.T) {
	list, err := OK(OCSv2, FormatJSON, VEmptyList()).JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	obj, err := OK(OCSv2, FormatJSON, VEmptyMap()).JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(list, `"data":[]`) {
		t.Fatalf("an empty list did not render as []: %s", list)
	}
	if !strings.Contains(obj, `"data":{}`) {
		t.Fatalf("an empty map did not render as {}: %s", obj)
	}
}

// The XML form is not the JSON form with different brackets.
func TestTheXMLFormFollowsTheReferenceWriter(t *testing.T) {
	doc := OK(OCSv2, FormatXML, VMap(
		F("yes", VBool(true)),
		F("no", VBool(false)),
		F("nothing", VNull()),
		F("empty", VStr("")),
		F("items", VList(VStr("a"), VStr("b"))),
		F("count", VInt(3)),
	)).XML()

	// A boolean is 1 or an empty element, never "true" and "false".
	if !strings.Contains(doc, "<yes>1</yes>") {
		t.Fatalf("true did not render as 1:\n%s", doc)
	}
	// An element with no content collapses to a self-closing tag: that covers
	// false, null and the empty string alike.
	for _, want := range []string{"<no/>", "<nothing/>", "<empty/>"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("missing %s:\n%s", want, doc)
		}
	}
	// A list item loses its index and becomes <element>.
	if !strings.Contains(doc, "<items><element>a</element><element>b</element></items>") {
		t.Fatalf("list items did not become <element>:\n%s", doc)
	}
	if strings.Contains(doc, "true") || strings.Contains(doc, "false") {
		t.Fatalf("a boolean leaked its Go spelling:\n%s", doc)
	}
}

func TestAnEmptyDataNodeIsSelfClosingInXML(t *testing.T) {
	doc := Fail(OCSv1, FormatXML, NotFound("gone")).XML()
	if !strings.Contains(doc, "<data/>") {
		t.Fatalf("the empty data node is not self-closing:\n%s", doc)
	}
}

// A float that happens to be integral has no trailing decimal, which is the
// quota case exactly.
func TestAnIntegralFloatHasNoTrailingDecimal(t *testing.T) {
	doc := OK(OCSv2, FormatXML, VMap(
		F("whole", VFloat(25)),
		F("part", VFloat(12.5)),
	)).XML()
	if !strings.Contains(doc, "<whole>25</whole>") {
		t.Fatalf("an integral float rendered with a decimal:\n%s", doc)
	}
	if !strings.Contains(doc, "<part>12.5</part>") {
		t.Fatalf("a fractional float lost its decimal:\n%s", doc)
	}
}

// Character data is escaped, and a control character is dropped rather than
// emitted: a document carrying one is rejected wholesale by the client's
// parser, which loses the whole response rather than one field.
func TestXMLEscapingDropsControlCharacters(t *testing.T) {
	got := XMLEscapeText("a&b<c>d\"e'f\x01g")
	for _, want := range []string{"&amp;", "&lt;", "&gt;", "&quot;", "&apos;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("XMLEscapeText = %q, missing %s", got, want)
		}
	}
	if strings.ContainsRune(got, 0x01) {
		t.Fatalf("XMLEscapeText = %q, which carries a control character", got)
	}
	// Tab, newline and carriage return are legal and survive.
	if kept := XMLEscapeText("a\tb\nc\rd"); kept != "a\tb\nc\rd" {
		t.Fatalf("legal whitespace was dropped: %q", kept)
	}
}

// Map order is preserved, because several clients read the XML positionally
// and a reordering writer would produce a document they misread.
func TestMapOrderSurvivesBothWriters(t *testing.T) {
	v := VMap(F("zebra", VInt(1)), F("apple", VInt(2)), F("middle", VInt(3)))

	raw, err := OK(OCSv2, FormatJSON, v).JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	zi, ai := strings.Index(raw, "zebra"), strings.Index(raw, "apple")
	if zi < 0 || ai < 0 || zi > ai {
		t.Fatalf("the JSON writer sorted the keys:\n%s", raw)
	}

	doc := OK(OCSv2, FormatXML, v).XML()
	zx, ax := strings.Index(doc, "zebra"), strings.Index(doc, "apple")
	if zx < 0 || ax < 0 || zx > ax {
		t.Fatalf("the XML writer sorted the keys:\n%s", doc)
	}
}

// The format parameter wins over Accept, and the default is XML.
func TestFormatNegotiation(t *testing.T) {
	jsonAccept := http.Header{"Accept": {"application/json"}}
	for _, tc := range []struct {
		name    string
		query   string
		headers http.Header
		want    OCSFormat
	}{
		{"the parameter wins", "format=json", nil, FormatJSON},
		{"the parameter wins over Accept", "format=xml", jsonAccept, FormatXML},
		{"Accept is the fallback", "", jsonAccept, FormatJSON},
		{"the default is XML", "", nil, FormatXML},
		{"an unknown format is XML", "format=yaml", jsonAccept, FormatXML},
		{"another parameter is ignored", "limit=5", jsonAccept, FormatJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.headers
			if h == nil {
				h = http.Header{}
			}
			if got := NegotiateFormat(tc.query, h); got != tc.want {
				t.Fatalf("NegotiateFormat(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

func TestTheResponseCarriesItsContentTypeAndStatus(t *testing.T) {
	for _, tc := range []struct {
		name        string
		envelope    OCS
		wantStatus  int
		wantContent string
	}{
		{"v1 success", OK(OCSv1, FormatXML, VEmptyList()), 200, "application/xml"},
		// Unauthorized uses the legacy sentinel, which is the only code v1
		// promotes to an HTTP 401.
		{"v1 unauthorised", Fail(OCSv1, FormatXML, Unauthorized("no")), 401, "application/xml"},
		{"v1 not found is still 200", Fail(OCSv1, FormatJSON, NotFound("no")), 200, "application/json"},
		{"v2 not found", Fail(OCSv2, FormatJSON, NotFound("no")), 404, "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.envelope.Write(rec)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, tc.wantContent) {
				t.Fatalf("Content-Type = %q, want %s", ct, tc.wantContent)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Fatalf("the entry points set this unconditionally, got %q", got)
			}
		})
	}
}

// The stub shapes are a client-crash workaround and the test asserts the
// shape, not merely the status.
func TestTheStubShapesAreArraysAndObjectsAsTheClientExpects(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  Val
		want ValKind
	}{
		{"notifications", Notifications(), KindList},
		{"user statuses", UserStatuses(), KindList},
		{"navigation apps", NavigationApps(), KindList},
		{"autocomplete", Autocomplete(), KindList},
		{"the provisioning config", EmptyObject(), KindMap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.val.Kind != tc.want {
				t.Fatalf("%s is kind %v, want %v", tc.name, tc.val.Kind, tc.want)
			}
			if tc.want == KindList && len(tc.val.List) != 0 {
				t.Fatalf("%s is not empty", tc.name)
			}
			if tc.want == KindMap && len(tc.val.Map) != 0 {
				t.Fatalf("%s is not empty", tc.name)
			}
		})
	}
}

// These four must answer 404 rather than an empty success. A 200 with an empty
// object is handed to the client's JSON layer, which fills a non-nullable
// field with null and crashes on the next dereference.
func TestTheNotFoundPathsAreTheFourWithARecordedCrash(t *testing.T) {
	paths := NotFoundPaths()
	if len(paths) != 4 {
		t.Fatalf("got %d paths, want the four with a recorded cause: %v", len(paths), paths)
	}
	for _, p := range paths {
		if !IsNotFoundPath(p) {
			t.Fatalf("%s is in the list and does not match it", p)
		}
	}
	// Both versions of both endpoints, because a client may call either.
	for _, want := range []string{
		"/ocs/v2.php/apps/activity/api/v2/activity",
		"/ocs/v1.php/apps/activity/api/v2/activity",
		"/ocs/v2.php/apps/user_status/api/v1/user_status",
		"/ocs/v1.php/apps/user_status/api/v1/user_status",
	} {
		if !IsNotFoundPath(want) {
			t.Fatalf("%s must answer 404 and does not", want)
		}
	}
	// The bulk status query is a different endpoint and answers a list.
	if IsNotFoundPath("/ocs/v2.php/apps/user_status/api/v1/statuses") {
		t.Fatal("the bulk statuses endpoint must answer an empty list, not 404")
	}
}
