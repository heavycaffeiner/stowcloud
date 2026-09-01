//go:build linux

package main

import (
	"reflect"
	"testing"
)

// The deploy command line parses, including the flags that take a value.
//
// A flag taking a value has to advance past both itself and the value.
// Advancing by one left the value to be read as the next flag, so a
// deployment passing --data-dir was told its own directory was an unknown
// argument and the container refused to start.
func TestTheServeArgumentsParse(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want serveArgs
	}{
		{
			name: "nothing takes the defaults",
			args: nil,
			want: serveArgs{DataDir: deployDataDir, Shares: []string{deploySharesRoot}},
		},
		{
			name: "a value flag does not swallow the next one",
			args: []string{"--data-dir", "/d", "--plain"},
			want: serveArgs{DataDir: "/d", Plain: true, Shares: []string{deploySharesRoot}},
		},
		{
			name: "a bare switch does not skip what follows",
			args: []string{"--plain", "--shares", "/mnt"},
			want: serveArgs{DataDir: deployDataDir, Plain: true, Shares: []string{"/mnt"}},
		},
		{
			name: "every flag together",
			args: []string{"--addr", ":9000", "--data-dir", "/d", "--shares", "/mnt", "--plain"},
			want: serveArgs{Addr: ":9000", DataDir: "/d", Plain: true, Shares: []string{"/mnt"}},
		},
		{
			// A deployment can mount folders in more than one place, and the
			// sandbox has to name every one before it is installed.
			name: "shares repeats",
			args: []string{"--shares", "/mnt/a", "--shares", "/mnt/b"},
			want: serveArgs{DataDir: deployDataDir, Shares: []string{"/mnt/a", "/mnt/b"}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseServeArgs(c.args)
			if err != nil {
				t.Fatalf("parsing %v: %v", c.args, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parsed %+v, want %+v", got, c.want)
			}
		})
	}
}

// A flag missing its value is refused rather than read past the end.
func TestAServeFlagWithoutItsValueIsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"--data-dir"},
		{"--addr"},
		{"--shares"},
		{"--plain", "--data-dir"},
	} {
		if _, err := parseServeArgs(args); err == nil {
			t.Errorf("%v parsed without its value", args)
		}
	}
}

// An argument this command does not have is refused, never ignored.
func TestAnUnknownServeArgumentIsRefused(t *testing.T) {
	if _, err := parseServeArgs([]string{"--nosuch"}); err == nil {
		t.Error("an unknown argument was accepted")
	}
}
