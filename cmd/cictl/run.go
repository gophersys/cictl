package main

import (
	"flag"
	"io"
	"os"

	"github.com/gophersys/cictl/internal/affected"
	"github.com/gophersys/cictl/internal/cirepo"
	"github.com/gophersys/cictl/internal/conformance"
	"github.com/gophersys/cictl/internal/schema"
	"github.com/gophersys/cictl/internal/updatability"
	"github.com/gophersys/cictl/internal/validation"
)

// run is the testable entry point: it returns the process exit code, writing
// normal output to rawOut and diagnostics to rawErr. Exit codes: 0 success, 1 a
// gate failure (invalid contract, drift, non-conformance), 2 a usage error.
func run(args []string, rawOut, rawErr io.Writer) int {
	out := printer{rawOut}
	errOut := printer{rawErr}
	if len(args) == 0 {
		usage(errOut)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "schema":
		return cmdSchema(rest, out, errOut)
	case "validate":
		return cmdValidate(rest, out, errOut)
	case "conformance":
		return cmdConformance(rest, out, errOut)
	case "affected":
		return cmdAffected(rest, out, errOut)
	case "generate":
		return cmdGenerate(rest, out, errOut)
	case "drift":
		return cmdDrift(rest, out, errOut)
	case "updatability":
		return cmdUpdatability(rest, out, errOut)
	case "help", "-h", "--help":
		usage(out)
		return 0
	default:
		errOut.printf("cictl: unknown command %q\n", cmd)
		usage(errOut)
		return 2
	}
}

func usage(w printer) {
	w.print(`cictl — the Eden CI contract tool

Usage:
  cictl schema       [-o FILE]                  Emit the JSON Schema (draft 2020-12) from the contract structs
  cictl validate     [-f .ci/ci.contract.yaml]  Validate a contract instance (structural + semantic)
  cictl conformance  [-C DIR]                    Assert a repo's .ci/ is canonical
  cictl affected     [--base origin/main]        List changed project roots (one per line)
  cictl generate     [-C DIR]                    Render the provider workflows from the contract
  cictl drift        [-C DIR]                    Fail if the committed workflows drift from the contract
  cictl updatability [-C DIR] [--check-latest]   Report the pinned-version matrix
  cictl help                                     Show this message
`)
}

