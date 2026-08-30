package lifecycle_test

import (
	"net/http/httptest"
	"testing"
)

func TestDbgR(t *testing.T) {
	f := newFixture(t)
	m := f.mounted()

	w := httptest.NewRecorder()
	m.ServeHTTP(w, asDavUser(request("MKCOL", "/dav-uploads/tid-b", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "5",
	})))
	t.Logf("MKCOL -> %d", w.Code)

	w = httptest.NewRecorder()
	m.ServeHTTP(w, asDavUser(request("PUT", "/dav-uploads/tid-b/1", "stray", map[string]string{
		"Destination": "/dav/safe/out.bin",
	})))
	t.Logf("stray PUT -> %d", w.Code)

	w = httptest.NewRecorder()
	m.ServeHTTP(w, asDavUser(request("MOVE", "/dav-uploads/tid-b", "", map[string]string{
		"Destination":     "/dav/files/out.bin",
		"OC-Total-Length": "5",
	})))
	t.Logf("assemble -> %d", w.Code)
	t.Logf("out.bin: %v", f.exists("out.bin"))
	if f.exists("out.bin") {
		t.Logf("out.bin content: %q", f.read(t, "out.bin"))
	}
}
