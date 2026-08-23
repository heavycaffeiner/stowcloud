// The response differ.
//
// It drives one request corpus against two servers and reports every
// difference in status, headers and body. The corpus is checked in rather than
// generated, so a difference is reproducible and a regression is a diff.
//
// What it is for: every other check in this tree was written by whoever wrote
// the code it checks. This one compares against an artefact that already
// exists and was not written for the purpose: a recorded set of the previous
// implementation's actual responses.
package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// usage explains the tool. Written once and printed with a checked write, so a
// closed stderr is not an unnoticed error in the tool that exists to notice.
func usage() {
	const text = `usage: differ -old URL -new URL [-corpus dir] [-group name]

  Runs the checked-in corpus against both servers and reports every difference
  outside the allow list. Each allowed field carries the reason it is allowed.

  Credentials come from the environment, because a session token written into
  the corpus is wrong on every run but the one that minted it:
    SC_DIFFER_SESSION          the session cookie value
    SC_DIFFER_CSRF             the token derived from it
    SC_DIFFER_EXPIRED_SESSION  a session that has already expired
    SC_DIFFER_CONTENT_HOST     the content origin
`
	if _, err := io.WriteString(os.Stderr, text); err != nil {
		// Nothing left to report it on.
		_ = err
	}
}

func main() {
	var (
		oldBase = flag.String("old", "", "base URL of the reference server")
		newBase = flag.String("new", "", "base URL of the server under test")
		dir     = flag.String("corpus", "corpus", "the corpus directory")
		host    = flag.String("host", "localhost", "the Host header every request carries")
		only    = flag.String("group", "", "run one group only")
		// The credential is per side, because the two implementations do not
		// agree on the cookie's name and a session minted against one is not
		// valid on the other. One value for both would send each side
		// something it ignores, and two unauthenticated answers match.
		oldCookie = flag.String("old-cookie", "", "reference session, as name=value")
		newCookie = flag.String("new-cookie", "", "session under test, as name=value")
		// The token that goes with each session. Without it a state-changing
		// request is refused by the guard on both sides, which compares two
		// refusals rather than the two handlers.
		oldCSRF = flag.String("old-csrf", "", "reference session's request token")
		newCSRF = flag.String("new-csrf", "", "token for the session under test")
	)
	flag.Parse()

	if *oldBase == "" || *newBase == "" {
		usage()
		os.Exit(2)
	}

	groups, err := loadCorpus(*dir, *only)
	if err != nil {
		say("differ: %v\n", err)
		os.Exit(1)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Both servers present a certificate generated into their own data
			// directory, and the differ is talking to loopback. It verifies
			// nothing because there is nothing to verify against, which is a
			// statement about this tool and not about the servers.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // G402: a loopback differ against two self-signed test servers.
		},
		// A redirect is a difference worth seeing rather than following.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	report := &Report{}
	for _, g := range groups {
		for _, req := range g.Requests {
			oldResp, oerr := send(client, *oldBase, *host, req, credential{*oldCookie, *oldCSRF})
			newResp, nerr := send(client, *newBase, *host, req, credential{*newCookie, *newCSRF})
			if oerr != nil || nerr != nil {
				report.Errors = append(report.Errors, RequestError{
					Group: g.Group, Name: req.Name,
					Old: errText(oerr), New: errText(nerr),
				})
				continue
			}
			if diffs := compare(oldResp, newResp); len(diffs) > 0 {
				report.Differences = append(report.Differences, Difference{
					Group: g.Group, Name: req.Name,
					Method: req.Method, Path: req.Path,
					Fields: diffs,
				})
			}
			report.Compared++
		}
	}

	if werr := report.Write(os.Stdout); werr != nil {
		os.Exit(1)
	}
	if len(report.Differences) > 0 || len(report.Errors) > 0 {
		os.Exit(1)
	}
}

// Group is one corpus file.
type Group struct {
	Group    string    `json:"group"`
	Note     string    `json:"note"`
	Requests []Request `json:"requests"`
}

// Request is one corpus entry.
type Request struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Auth    string            `json:"auth"`
	Host    string            `json:"host"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func loadCorpus(dir, only string) ([]Group, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading the corpus: %w", err)
	}
	var out []Group
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // G304: the corpus directory is this tool's own argument.
		if rerr != nil {
			return nil, rerr
		}
		var g Group
		if uerr := json.Unmarshal(raw, &g); uerr != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), uerr)
		}
		if only != "" && g.Group != only {
			continue
		}
		out = append(out, g)
	}
	if len(out) == 0 {
		return nil, errors.New("the corpus is empty, and a differ with nothing to send reports success")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out, nil
}

// Response is what one server answered.
type Response struct {
	Status  int
	Headers http.Header
	Body    []byte
}

// credential is one side's session and the token that goes with it.
type credential struct {
	Cookie string
	CSRF   string
}

func send(c *http.Client, base, host string, r Request, cred credential) (*Response, error) {
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(expand(r.Body))
	}
	req, err := http.NewRequest(r.Method, base+r.Path, body) //nolint:noctx // the client's own timeout bounds this.
	if err != nil {
		return nil, err
	}
	req.Host = host
	if r.Host != "" {
		req.Host = r.Host
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	applyAuth(req, r.Auth, cred)

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			say("differ: closing a body: %v\n", cerr)
		}
	}()
	// Bounded: the differ is talking to a server that may be misbehaving, and
	// this is the tool that is supposed to notice.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCapturedBody))
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Headers: resp.Header, Body: raw}, nil
}

// maxCapturedBody bounds what one response contributes to a report.
const maxCapturedBody = 4 << 20

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// say writes to stderr with the error checked, because an unchecked write in
// the tool whose job is noticing things is the wrong place to start.
func say(format string, args ...any) {
	if _, err := fmt.Fprintf(os.Stderr, format, args...); err != nil {
		_ = err
	}
}
