// routecheck compares the paths the frontend calls against the routes the
// server mounts.
//
// The two are halves of one contract and nothing else in this tree checks that
// they agree. Every phase tested its own package, no phase tested that a
// request arrives, and the gate validates what is in the route table without
// ever asking whether it is the table the client needs. A route the client
// calls and the server does not mount was invisible to every check here.
//
// It found 39 of them, including login: the server mounted it on the
// change-password path, so nobody could sign in from the shipped interface
// while every test in the tree was green.
//
// It compares in both directions. A path the client calls and the server does
// not mount is a screen that cannot work, and it fails the check. A route the
// server mounts that no client calls is reported and does not fail: it is
// usually correct, because WebDAV clients, sync clients and operators call
// routes the web interface never does, and a check that failed on it would be
// a check people learn to silence.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	var (
		clientDir      = flag.String("client-dir", "web/src/lib/api", "the frontend's API client directory")
		routesPath     = flag.String("routes", "go/internal/server/routes.go", "the server's route table")
		allowPath      = flag.String("allow", "go/routes.allow", "paths the client may call that the server need not mount")
		serverOnlyPath = flag.String("server-only", "go/routes.server-only",
			"routes the server mounts for callers other than the web client")
	)
	flag.Parse()

	// Every file in the directory, not one named file. The streaming search
	// and the change channel live in their own modules, so a check pointed at
	// the main one saw neither: search was mounted nowhere and nothing
	// reported it.
	client, err := readClient(*clientDir)
	if err != nil {
		fail("reading the client: %v", err)
	}
	routes, err := os.ReadFile(filepath.Clean(*routesPath))
	if err != nil {
		fail("reading the route table: %v", err)
	}
	allowed := readAllow(*allowPath)

	called := clientPaths(client)
	mounted := mountedPaths(string(routes))
	serverOnly := readAllow(*serverOnlyPath)

	var missing []string
	for _, c := range called {
		if mounted[c] || allowed[c.path] || matchesWildcard(c, mounted) {
			continue
		}
		// A path mounted under a different verb is the more useful message:
		// the route exists and refuses, which reads to a client as the screen
		// being broken rather than the path being absent.
		if other := verbsFor(c.path, mounted); len(other) > 0 {
			missing = append(missing, fmt.Sprintf("%s %s (mounted as %s)",
				c.method, c.path, strings.Join(other, ", ")))
			continue
		}
		missing = append(missing, c.method+" "+c.path)
	}
	sort.Strings(missing)

	say(os.Stdout, "the client calls %d paths; the server mounts %d\n", len(called), len(mounted))

	// The other direction. Reported, never fatal: most of these are correct,
	// because the routes a sync client or an operator calls are not ones the
	// web interface has a screen for. What it catches is a route left behind
	// by a client change, which is a maintenance cost rather than a fault.
	if unused := unusedRoutes(mounted, called, serverOnly); len(unused) > 0 {
		say(os.Stdout, "\n%d mounted routes no client module calls:\n", len(unused))
		for _, p := range unused {
			say(os.Stdout, "  %s\n", p)
		}
		say(os.Stdout, "\nEach is either a route for a caller that is not the web client, "+
			"which belongs in %s,\nor one the client stopped calling, which belongs deleted.\n",
			*serverOnlyPath)
	}

	if len(missing) == 0 {
		say(os.Stdout, "every path the client calls is mounted\n")
		return
	}

	say(os.Stderr, "\n%d calls the client makes are not mounted:\n", len(missing))
	for _, p := range missing {
		say(os.Stderr, "  %s\n", p)
	}
	say(os.Stderr, "\nA path here is a screen that cannot work. Mount it, or record it in %s\n"+
		"with the reason it is not needed.\n", *allowPath)
	os.Exit(1)
}

