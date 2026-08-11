package config

import (
	_ "embed"
	"slices"
)

// scaffold is the starter config `labelsync init` writes.
//
// It lives in this package rather than in internal/cmd because it is config
// data, and because that is what lets it be held to the same rules as any other
// config file: catalogue_test.go loads it through the real entry point, next to
// the repository's own labels.yml, so the worked example cannot drift away from
// what the validator accepts.
//
// It is a file rather than a Go string literal so that it reads as YAML while
// being edited — the comments in it are most of its value, and they are what a
// user meets first.
//
//go:embed scaffold.yml
var scaffold []byte

// Scaffold returns the starter config as it should be written to disk.
//
// The copy is not defensive politeness: the embedded bytes are package state
// for the life of the process, and a caller that sliced into them could change
// what every later call returns.
func Scaffold() []byte {
	return slices.Clone(scaffold)
}
