package args

import (
	"reflect"
	"testing"
)

func TestParseServeArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want ServeArgs
	}{
		{name: "defaults", want: ServeArgs{DataDir: DefaultDataDir}},
		{name: "value flag and switch", args: []string{"--data-dir", "/d", "--plain"}, want: ServeArgs{DataDir: "/d", Plain: true}},
		{name: "switch and value flag", args: []string{"--plain", "--addr", ":9000"}, want: ServeArgs{DataDir: DefaultDataDir, Plain: true, Addr: ":9000"}},
		{name: "all flags", args: []string{"--addr", ":9000", "--data-dir", "/d", "--plain"}, want: ServeArgs{Addr: ":9000", DataDir: "/d", Plain: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseServeArgs(c.args)
			if err != nil {
				t.Fatalf("parsing %v: %v", c.args, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parsed %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestParseServeArgsRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--data-dir"}, {"--addr"}, {"--plain", "--data-dir"}, {"--nosuch"}} {
		if _, err := ParseServeArgs(args); err == nil {
			t.Errorf("%v parsed without an error", args)
		}
	}
}

func TestParseSettingsArgs(t *testing.T) {
	for _, c := range []struct {
		args    []string
		section string
		dataDir string
	}{
		{args: []string{"shares", "--data-dir", "/d"}, section: "shares", dataDir: "/d"},
		{args: []string{"--data-dir", "/d", "shares"}, section: "shares", dataDir: "/d"},
		{args: nil, dataDir: DefaultDataDir},
	} {
		section, dataDir := ParseSettingsArgs(c.args)
		if section != c.section || dataDir != c.dataDir {
			t.Errorf("ParseSettingsArgs(%v) = %q, %q; want %q, %q", c.args, section, dataDir, c.section, c.dataDir)
		}
	}
}

func TestDataDir(t *testing.T) {
	if got := DataDir([]string{"--data-dir", "/d"}); got != "/d" {
		t.Errorf("DataDir returned %q", got)
	}
	if got := DataDir(nil); got != DefaultDataDir {
		t.Errorf("DataDir default = %q", got)
	}
}