// readClient concatenates every TypeScript module under the client directory,
// tests excluded: a mock's calls are the mock's contract, not this server's.
//
// Recursive, and that is load-bearing rather than tidy. The resumable upload
// transport lives in a sibling directory, so a check that read one directory
// saw none of its four calls and reported every upload route as one no client
// makes. The client is not one directory and never was.
func readClient(dir string) (string, error) {
	var out []byte
	err := filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := e.Name()
		if e.IsDir() {
			// Built output and installed packages are not this client's source,
			// and reading them would compare the server against a dependency.
			if name == "node_modules" || name == "build" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".ts") || strings.Contains(name, ".test.") {
			return nil
		}
		body, rerr := os.ReadFile(path) //nolint:gosec // G304 reads the variable: the directory is the gate's own argument, never request input.
		if rerr != nil {
			return rerr
		}
		out = append(out, body...)
		out = append(out, '\n')
		return nil
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// clientPaths pulls every API path the client requests.
//
// The client builds paths by interpolation, so a segment that is a value
// becomes a placeholder here: what is being compared is the shape of the route,
// not one call's arguments.
var (
	// The type argument is optional and has to be allowed for. The client
	// writes request<SessionInfo>('/auth/session'), and a pattern demanding the
	// parenthesis right after the name matched none of those: eleven mounted
	// routes read as uncalled, which is noise in the direction this tool is
	// least able to afford it.
	requestCall = regexp.MustCompile("request(?:Blob|Raw)?(?:<[^(]*>)?\\(\\s*[`'\"]([^`'\"]+)")
	// The three calls that do not go through the client's own helper, and so
	// were invisible to this check while it looked for that helper alone. The
	// streaming search was mounted nowhere and nothing reported it, which is
	// the exact failure this tool exists to catch.
	//
	// Each names the base and then the path, so the base is what anchors the
	// match: a bare fetch to somewhere else is not this server's route table's
	// business.
	directCall = regexp.MustCompile("(?:fetch|new EventSource|new WebSocket)\\(\\s*`\\$\\{BASE\\}([^`]*)`")
	// A template hole can itself contain braces, as a call with an object
	// argument does, so the match is not "up to the first closing brace":
	// that leaves the remainder of the call in the path.
	interpolate = regexp.MustCompile(`\$\{(?:[^{}]|\{[^{}]*\})*\}`)
	queryTail   = regexp.MustCompile(`\?.*$`)
	// The client builds a query with a helper, which appears as a trailing
	// hole naming it.
	queryBuilder = regexp.MustCompile(`\$\{qs\((?:[^{}]|\{[^{}]*\})*\)\}$`)
	// The verb, which follows the path in the same call. Absent means GET,
	// which is what both the helper and a bare fetch default to.
	//
	// Checked because a path that is mounted under a different verb is a route
	// that exists and refuses: cancelling a job was mounted as a POST to one
	// path and called as a DELETE to another, so the client got "method not
	// allowed" from a comparison that only looked at paths and passed.
	methodCall = regexp.MustCompile(`method:\s*'([A-Z]+)'`)
)

// call is one path the client asks for, and how.
type call struct {
	method string
	path   string
}

func clientPaths(src string) []call {
	seen := map[call]bool{}
	var out []call
	for _, re := range []*regexp.Regexp{requestCall, directCall} {
		for _, loc := range re.FindAllStringSubmatchIndex(src, -1) {
			p := normalise(src[loc[2]:loc[3]])
			// The helper's own body is the one fetch whose path is the
			// argument every other call already supplied, so it normalises to
			// a bare placeholder rather than a route.
			if p == "" || p == "/api{}" {
				continue
			}
			c := call{method: methodOf(src, loc[1]), path: p}
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return out[i].method < out[j].method
	})
	return out
}

// methodOf reads the verb out of the options object that follows a call.
//
// Bounded to that call's own arguments rather than a fixed window: a window
// reads the next function's verb as this one's, which reports a mounted route
// as missing and buries the real mismatches in noise.
//
// The scan stops at the closing parenthesis of the call, tracking depth so an
// object argument's own braces do not end it early. A call with no options is
// a GET, which is what both the helper and a bare fetch default to.
func methodOf(src string, from int) string {
	depth := 1
	i := from
	for ; i < len(src) && depth > 0; i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
	}
	if m := methodCall.FindStringSubmatch(src[from:i]); m != nil {
		return m[1]
	}
	return "GET"
}

