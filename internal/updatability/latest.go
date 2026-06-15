package updatability

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LatestResolver queries an upstream registry for a pin's newest version. It is
// an injected port so the network is testable/fakeable and so an offline run can
// degrade gracefully instead of failing.
type LatestResolver interface {
	// Latest returns the newest upstream version for a pin, or ("", false) when it
	// cannot be determined (no known upstream, or a transient/offline error).
	Latest(pin Pin) (string, bool)
}

// Row pairs a pin with its resolved latest version and a status verdict.
type Row struct {
	Pin    Pin
	Latest string
	Status string // current | outdated | unknown
}

// CheckLatest resolves the newest upstream version for every pin via r and
// returns a status row per pin. A nil resolver yields all-"unknown" rows (the
// offline / no-network path), never an error.
func CheckLatest(m Matrix, r LatestResolver) []Row {
	rows := make([]Row, 0, len(m.Pins))
	for _, p := range m.Pins {
		var (
			latest string
			ok     bool
		)
		if r != nil {
			latest, ok = r.Latest(p)
		}
		var status string
		switch {
		case !ok || latest == "":
			status = "unknown"
		case strings.TrimPrefix(latest, "v") == strings.TrimPrefix(p.Pinned, "v"):
			status = "current"
		default:
			status = "outdated"
		}
		rows = append(rows, Row{Pin: p, Latest: latest, Status: status})
	}
	return rows
}

// RenderRows formats the check-latest rows as an aligned table with the extra
// LATEST and STATUS columns.
func RenderRows(rows []Row) string {
	if len(rows) == 0 {
		return "no pins found in the declared toolMatrix sources\n"
	}
	toolW, pinW, latW, srcW := len("TOOL"), len("PINNED"), len("LATEST"), len("SOURCE")
	for _, r := range rows {
		toolW = max(toolW, len(r.Pin.Tool))
		pinW = max(pinW, len(r.Pin.Pinned))
		latW = max(latW, len(displayLatest(r.Latest)))
		srcW = max(srcW, len(r.Pin.Source))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %-8s  %s\n", toolW, "TOOL", pinW, "PINNED", latW, "LATEST", "STATUS", "SOURCE")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-*s  %-*s  %-*s  %-8s  %s\n",
			toolW, r.Pin.Tool, pinW, r.Pin.Pinned, latW, displayLatest(r.Latest), r.Status, r.Pin.Source)
	}
	return b.String()
}

func displayLatest(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// HTTPResolver is the real best-effort upstream resolver. It queries the Go
// module proxy for go.mod pins and the npm registry for package.json pins; env
// and dockerfile pins have no universal upstream and resolve to unknown. Every
// request is short-timeout so an offline run degrades quickly to "unknown".
type HTTPResolver struct {
	Client *http.Client
}

// NewHTTPResolver returns an HTTPResolver with a conservative timeout.
func NewHTTPResolver() *HTTPResolver {
	return &HTTPResolver{Client: &http.Client{Timeout: 5 * time.Second}}
}

// Latest implements LatestResolver for the real registries.
func (h *HTTPResolver) Latest(pin Pin) (string, bool) {
	switch pin.Kind {
	case "go.mod":
		mod := strings.TrimSuffix(pin.Tool, " (indirect)")
		return h.goProxyLatest(mod)
	case "package.json":
		name := strings.TrimSuffix(pin.Tool, " (dev)")
		return h.npmLatest(name)
	default:
		return "", false
	}
}

func (h *HTTPResolver) goProxyLatest(module string) (string, bool) {
	url := fmt.Sprintf("https://proxy.golang.org/%s/@latest", strings.ToLower(module))
	body, ok := h.getJSON(url)
	if !ok {
		return "", false
	}
	var v struct {
		Version string `json:"Version"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.Version == "" {
		return "", false
	}
	return v.Version, true
}

func (h *HTTPResolver) npmLatest(name string) (string, bool) {
	url := fmt.Sprintf("https://registry.npmjs.org/%s/latest", name)
	body, ok := h.getJSON(url)
	if !ok {
		return "", false
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.Version == "" {
		return "", false
	}
	return v.Version, true
}

func (h *HTTPResolver) getJSON(url string) ([]byte, bool) {
	resp, err := h.Client.Get(url) //nolint:noctx,gosec // best-effort version probe; client carries a timeout.
	if err != nil {
		return nil, false
	}
	defer func() { resp.Body.Close() }() //nolint:errcheck,gosec // response body close on a read-only probe.
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	return body, true
}
