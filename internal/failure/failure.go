// Package failure is cictl's error-wrapping boundary. A standalone tool module
// cannot import the workspace-local Eden errors library (a v0.0.0 sibling), so
// this package provides the same discipline in miniature: every error that
// crosses a package boundary is wrapped here with context, preserving the cause
// chain via %w. Routing wraps through one named helper (Wrap/Wrapf) keeps the
// wrapping boundary in a single, reviewable home — the same property the errors
// contract (rule 12) and the shared wrapcheck gate require.
package failure

import "fmt"

// Wrap annotates err with a static message, preserving the cause chain. It
// returns nil when err is nil so call sites can wrap unconditionally.
func Wrap(err error, message string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err) //nolint:wrapcheck // this IS the wrapping boundary.
}

// Wrapf annotates err with a formatted message, preserving the cause chain. The
// format/args describe what the failing layer was doing; the cause stays
// reachable via errors.Is/As.
func Wrapf(err error, format string, a ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf(format+": %w", append(a, err)...) //nolint:wrapcheck,err113 // this IS the wrapping boundary; the dynamic format is the caller's context.
}