// mountedPaths pulls every method and pattern the route table registers.
func mountedPaths(src string) map[call]bool {
	route := regexp.MustCompile(`Method:\s*"([A-Z]+)",\s*Pattern:\s*"([^"]+)"`)
	out := map[call]bool{}
	for _, m := range route.FindAllStringSubmatch(src, -1) {
		out[call{method: m[1], path: normalise(m[2])}] = true
	}
	return out
}

// unusedRoutes is every mounted route no client call reaches.
//
// The wildcard match runs the other way round here: the client names a value
// where the server names a wildcard, so the mounted pattern is the pattern and
// the client's path is the concrete one.
func unusedRoutes(mounted map[call]bool, called []call, allowed map[string]bool) []string {
	var out []string
	for m := range mounted {
		if allowed[m.path] {
			continue
		}
		used := false
		for _, c := range called {
			if c.method == m.method && pathMatches(c.path, m.path) {
				used = true
				break
			}
		}
		if !used {
			out = append(out, m.method+" "+m.path)
		}
	}
	sort.Strings(out)
	return out
}

// matchesWildcard reports whether a concrete path is covered by a mounted
// pattern with a wildcard segment.
//
// The server mounts one route for a family of names, such as every settings
// section, and the client names each one. Comparing the two literally would
// report a mounted route as missing, and adding each name to the ledger would
// hide the ones that really are.
func matchesWildcard(c call, mounted map[call]bool) bool {
	for m := range mounted {
		if m.method == c.method && pathMatches(c.path, m.path) {
			return true
		}
	}
	return false
}

// verbsFor lists the methods a path is mounted under, for a call whose own
// method is not among them.
func verbsFor(path string, mounted map[call]bool) []string {
	var out []string
	for m := range mounted {
		if pathMatches(path, m.path) {
			out = append(out, m.method)
		}
	}
	sort.Strings(out)
	return out
}

func pathMatches(path, pattern string) bool {
	want := strings.Split(path, "/")
	have := strings.Split(pattern, "/")
	if len(have) != len(want) {
		return false
	}
	for i := range have {
		if have[i] != "{}" && have[i] != want[i] {
			return false
		}
	}
	return true
}

// normalise reduces a path to the shape both sides can be compared on: one
// leading prefix, no query, and every value segment as a placeholder.
func normalise(p string) string {
	// A hole that builds a query string sits at the end and is not part of the
	// path, so it is dropped rather than becoming a segment.
	p = queryBuilder.ReplaceAllString(p, "")
	p = interpolate.ReplaceAllString(p, "{}")
	p = queryTail.ReplaceAllString(p, "")
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	// The client's own base is the API prefix, so its paths are relative to it
	// and the table's are absolute.
	if !strings.HasPrefix(p, "/api") {
		p = "/api" + p
	}
	// A named wildcard on the server and a value on the client are the same
	// segment.
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "{}"
		}
	}
	return strings.TrimSuffix(strings.Join(segs, "/"), "/")
}

// readAllow reads the recorded exceptions. A path is listed there with the
// reason the server does not have to mount it, which is a decision somebody
// made rather than one this tool infers.
func readAllow(path string) map[string]bool {
	out := map[string]bool{}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[normalise(line)] = true
	}
	return out
}

func fail(format string, args ...any) {
	say(os.Stderr, "routecheck: "+format+"\n", args...)
	os.Exit(2)
}

// say writes with the error checked. A tool whose job is reporting a mismatch
// should not itself drop a write.
func say(w *os.File, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		// The stream this was meant to report on is gone; there is nowhere
		// left to say so.
		os.Exit(2)
	}
}
