package main

import (
	"fmt"
	"io"
	"strings"
)

// Report is what the run produced.
type Report struct {
	Compared    int
	Differences []Difference
	Errors      []RequestError
}

// Write renders the report.
//
// It names every difference rather than counting them: a count tells whoever
// reads it that something changed and not what, and the next step is always to
// go and look.
func (r *Report) Write(w io.Writer) error {
	var b []byte

	b = fmt.Appendf(b, "compared %d requests\n", r.Compared)

	if len(r.Errors) > 0 {
		b = append(b, "\nrequests one side could not answer:\n"...)
		for _, e := range r.Errors {
			b = fmt.Appendf(b, "  [%s] %s\n", e.Group, e.Name)
			if e.Old != "" {
				b = fmt.Appendf(b, "      reference: %s\n", e.Old)
			}
			if e.New != "" {
				b = fmt.Appendf(b, "      under test: %s\n", e.New)
			}
		}
	}

	if len(r.Differences) == 0 {
		b = append(b, "\nno difference outside the allow list\n"...)
		_, err := w.Write(b)
		return err
	}

	b = fmt.Appendf(b, "\n%d requests differ:\n", len(r.Differences))
	for _, d := range r.Differences {
		b = fmt.Appendf(b, "\n  [%s] %s\n    %s %s\n", d.Group, d.Name, d.Method, d.Path)
		for _, f := range d.Fields {
			b = fmt.Appendf(b, "      %s\n", f.Field)
			b = fmt.Appendf(b, "        reference:  %s\n", indent(f.Old))
			b = fmt.Appendf(b, "        under test: %s\n", indent(f.New))
		}
	}

	// The allow list, printed with the report, so whoever reads a difference
	// can see what was not compared without going to find the source.
	b = append(b, "\nfields allowed to differ:\n"...)
	for name, why := range allowedHeaders() {
		b = fmt.Appendf(b, "  %-18s %s\n", name, why)
	}
	b = append(b, "  etag               only the advisory marker, never the token\n"...)

	_, err := w.Write(b)
	return err
}

func indent(s string) string {
	return strings.ReplaceAll(s, "\n", "\n                    ")
}
