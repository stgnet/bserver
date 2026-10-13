package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain runs the tests from the repository root instead of src/.
//
// `go test` always starts in the package directory (src/), but the tests
// read the real site content under www/ via os.Getwd()+"www", and the server
// itself resolves www/ and cert-cache/ relative to its working directory
// (the repo root, as set by scripts/install-service.sh). Changing to the parent
// directory once here makes the tests see the same layout the running
// server does, without having to rewrite every path in every test.
func TestMain(m *testing.M) {
	if err := os.Chdir(".."); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: cannot chdir to repo root:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
