package handler

import (
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
		for _, root := range roots {
			werr := d.Core.ArchiveWalk(r.Context(), root, func(e core.WalkEntry, s *core.Stream) error {
				if !e.Readable {
					// A name the caller cannot read is recorded rather than
					// failing the archive. Their own archive listing what it
					// could not include is useful to them.
					skipped = append(skipped, e.RelPath)
					return nil
				}
				if e.IsDir {
					return zw.AddDir(e.RelPath, e.MTimeNs)
				}
				_, aerr := zw.AddFile(e.RelPath, e.MTimeNs, s)
				return aerr
			})
			if werr != nil {
				// The status is long gone. The archive is closed out with what
				// it has, so the client receives a valid file that is short
				// rather than a stream that stops mid-entry.
				d.Log.Warn("an archive walk failed partway", "error", werr)
				break
			}
		}

		if len(skipped) > 0 {
			note := "These entries were not included, because this account " +
				"cannot read them:\n\n" + strings.Join(skipped, "\n") + "\n"
			if aerr := zw.AddBytes(skippedName, d.Clock.Nanos(), []byte(note)); aerr != nil {
				d.Log.Warn("the skipped list could not be written", "error", aerr)
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
