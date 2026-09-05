package objstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// awsAlgorithm is the one signing algorithm SigV4 names; there is no
// negotiation, so it is a constant rather than a field on signer.
const awsAlgorithm = "AWS4-HMAC-SHA256"

// emptyPayloadHash is the SHA-256 of a zero-length body. It is a function
// rather than a precomputed constant so it is provably the hash of nothing
// rather than a magic string somebody has to trust, at the cost of one
// cheap hash per call.
func emptyPayloadHash() string { return sha256Hex(nil) }

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	if _, err := h.Write([]byte(data)); err != nil {
		panicInfallibleWrite(err)
	}
	return h.Sum(nil)
}

// panicInfallibleWrite stops the process when a write into a hash.Hash or a
// strings.Builder fails. Both types document Write as never returning an
// error, so a non-nil one here means that guarantee broke, and a signature
// computed after silently discarding it would be over bytes that were
// never actually written.
func panicInfallibleWrite(err error) {
	panic("objstore: an infallible write failed: " + err.Error())
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// signingKey derives the SigV4 signing key by chaining four HMACs over the
// date, the region, the service and a fixed terminator. Deriving it fresh
// per request, rather than caching it for a day, costs three cheap HMACs
// and means the secret key is the only long-lived value this package holds.
func signingKey(secretKey, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

// canonicalHeaderBlock renders the canonical-headers section of a canonical
// request: one "name:value\n" line per name, in the order given, which the
// caller must already have sorted. A header's value is passed through
// exactly as it will be sent; this package never sends a header whose value
// needs SigV4's whitespace-collapsing rule.
func canonicalHeaderBlock(names []string, values map[string]string) string {
	var b strings.Builder
	for _, n := range names {
		if _, err := b.WriteString(n); err != nil {
			panicInfallibleWrite(err)
		}
		if err := b.WriteByte(':'); err != nil {
			panicInfallibleWrite(err)
		}
		if _, err := b.WriteString(values[n]); err != nil {
			panicInfallibleWrite(err)
		}
		if err := b.WriteByte('\n'); err != nil {
			panicInfallibleWrite(err)
		}
	}
	return b.String()
}

// canonicalRequest assembles the six-line form SigV4 hashes and signs.
// canonicalHeaderBlock already ends in "\n", so joining with "\n" here is
// what produces the blank line the spec's own examples show between the
// headers block and the signed-headers line.
func canonicalRequest(method, uri, query, headerBlock, signedHeaders, payloadHashHex string) string {
	return strings.Join([]string{method, uri, query, headerBlock, signedHeaders, payloadHashHex}, "\n")
}

func stringToSign(amzDate, credentialScope, canonicalRequestHashHex string) string {
	return strings.Join([]string{awsAlgorithm, amzDate, credentialScope, canonicalRequestHashHex}, "\n")
}

// isUnreservedByte is RFC 3986's unreserved set, which SigV4 borrows
// unchanged: everything outside it is percent-encoded, byte by byte.
func isUnreservedByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

// awsURIEncode percent-encodes s per SigV4's URI-encoding rule, one byte at
// a time rather than one rune at a time, which is what makes a multi-byte
// UTF-8 character come out as the multiple %XY escapes the spec's own
// examples show. encodeSlash is false for a path, where '/' is a separator
// SigV4 leaves alone, and true for a query key or value, where '/' is
// escaped like any other reserved byte.
func awsURIEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := range s {
		c := s[i]
		if isUnreservedByte(c) || (c == '/' && !encodeSlash) {
			if err := b.WriteByte(c); err != nil {
				panicInfallibleWrite(err)
			}
			continue
		}
		if _, err := fmt.Fprintf(&b, "%%%02X", c); err != nil {
			panicInfallibleWrite(err)
		}
	}
	return b.String()
}

// canonicalQuery renders params in SigV4's canonical query-string form:
// pairs sorted by key and then by value, every key and value URI-encoded,
// and "=" always present even for a valueless key. It is used both to sign
// and, by the caller assigning its result straight to a request's RawQuery,
// to send, so the two can never disagree about what was signed.
func canonicalQuery(params [][2]string) string {
	sorted := make([][2]string, len(params))
	copy(sorted, params)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i][0] != sorted[j][0] {
			return sorted[i][0] < sorted[j][0]
		}
		return sorted[i][1] < sorted[j][1]
	})
	parts := make([]string, len(sorted))
	for i, kv := range sorted {
		parts[i] = awsURIEncode(kv[0], true) + "=" + awsURIEncode(kv[1], true)
	}
	return strings.Join(parts, "&")
}

// signer holds one share's S3 signing identity. The secret is kept as the
// raw bytes secret.Secret.Reveal() hands back at Open, since a request is
// signed far more often than the process is asked to zero it, and this
// package never logs or renders it.
type signer struct {
	accessKey string
	secret    []byte
	region    string
}

// sign computes req's Authorization header and the two amz headers SigV4
// requires alongside it, against the request as it stands: the caller adds
// any header that must be authenticated, such as x-amz-copy-source, only
// after sign returns, which is why this package never signs that header.
//
// Exactly three headers are signed: host, x-amz-content-sha256 and
// x-amz-date. That is the minimum SigV4 accepts, not an economy: every
// header this package sends beyond those three is either informational
// (Content-Type) or, like x-amz-copy-source, applies to one call this
// package itself issues and controls, so there is nothing an intermediary
// could tamper with by editing an unsigned header that this package would
// then act on differently.
func (s *signer) sign(req *http.Request, payloadHashHex string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := amzDate[:8]

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHashHex)

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	headerValues := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": payloadHashHex,
		"x-amz-date":           amzDate,
	}

	creq := canonicalRequest(
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonicalHeaderBlock(signedHeaders, headerValues),
		strings.Join(signedHeaders, ";"),
		payloadHashHex,
	)
	scope := dateStamp + "/" + s.region + "/s3/aws4_request"
	sts := stringToSign(amzDate, scope, sha256Hex([]byte(creq)))
	key := signingKey(string(s.secret), dateStamp, s.region, "s3")
	sig := hex.EncodeToString(hmacSHA256(key, sts))

	req.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		awsAlgorithm, s.accessKey, scope, strings.Join(signedHeaders, ";"), sig,
	))
}
