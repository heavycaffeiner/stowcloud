package main

import (
	"errors"
	"fmt"
	"strings"
)

// The one bootstrap value.
//
// Everything a deployment is configured with lives in the database, and a
// process has to know where that is before it can read any of it. So this is
// an argument rather than a setting, and it is the only one.

// defaultDataDir is where a container image puts the store. An operator who
// mounts a volume somewhere else says so on the command line.
const defaultDataDir = "/var/lib/stowcloud"

// dataDirArg reads --data-dir out of a subcommand's arguments.
//
// Both spellings, because the two exist in the wild and refusing one of them
// is a startup failure over a hyphen: `--data-dir DIR` and `--data-dir=DIR`.
// Anything else is an error naming what was passed, rather than a directory
// silently taken from a flag this does not have.
func dataDirArg(args []string) (string, error) {
	dir, emergencyOnly, err := serveArgs(args)
	if err != nil {
		return "", err
	}
	if emergencyOnly {
		// Only serve has a listener to bring up in safe mode. Accepting it
		// silently elsewhere would be a flag that reads as doing something.
		return "", errors.New(`unexpected argument "--emergency"`)
	}
	return dir, nil
}

// serveArgs reads what serve takes: the data directory, and whether to bring
// up only the emergency layer.
//
// --emergency is a flag rather than a setting for the obvious reason: it is
// the answer to a stored setting that stops the server starting, so it cannot
// itself be stored.
func serveArgs(args []string) (dir string, emergencyOnly bool, err error) {
	dir = defaultDataDir
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--data-dir":
			if i+1 >= len(args) {
				return "", false, errors.New("--data-dir needs a directory")
			}
			dir = args[i+1]
			i++
		case strings.HasPrefix(a, "--data-dir="):
			dir = strings.TrimPrefix(a, "--data-dir=")
		case a == "--emergency":
			emergencyOnly = true
		default:
			return "", false, fmt.Errorf("unexpected argument %q", a)
		}
	}
	if strings.TrimSpace(dir) == "" {
		return "", false, errors.New("--data-dir needs a directory")
	}
	return dir, emergencyOnly, nil
}
