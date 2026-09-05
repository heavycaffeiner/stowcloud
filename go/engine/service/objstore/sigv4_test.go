package objstore

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// These three cases are drawn verbatim from the published AWS Signature
// Version 4 test suite (its "host" test cases, which check the algorithm
// itself against a fictional service rather than any live AWS one): a bare
// GET, a path made entirely of RFC 3986 unreserved characters, and a query
// string with two parameters already in sorted order. All three share the
// suite's fixed test credentials. A broken canonicalization or a broken
// signing-key derivation fails one of these at build time, never against a
// live endpoint.
const (
	sigV4TestAccessKey = "AKIDEXAMPLE"
	sigV4TestSecretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	sigV4TestRegion    = "us-east-1"
	sigV4TestService   = "host"
	sigV4TestAmzDate   = "20110909T233600Z"
)

func TestSigV4CanonicalTestSuiteVectors(t *testing.T) {
	headerValues := map[string]string{
		"date": "Mon, 09 Sep 2011 23:36:00 GMT",
		"host": "host.foo.com",
	}
	signedHeaders := []string{"date", "host"}

	cases := []struct {
		name      string
		uri       string
		query     string
		wantCreq  string
		wantAuthz string
	}{
		{
			name:      "get-vanilla",
			uri:       "/",
			query:     "",
			wantCreq:  "GET\n/\n\ndate:Mon, 09 Sep 2011 23:36:00 GMT\nhost:host.foo.com\n\ndate;host\n" + emptyPayloadHash(),
			wantAuthz: "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20110909/us-east-1/host/aws4_request, SignedHeaders=date;host, Signature=b27ccfbfa7df52a200ff74193ca6e32d4b48b8856fab7ebf1c595d0670a7e470",
		},
		{
			name:      "get-unreserved",
			uri:       "/-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz",
			query:     "",
			wantCreq:  "GET\n/-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz\n\ndate:Mon, 09 Sep 2011 23:36:00 GMT\nhost:host.foo.com\n\ndate;host\n" + emptyPayloadHash(),
			wantAuthz: "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20110909/us-east-1/host/aws4_request, SignedHeaders=date;host, Signature=830cc36d03f0f84e6ee4953fbe701c1c8b71a0372c63af9255aa364dd183281e",
		},
		{
			name:      "get-vanilla-query-order-key",
			uri:       "/",
			query:     "a=foo&b=foo",
			wantCreq:  "GET\n/\na=foo&b=foo\ndate:Mon, 09 Sep 2011 23:36:00 GMT\nhost:host.foo.com\n\ndate;host\n" + emptyPayloadHash(),
			wantAuthz: "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20110909/us-east-1/host/aws4_request, SignedHeaders=date;host, Signature=0dc122f3b28b831ab48ba65cb47300de53fbe91b577fe113edac383730254a3b",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creq := canonicalRequest("GET", tc.uri, tc.query,
				canonicalHeaderBlock(signedHeaders, headerValues),
				strings.Join(signedHeaders, ";"), emptyPayloadHash())
			if creq != tc.wantCreq {
				t.Fatalf("canonical request =\n%q\nwant\n%q", creq, tc.wantCreq)
			}

			dateStamp := sigV4TestAmzDate[:8]
			scope := dateStamp + "/" + sigV4TestRegion + "/" + sigV4TestService + "/aws4_request"
			sts := stringToSign(sigV4TestAmzDate, scope, sha256Hex([]byte(creq)))
			key := signingKey(sigV4TestSecretKey, dateStamp, sigV4TestRegion, sigV4TestService)
			sig := hex.EncodeToString(hmacSHA256(key, sts))

			authz := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
				sigV4TestAccessKey, scope, strings.Join(signedHeaders, ";"), sig)
			if authz != tc.wantAuthz {
				t.Fatalf("authorization =\n%q\nwant\n%q", authz, tc.wantAuthz)
			}
		})
	}
}

// TestSigV4StringToSignMatchesGetVanilla pins the intermediate
// string-to-sign for get-vanilla against the test suite's own .sts file,
// so a canonical-request hash computed correctly but combined wrongly into
// the string to sign still fails on its own, distinct from the final
// signature check above.
func TestSigV4StringToSignMatchesGetVanilla(t *testing.T) {
	creq := "GET\n/\n\ndate:Mon, 09 Sep 2011 23:36:00 GMT\nhost:host.foo.com\n\ndate;host\n" + emptyPayloadHash()
	got := stringToSign(sigV4TestAmzDate, "20110909/us-east-1/host/aws4_request", sha256Hex([]byte(creq)))
	want := "AWS4-HMAC-SHA256\n20110909T233600Z\n20110909/us-east-1/host/aws4_request\n" +
		"366b91fb121d72a00f46bbe8d395f53a102b06dfb7e79636515208ed3fa606b1"
	if got != want {
		t.Fatalf("string to sign =\n%q\nwant\n%q", got, want)
	}
}

func TestAWSURIEncode(t *testing.T) {
	cases := []struct {
		in          string
		encodeSlash bool
		want        string
	}{
		{"-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", false, "-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"},
		{"a/b", false, "a/b"},
		{"a/b", true, "a%2Fb"},
		{"a b", true, "a%20b"},
		{"a=b", true, "a%3Db"},
	}
	for _, tc := range cases {
		if got := awsURIEncode(tc.in, tc.encodeSlash); got != tc.want {
			t.Errorf("awsURIEncode(%q, %v) = %q, want %q", tc.in, tc.encodeSlash, got, tc.want)
		}
	}
}

func TestCanonicalQuerySortsByKeyThenValue(t *testing.T) {
	got := canonicalQuery([][2]string{
		{"b", "1"},
		{"a", "2"},
		{"a", "1"},
	})
	want := "a=1&a=2&b=1"
	if got != want {
		t.Fatalf("canonicalQuery = %q, want %q", got, want)
	}
}
