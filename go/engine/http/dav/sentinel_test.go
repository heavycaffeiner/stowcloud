//go:build linux

package dav

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every refusal this package makes has to reach the status table.
//
// A sentinel the table does not recognise falls through to 500, so a client
// that sent a malformed header is told the server broke and retries the same
// request forever. This has already happened twice: once when the lock
// service's sentinel was never translated, and once when the upload
// collection's five were added without a table entry.

// mappedSentinels is every sentinel, with the status it must produce. Adding
// one here is not optional: the completeness check below fails until it is
// listed.
//
// A function rather than a package variable so nothing can mutate the table
// between tests running in parallel.
func mappedSentinels() map[string]struct {
	err  error
	want int
} {
	return map[string]struct {
		err  error
		want int
	}{
		"ErrNoElements":         {ErrNoElements, http.StatusBadRequest},
		"ErrBadDepth":           {ErrBadDepth, http.StatusBadRequest},
		"ErrBadEscape":          {ErrBadEscape, http.StatusBadRequest},
		"ErrBadIf":              {ErrBadIf, http.StatusBadRequest},
		"ErrBadLockDepth":       {ErrBadLockDepth, http.StatusBadRequest},
		"ErrBadPropertyName":    {ErrBadPropertyName, http.StatusBadRequest},
		"ErrBadUploadLength":    {ErrBadUploadLength, http.StatusBadRequest},
		"ErrBadUploadMTime":     {ErrBadUploadMTime, http.StatusBadRequest},
		"ErrBodyTooLarge":       {ErrBodyTooLarge, http.StatusBadRequest},
		"ErrChunkLeadingZero":   {ErrChunkLeadingZero, http.StatusBadRequest},
		"ErrChunkNotDecimal":    {ErrChunkNotDecimal, http.StatusBadRequest},
		"ErrChunkOnCollection":  {ErrChunkOnCollection, http.StatusBadRequest},
		"ErrChunkRange":         {ErrChunkRange, http.StatusBadRequest},
		"ErrDirective":          {ErrDirective, http.StatusBadRequest},
		"ErrDotSegment":         {ErrDotSegment, http.StatusBadRequest},
		"ErrEncodedSeparator":   {ErrEncodedSeparator, http.StatusBadRequest},
		"ErrForeignDestination": {ErrForeignDestination, http.StatusBadGateway},
		"ErrIfTooLarge":         {ErrIfTooLarge, http.StatusBadRequest},
		"ErrLocked":             {ErrLocked, http.StatusLocked},
		"ErrNUL":                {ErrNUL, http.StatusBadRequest},
		"ErrNameTooLong":        {ErrNameTooLong, http.StatusBadRequest},
		"ErrNoBody":             {ErrNoBody, http.StatusBadRequest},
		"ErrNoDestination":      {ErrNoDestination, http.StatusBadRequest},
		"ErrNoLockTable":        {ErrNoLockTable, http.StatusMethodNotAllowed},
		"ErrNoLockToken":        {ErrNoLockToken, http.StatusBadRequest},
		"ErrNoPropertyStore":    {ErrNoPropertyStore, http.StatusConflict},
		"ErrNoUploadLength":     {ErrNoUploadLength, http.StatusBadRequest},
		"ErrPreconditionFailed": {ErrPreconditionFailed, http.StatusPreconditionFailed},
		"ErrProcInst":           {ErrProcInst, http.StatusBadRequest},
		"ErrTooDeep":            {ErrTooDeep, http.StatusBadRequest},
		"ErrTooManyElements":    {ErrTooManyElements, http.StatusBadRequest},
		"ErrTooManyProperties":  {ErrTooManyProperties, http.StatusBadRequest},
		"ErrTooMuchText":        {ErrTooMuchText, http.StatusBadRequest},
		"ErrUndeclaredPrefix":   {ErrUndeclaredPrefix, http.StatusBadRequest},
		"ErrUnsupportedMedia":   {ErrUnsupportedMedia, http.StatusUnsupportedMediaType},
	}
}

// No sentinel falls through to 500. That is the failure this guards.
func TestEverySentinelHasAStatus(t *testing.T) {
	t.Parallel()

	for name, c := range mappedSentinels() {
		got, _ := StatusOf(c.err)
		// 500 exactly is the fall-through the table produces for something it
		// does not recognise. A deliberate 5xx such as the 502 a foreign
		// destination earns is a mapping, not a miss, so the comparison below
		// is what actually decides.
		if got == http.StatusInternalServerError && c.want != http.StatusInternalServerError {
			t.Errorf("%s answers 500: the status table does not know it", name)
			continue
		}
		if got != c.want {
			t.Errorf("%s answers %d, want %d", name, got, c.want)
		}
	}
}

// The list above covers every sentinel the package declares.
//
// Without this, adding a sentinel and forgetting the table passes: the test
// only checks what it was told about. Reading the source is what makes the
// list self-maintaining.
func TestTheSentinelListIsComplete(t *testing.T) {
	t.Parallel()

	declared := declaredSentinels(t)
	if len(declared) == 0 {
		t.Fatal("no sentinels found: the scan is broken, not the package")
	}

	for _, name := range declared {
		if _, ok := mappedSentinels()[name]; !ok {
			t.Errorf("%s is declared but not in the mapped list, so nothing checks its status", name)
		}
	}
	for name := range mappedSentinels() {
		if !slicesContains(declared, name) {
			t.Errorf("%s is in the mapped list but no longer declared", name)
		}
	}
}

// declaredSentinels reads the package's own source for exported error values.
func declaredSentinels(t *testing.T) []string {
	t.Helper()

	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing the package source: %v", err)
	}

	pattern := regexp.MustCompile(`(?m)^\s*(Err[A-Za-z0-9]*)\s*=\s*errors\.New\(`)
	var out []string
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("reading %s: %v", path, rerr)
		}
		for _, m := range pattern.FindAllStringSubmatch(string(body), -1) {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
