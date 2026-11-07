// Command claude-compat-gen regenerates the Claude client profile's compatibility table
// (#153) from offline captures, in two reproducible steps:
//
//	claude-compat-gen evidence -o evidence.json capture.jsonl...   # raw captures -> sanitized evidence
//	claude-compat-gen profile -evidence evidence.json -profile profile.json  # evidence -> table
//
// The capture format and shipping policy are documented in internal/pkg/claude/compatibility_gen.go
// and docs/CLAUDE_CLIENT_PROFILE.md. The profile step keeps every identity field of the file
// it rewrites.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "claude-compat-gen:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: claude-compat-gen evidence|profile")
	}
	switch args[0] {
	case "evidence":
		fs := flag.NewFlagSet("evidence", flag.ContinueOnError)
		out := fs.String("o", "", "evidence file to write")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *out == "" || fs.NArg() == 0 {
			return fmt.Errorf("evidence needs -o and at least one capture file")
		}
		var ev claude.CompatibilityEvidence
		for _, path := range fs.Args() {
			if err := addCaptureFile(&ev, path); err != nil {
				return err
			}
		}
		b, err := claude.MarshalCompatibilityEvidence(ev.Cells())
		if err != nil {
			return err
		}
		return os.WriteFile(*out, b, 0o644) //nolint:gosec // developer tool writing the path it was given
	case "profile":
		fs := flag.NewFlagSet("profile", flag.ContinueOnError)
		evPath := fs.String("evidence", "", "sanitized evidence file")
		profilePath := fs.String("profile", "", "profile file to rewrite in place")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *evPath == "" || *profilePath == "" {
			return fmt.Errorf("profile needs -evidence and -profile")
		}
		raw, err := os.ReadFile(*evPath)
		if err != nil {
			return err
		}
		cells, err := claude.ParseCompatibilityEvidence(raw)
		if err != nil {
			return err
		}
		current, err := os.ReadFile(*profilePath)
		if err != nil {
			return err
		}
		identity, err := claude.ReadClientProfileIdentity(current)
		if err != nil {
			return err
		}
		b, err := claude.RenderClientProfile(identity, cells)
		if err != nil {
			return err
		}
		return os.WriteFile(*profilePath, b, 0o644) //nolint:gosec // developer tool writing the path it was given
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func addCaptureFile(ev *claude.CompatibilityEvidence, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for n := 1; sc.Scan(); n++ {
		if err := ev.AddCaptureLine(sc.Bytes()); err != nil {
			return fmt.Errorf("%s:%d: %w", path, n, err)
		}
	}
	return sc.Err()
}
