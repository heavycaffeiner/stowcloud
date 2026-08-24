//go:build ignore

// A plaintext front for a TLS server, so a conformance suite with no TLS
// support can reach one.
//
// It exists because the standard WebDAV suite is a 2005-era program built
// without TLS on this host, and this server has no plaintext listener by
// design. Proxying is the honest way round that: the alternative is adding a
// plaintext listener to the thing being tested, which would test something
// other than what ships.
//
// Run: go run scripts/tlsproxy.go -listen 127.0.0.1:8080 -target https://127.0.0.1:8443 -host localhost
package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "the plaintext address to accept on")
	target := flag.String("target", "", "the TLS server to forward to")
	host := flag.String("host", "localhost", "the Host header to forward under")
	flag.Parse()

	if *target == "" {
		log.Fatal("a target is required")
	}
	u, err := url.Parse(*target)
	if err != nil {
		log.Fatalf("the target is not a URL: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = &http.Transport{
		// The target presents a certificate generated into its own data
		// directory and this proxy is on loopback beside it, so there is
		// nothing to verify against. That is a statement about this script.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // a loopback test proxy.
	}
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		// The server answers only for names it was configured with, and the
		// suite addresses it by whatever this proxy listens on.
		r.Host = *host
	}

	log.Printf("proxying %s to %s as %s", *listen, *target, *host)
	srv := &http.Server{Addr: *listen, Handler: proxy}
	log.Fatal(srv.ListenAndServe())
}
