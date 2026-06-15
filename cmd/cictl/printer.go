package main

import (
	"fmt"
	"io"
)

// printer is a thin io.Writer wrapper for CLI output. A failed write to the
// process's stdout/stderr is unactionable (there is nowhere left to report it),
// so each method deliberately discards the write error — the one place in the
// program where dropping an error is correct, made explicit here rather than
// scattered as per-call suppressions.
type printer struct {
	w io.Writer
}

func (p printer) printf(format string, a ...any) {
	fmt.Fprintf(p.w, format, a...) //nolint:errcheck,gosec // CLI stdout/stderr write failure is unactionable.
}

func (p printer) print(s string) {
	io.WriteString(p.w, s) //nolint:errcheck,gosec // CLI stdout/stderr write failure is unactionable.
}

func (p printer) write(b []byte) {
	p.w.Write(b) //nolint:errcheck,gosec // CLI stdout/stderr write failure is unactionable.
}
