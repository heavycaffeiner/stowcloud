package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/archive"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Packing an archive.
//
// It streams. No length is promised and none is knowable: the sizes come from
// the walk as it goes, and a file can change under it. That is what makes the
// useful failure possible, because an entry cut short because the file vanished
// still leaves a valid archive.
//
// The response is committed the moment the first byte goes out, so every
// refusal that can happen has to happen before that: the paths are resolved and
// checked first, and a failure after the first byte is a truncated archive
// rather than an error the client can read.

// ArchiveCreate answers POST /api/fs/archive.
func ArchiveCreate(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			Paths []string `json:"paths"`
			Name  string   `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if len(req.Paths) == 0 {
			return apierr.BadRequest("archive.no_paths", "paths")
		}
		if len(req.Paths) > limits.ArchiveEntriesListed {
			return limits.Exceed("archive paths", limits.ArchiveEntriesListed, int64(len(req.Paths)))
		}

		// Everything is resolved before a byte is written, because after that
		// a refusal is a truncated file rather than a status.
		roots := make([]core.Resolved, 0, len(req.Paths))
		for _, p := range req.Paths {
			vp, perr := vfs.ParseVpath(p)
			if perr != nil {
				return perr
			}
			resolved, rerr := d.Core.Resolve(uid, vp, acl.Read|acl.Download)
			if rerr != nil {
				return rerr
			}
			roots = append(roots, resolved)
		}

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", contentDisposition(archiveName(req.Name)))
		// No length, and the header says so rather than leaving a client to
		// infer it: the size is not known until the walk ends.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)

		zw := archive.NewWriter(w)
		var skipped []string
		skippedOver := 0
		note := func(line string) {
			// Bounded, because this is held in memory while the archive streams
			// and a tree of unreadable files would otherwise make the list the
			// largest thing in the request. What is past the bound is counted,
			// so the entry says how much it is not showing.
			if len(skipped) >= maxSkippedListed {
				skippedOver++
				return
			}
			skipped = append(skipped, line)
		}
		var entries int64
		var packed uint64
		truncated := false
		for _, root := range roots {
			if truncated {
				break
			}
			werr := d.Core.ArchiveWalk(r.Context(), root, func(e core.WalkEntry, s *core.Stream) error {
				if entries >= limits.ArchivePackedEntries || packed >= limits.ArchivePackedBytes {
					// The bound is reached. The walk ends here and the archive
					// is closed out carrying a marker entry, because the status
					// was committed on the first byte and a refusal is no
					// longer available.
					truncated = true
					return errArchiveTruncated
				}
				entries++
				if !e.Readable {
					// A name the caller cannot read is recorded rather than
					// failing the archive. Their own archive listing what it
					// could not include is useful to them.
					note(e.RelPath + ": unreadable")
					return nil
				}
				if e.IsDir {
					return zw.AddDir(e.RelPath, e.MTimeNs)
				}
				n, aerr := zw.AddFile(e.RelPath, e.MTimeNs, s)
				packed += n
				if aerr == nil {
					return nil
				}
				// A failed write is the response body going away, and nothing
				// after it can reach the client. A failed read is one file:
				// the entry is already closed out short and valid, so it is
				// recorded like a permission skip and the archive goes on.
				if zw.Err() != nil {
					return aerr
				}
				note(e.RelPath + ": the read failed partway, so this entry is short")
				return nil
			})
			if werr != nil && !errors.Is(werr, errArchiveTruncated) {
				// The status is long gone. The archive is closed out with what
				// it has, so the client receives a valid file that is short
				// rather than a stream that stops mid-entry.
				d.Log.Warn("an archive walk failed partway", "error", werr)
				break
			}
		}

		if len(skipped) > 0 {
			body := "These entries were not included in full:\n\n" +
				strings.Join(skipped, "\n") + "\n"
			if skippedOver > 0 {
				body += fmt.Sprintf("\nand %d more, not listed.\n", skippedOver)
			}
			if aerr := zw.AddBytes(skippedName, d.Clock.Nanos(), []byte(body)); aerr != nil {
				d.Log.Warn("the skipped list could not be written", "error", aerr)
			}
		}
		if truncated {
			body := fmt.Sprintf("This archive stopped at the server's bound: "+
				"%d entries or %d bytes, whichever came first. "+
				"It holds %d entries and %d bytes. Archive a smaller selection "+
				"to get the rest.\n",
				int64(limits.ArchivePackedEntries), int64(limits.ArchivePackedBytes), entries, packed)
			if aerr := zw.AddBytes(truncatedName, d.Clock.Nanos(), []byte(body)); aerr != nil {
				d.Log.Warn("the truncation marker could not be written", "error", aerr)
			}
		}

		if cerr := zw.Close(); cerr != nil {
			d.Log.Warn("the archive could not be closed", "error", cerr)
		}
		return nil
	})
}

// skippedName is the entry listing what the archive could not include. It is
// written only for an authenticated archive: to the account that asked, it is
// an explanation, and to an anonymous visitor it would be a list of file names
// they were specifically not allowed to see.
const skippedName = "_skipped.txt"

// truncatedName is the entry that says the archive stopped at a bound. An
// archive that is silently short is one a person restores from and only later
// finds out was incomplete.
const truncatedName = "_truncated.txt"

// maxSkippedListed bounds the skipped list, which is held in memory for the
// whole of a streaming archive. What is past it is counted rather than named.
const maxSkippedListed = 1000

// errArchiveTruncated ends the walk at the bound. It never reaches the client:
// the response is already committed, and what the client gets is the marker
// entry above.
var errArchiveTruncated = errors.New("archive: the bound was reached")

// archiveName is what the download is called.
func archiveName(requested string) string {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = "archive"
	}
	// The name goes into a header, so anything that could end the field early
	// or introduce a path is refused rather than escaped.
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', '"', '\r', '\n', 0:
			return -1
		}
		return r
	}, name)
	if !strings.HasSuffix(name, ".zip") {
		name += ".zip"
	}
	return name
}

// contentDisposition names the download in both the form every client
// understands and the form that carries a name outside ASCII.
//
// Two spellings because the plain one has no encoding: a name with a Korean or
// an accented character in it arrives mangled, and the extended spelling is
// what says the bytes are UTF-8.
func contentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r > 127 {
			return '_'
		}
		return r
	}, name)
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + percentEncode(name)
}

// percentEncode escapes for the extended header form, which takes the
// unreserved set literally and everything else as bytes.
func percentEncode(s string) string {
	const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.~"
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		c := s[i]
		if strings.IndexByte(unreserved, c) >= 0 {
			out = append(out, c)
			continue
		}
		const hex = "0123456789ABCDEF"
		out = append(out, '%', hex[c>>4], hex[c&0x0f])
	}
	return string(out)
}
