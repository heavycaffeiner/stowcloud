package objstore

import (
	"net/http"
	"net/url"
	"testing"
)

func TestRefuseCrossHostRedirectRejectsHTTPSDowngrade(t *testing.T) {
	origin, err := url.Parse("https://objects.example.test/bucket/source")
	if err != nil {
		t.Fatalf("parse origin: %v", err)
	}
	target, err := url.Parse("http://objects.example.test/bucket/source")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	err = refuseCrossHostRedirect(
		&http.Request{URL: target},
		[]*http.Request{{URL: origin}},
	)
	if err == nil {
		t.Fatal("HTTPS to HTTP redirect was accepted")
	}
}

func TestRefuseCrossHostRedirectRejectsSameHostS3Redirect(t *testing.T) {
	origin, err := url.Parse("https://objects.example.test/bucket/source")
	if err != nil {
		t.Fatalf("parse origin: %v", err)
	}
	target, err := url.Parse("https://objects.example.test/other-bucket/source")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	err = refuseCrossHostRedirect(
		&http.Request{URL: target},
		[]*http.Request{{URL: origin}},
	)
	if err == nil {
		t.Fatal("unsupported same-host S3 redirect was accepted")
	}
}
