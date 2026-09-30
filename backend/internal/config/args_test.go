package config

import (
	"io"
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
		{name: "equals form", args: []string{"--data-dir=/d"}, want: ServeArgs{DataDir: "/d"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseServeArgs("serve", c.args, io.Discard)
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
	for _, args := range [][]string{{"--data-dir"}, {"--addr"}, {"--plain", "--data-dir"}, {"--nosuch"}, {"-data", "/d"}, {"stray"}} {
		if _, err := ParseServeArgs("serve", args, io.Discard); err == nil {
			t.Errorf("%v parsed without an error", args)
		}
	}
}

func TestParseDataDirArgs(t *testing.T) {
	for _, c := range []struct {
		args       []string
		dataDir    string
		positional []string
	}{
		{args: []string{"shares", "--data-dir", "/d"}, dataDir: "/d", positional: []string{"shares"}},
		{args: []string{"--data-dir", "/d", "shares"}, dataDir: "/d", positional: []string{"shares"}},
		{args: nil, dataDir: DefaultDataDir},
	} {
		dataDir, positional, err := ParseDataDirArgs("settings", c.args, io.Discard)
		if err != nil {
			t.Fatalf("parsing %v: %v", c.args, err)
		}
		if dataDir != c.dataDir || !reflect.DeepEqual(positional, c.positional) {
			t.Errorf("ParseDataDirArgs(%v) = %q, %q; want %q, %q", c.args, dataDir, positional, c.dataDir, c.positional)
		}
	}
	if _, _, err := ParseDataDirArgs("settings", []string{"-data", "/d"}, io.Discard); err == nil {
		t.Error("the old -data spelling parsed without an error")
	}
}
