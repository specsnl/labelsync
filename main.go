// Command labelsync synchronises GitHub issue/PR labels across a configured set
// of repositories, using a local YAML file as the source of truth.
package main

import (
	"errors"
	"os"

	"github.com/specsnl/labelsync/internal/cmd"
	"github.com/specsnl/labelsync/internal/util/exit"
	"github.com/specsnl/labelsync/internal/util/output"
)

// main is the only place in labelsync that calls os.Exit. It skips deferred
// cleanup, so called from inside a command it would leak temp files, unreleased
// locks, and unflushed writers. Commands return an error; the code is derived
// from it here.
func main() {
	app := cmd.NewApp()

	err := cmd.Execute(app)

	report(app.Out, err)

	os.Exit(int(exit.Of(err)))
}

// report prints err, unless there is nothing to print.
//
// A carrier with a nil Err field is silent: exit code 2 on a drifting dry run
// reports an outcome rather than a failure, and the diff already went to stdout.
// An error line under it would be claiming the run broke.
//
// Printing goes through the Writer, never fmt.Fprintln(os.Stderr, err), because
// the Writer is what puts error_kind on the final line of a JSON run.
//
// It is a function rather than four lines inline so that a test can drive it;
// the os.Exit above is the part that cannot be.
func report(w output.Writer, err error) {
	if err == nil {
		return
	}

	var carrier *exit.Err
	if errors.As(err, &carrier) && carrier.Err == nil {
		return
	}

	w.WriteErr(err)
}
