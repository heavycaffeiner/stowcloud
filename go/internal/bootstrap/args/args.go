// Package args parses command-line arguments for deployment commands.
package args

import "fmt"

// DefaultDataDir is the deployment data directory used when no directory is named.
const DefaultDataDir = "/var/lib/stowcloud"

// ServeArgs is the command line for the serve deployment command.
type ServeArgs struct {
	// Addr is empty when the server should use its stored bind address.
	Addr string
	// DataDir is where deployment state is stored.
	DataDir string
	// Plain selects HTTP instead of HTTPS.
	Plain bool
}

// ParseServeArgs parses the longhand flags accepted by the serve command.
func ParseServeArgs(argv []string) (ServeArgs, error) {
	out := ServeArgs{DataDir: DefaultDataDir}

	for i := 0; i < len(argv); {
		flag := argv[i]
		value := func() (string, bool) {
			if i+1 >= len(argv) {
				return "", false
			}
			return argv[i+1], true
		}
		switch flag {
		case "--data-dir", "-data":
			v, ok := value()
			if !ok {
				return ServeArgs{}, fmt.Errorf("%s needs a directory", flag)
			}
			out.DataDir = v
			i += 2
		case "--addr", "-addr":
			v, ok := value()
			if !ok {
				return ServeArgs{}, fmt.Errorf("%s needs an address", flag)
			}
			out.Addr = v
			i += 2
		case "--plain":
			out.Plain = true
			i++
		default:
			return ServeArgs{}, fmt.Errorf("unknown argument %q", flag)
		}
	}

	return out, nil
}

// ParseSettingsArgs extracts the section and data directory from settings args.
// The section and data-directory flag may occur in either order.
func ParseSettingsArgs(argv []string) (section, dataDir string) {
	dataDir = DefaultDataDir
	for i := 0; i < len(argv); i++ {
		if argv[i] == "-data" || argv[i] == "--data-dir" {
			if i+1 < len(argv) {
				dataDir = argv[i+1]
				i++
			}
			continue
		}
		if section == "" {
			section = argv[i]
		}
	}
	return section, dataDir
}

// DataDir extracts a data directory from a flat argument list.
func DataDir(argv []string) string {
	for i, arg := range argv {
		if (arg == "-data" || arg == "--data-dir") && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return DefaultDataDir
}