func cmdSchema(args []string, out, errOut printer) int {
	fs := flag.NewFlagSet("schema", flag.ContinueOnError)
	fs.SetOutput(errOut.w)
	outFile := fs.String("o", "ci-contract.schema.json", "output file (\"-\" for stdout)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	doc, err := schema.Emit()
	if err != nil {
		errOut.printf("cictl schema: %v\n", err)
		return 1
	}
	if *outFile == "-" {
		out.write(doc)
		return 0
	}
	if err := os.WriteFile(*outFile, doc, 0o600); err != nil {
		errOut.printf("cictl schema: write %s: %v\n", *outFile, err)
		return 1
	}
	out.printf("wrote %s (%d bytes)\n", *outFile, len(doc))
	return 0
}

func cmdValidate(args []string, out, errOut printer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(errOut.w)
	file := fs.String("f", ".ci/ci.contract.yaml", "contract instance to validate")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	raw, err := os.ReadFile(*file) //nolint:gosec // operator-chosen contract path.
	if err != nil {
		errOut.printf("cictl validate: %v\n", err)
		return 1
	}
	res := validation.ValidateBytes(raw)
	if !res.OK() {
		errOut.printf("cictl validate: %s is INVALID\n", *file)
		errOut.printf("%s\n", res.Error())
		return 1
	}
	out.printf("%s is valid\n", *file)
	return 0
}

func cmdConformance(args []string, out, errOut printer) int {
	dir, code := repoFlag("conformance", args, errOut)
	if code >= 0 {
		return code
	}
	rep := conformance.Check(dir)
	if !rep.OK() {
		errOut.printf("cictl conformance: %s is NOT canonical\n", dir)
		errOut.printf("%s\n", rep.Error())
		return 1
	}
	out.printf("%s/.ci is canonical\n", dir)
	return 0
}

func cmdAffected(args []string, out, errOut printer) int {
	fs := flag.NewFlagSet("affected", flag.ContinueOnError)
	fs.SetOutput(errOut.w)
	base := fs.String("base", "origin/main", "diff base for affected detection")
	dir := fs.String("C", ".", "repository directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	roots, err := affected.Projects(*dir, *base)
	if err != nil {
		errOut.printf("cictl affected: %v\n", err)
		return 1
	}
	for _, r := range roots {
		out.printf("%s\n", r)
	}
	return 0
}

func cmdGenerate(args []string, out, errOut printer) int {
	dir, code := repoFlag("generate", args, errOut)
	if code >= 0 {
		return code
	}
	layout := cirepo.New(dir)
	c, err := layout.LoadContract()
	if err != nil {
		errOut.printf("cictl generate: %v\n", err)
		return 1
	}
	// Refuse to generate from an invalid contract — a bad contract must not produce
	// workflows that look authoritative.
	if res := validation.Validate(c, nil); !res.OK() {
		errOut.printf("cictl generate: contract is INVALID, refusing to render\n")
		errOut.printf("%s\n", res.Error())
		return 1
	}
	written, err := layout.Write(c)
	if err != nil {
		errOut.printf("cictl generate: %v\n", err)
		return 1
	}
	for _, w := range written {
		out.printf("wrote %s\n", w)
	}
	return 0
}

func cmdDrift(args []string, out, errOut printer) int {
	dir, code := repoFlag("drift", args, errOut)
	if code >= 0 {
		return code
	}
	layout := cirepo.New(dir)
	c, err := layout.LoadContract()
	if err != nil {
		errOut.printf("cictl drift: %v\n", err)
		return 1
	}
	divs, err := layout.Drift(c)
	if err != nil {
		errOut.printf("cictl drift: %v\n", err)
		return 1
	}
	if len(divs) > 0 {
		errOut.printf("cictl drift: %d generated file(s) drift from the contract (workflows are generated — re-run `cictl generate`):\n", len(divs))
		for _, d := range divs {
			errOut.printf("  %s: %s\n", d.Path, d.Reason)
			if d.Diff != "" {
				for _, line := range splitLines(d.Diff) {
					errOut.printf("    %s\n", line)
				}
			}
		}
		return 1
	}
	out.printf("no drift: committed workflows match the contract\n")
	return 0
}

func cmdUpdatability(args []string, out, errOut printer) int {
	fs := flag.NewFlagSet("updatability", flag.ContinueOnError)
	fs.SetOutput(errOut.w)
	dir := fs.String("C", ".", "repository directory")
	checkLatest := fs.Bool("check-latest", false, "query upstream for the newest version (best-effort, network)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	layout := cirepo.New(*dir)
	c, err := layout.LoadContract()
	if err != nil {
		errOut.printf("cictl updatability: %v\n", err)
		return 1
	}
	matrix, err := updatability.Collect(*dir, c.ToolMatrix.Sources)
	if err != nil {
		errOut.printf("cictl updatability: %v\n", err)
		return 1
	}
	if *checkLatest {
		rows := updatability.CheckLatest(matrix, updatability.NewHTTPResolver())
		out.print(updatability.RenderRows(rows))
		return 0
	}
	out.print(matrix.Render())
	return 0
}

// repoFlag parses the common "-C DIR" flag for the repo-scoped verbs. It returns
// the directory and a code: a non-negative code means "return this exit code
// now" (a parse error → 2); -1 means "proceed with dir".
func repoFlag(name string, args []string, errOut printer) (dir string, code int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(errOut.w)
	d := fs.String("C", ".", "repository directory")
	if err := fs.Parse(args); err != nil {
		return "", 2
	}
	return *d, -1
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
