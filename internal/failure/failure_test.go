package failure_test

import (
	"errors"
	"testing"

	"github.com/gophersys/eden/tools/cictl/internal/failure"
)

var errSentinel = errors.New("root cause")

func TestWrap_NilPassthrough(t *testing.T) {
	t.Parallel()
	if failure.Wrap(nil, "ctx") != nil {
		t.Fatal("Wrap(nil) must be nil")
	}
	if failure.Wrapf(nil, "ctx %d", 1) != nil {
		t.Fatal("Wrapf(nil) must be nil")
	}
}

func TestWrap_PreservesChain(t *testing.T) {
	t.Parallel()
	wrapped := failure.Wrap(errSentinel, "while reading")
	if !errors.Is(wrapped, errSentinel) {
		t.Fatalf("Wrap lost the cause chain: %v", wrapped)
	}
	if got := wrapped.Error(); got != "while reading: root cause" {
		t.Fatalf("Wrap message = %q", got)
	}
}

func TestWrapf_PreservesChainAndFormats(t *testing.T) {
	t.Parallel()
	wrapped := failure.Wrapf(errSentinel, "reading %s", "/etc/x")
	if !errors.Is(wrapped, errSentinel) {
		t.Fatalf("Wrapf lost the cause chain: %v", wrapped)
	}
	if got := wrapped.Error(); got != "reading /etc/x: root cause" {
		t.Fatalf("Wrapf message = %q", got)
	}
}
