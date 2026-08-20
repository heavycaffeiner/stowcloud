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
		clientPath = flag.String("client", "web/src/lib/api/http.ts", "the frontend's API client")
		routesPath = flag.String("routes", "go/internal/server/routes.go", "the server's route table")
		allowPath  = flag.String("allow", "go/routes.allow", "paths the client may call that the server need not mount")
	)
	flag.Parse()

	client, err := os.ReadFile(filepath.Clean(*clientPath))
	if err != nil {
		fail("reading the client: %v", err)
	}
	routes, err := os.ReadFile(filepath.Clean(*routesPath))
	if err != nil {
		fail("reading the route table: %v", err)
	}
	allowed := readAllow(*allowPath)

	called := clientPaths(string(client))
	mounted := mountedPaths(string(routes))

	var missing []string
	for _, p := range called {
		if mounted[p] || allowed[p] || matchesWildcard(p, mounted) {
			continue
		}
		missing = append(missing, p)
	}
	sort.Strings(missing)

	say(os.Stdout, "the client calls %d paths; the server mounts %d\n", len(called), len(mounted))
	if len(missing) == 0 {
		say(os.Stdout, "every path the client calls is mounted\n")
		return
	}

	say(os.Stderr, "\n%d paths the client calls are not mounted:\n", len(missing))
	for _, p := range missing {
		say(os.Stderr, "  %s\n", p)
	}
	say(os.Stderr, "\nA path here is a screen that cannot work. Mount it, or record it in %s\n"+
		"with the reason it is not needed.\n", *allowPath)
	os.Exit(1)
}

// clientPaths pulls every API path the client requests.
//
// The client builds paths by interpolation, so a segment that is a value
// becomes a placeholder here: what is being compared is the shape of the route,
// not one call's arguments.
var (
	requestCall = regexp.MustCompile("request(?:Blob|Raw)?\\(\\s*[`'\"]([^`'\"]+)")
	// A template hole can itself contain braces, as a call with an object
	// argument does, so the match is not "up to the first closing brace":
	// that leaves the remainder of the call in the path.
	interpolate = regexp.MustCompile(`\$\{(?:[^{}]|\{[^{}]*\})*\}`)
	queryTail   = regexp.MustCompile(`\?.*$`)
	// The client builds a query with a helper, which appears as a trailing
	// hole naming it.
	queryBuilder = regexp.MustCompile(`\$\{qs\((?:[^{}]|\{[^{}]*\})*\)\}$`)
)

func clientPaths(src string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range requestCall.FindAllStringSubmatch(src, -1) {
		p := normalise(m[1])
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// mountedPaths pulls every pattern the route table registers.
func mountedPaths(src string) map[string]bool {
	pattern := regexp.MustCompile(`Pattern:\s*"([^"]+)"`)
	out := map[string]bool{}
	for _, m := range pattern.FindAllStringSubmatch(src, -1) {
		out[normalise(m[1])] = true
	}
	return out
}

// matchesWildcard reports whether a concrete path is covered by a mounted
// pattern with a wildcard segment.
//
// The server mounts one route for a family of names, such as every settings
// section, and the client names each one. Comparing the two literally would
// report a mounted route as missing, and adding each name to the ledger would
// hide the ones that really are.
func matchesWildcard(path string, mounted map[string]bool) bool {
	want := strings.Split(path, "/")
	for pattern := range mounted {
		have := strings.Split(pattern, "/")
		if len(have) != len(want) {
			continue
		}
		ok := true
		for i := range have {
			if have[i] == "{}" || have[i] == want[i] {
				continue
			}
			ok = false
			break
		}
		if ok {
			return true
		}
	}
	return false
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
