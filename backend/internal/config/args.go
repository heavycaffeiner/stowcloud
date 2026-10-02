// Command-line arguments for the deployment commands. Every command spells a
// flag the same way: --data-dir, --addr and --plain.

package config

import (
	"flag"
	"fmt"
	"io"
)

// DefaultDataDir is the deployment data directory used when no directory is named.
const DefaultDataDir = "/var/lib/stowcloud"

// ServeArgs is the command line for serving.
type ServeArgs struct {
	// Addr is empty when the server should use its stored bind address.
	Addr string
	// DataDir is where deployment state is stored.
	DataDir string
	// Plain selects HTTP instead of HTTPS.
	Plain bool
}

// ParseServeArgs parses the serve flags. Usage and errors go to out.
func ParseServeArgs(name string, argv []string, out io.Writer) (ServeArgs, error) {
	var args ServeArgs
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&args.DataDir, "data-dir", DefaultDataDir, "data directory")
	fs.StringVar(&args.Addr, "addr", "", "listen address; overrides the stored one")
	fs.BoolVar(&args.Plain, "plain", false, "serve HTTP instead of HTTPS")
	if err := fs.Parse(argv); err != nil {
		return ServeArgs{}, err
	}
	if fs.NArg() > 0 {
		return ServeArgs{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return args, nil
}

// ParseDataDirArgs parses a command line whose one flag is --data-dir, which
// may come before or after the positional arguments. It returns the directory
// and the positional arguments in order.
func ParseDataDirArgs(name string, argv []string, out io.Writer) (string, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	dataDir := fs.String("data-dir", DefaultDataDir, "data directory")
	var positional []string
	for {
		if err := fs.Parse(argv); err != nil {
			return "", nil, err
		}
		if fs.NArg() == 0 {
			return *dataDir, positional, nil
		}
		positional = append(positional, fs.Arg(0))
		argv = fs.Args()[1:]
	}
}
