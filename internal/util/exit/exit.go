// Package exit holds the process exit codes labelsync returns, and the error
// type that carries one out of a command.
//
// The scheme is borrowed from `terraform plan -detailed-exitcode`: a dry run
// that finds pending work exits non-zero without that meaning "the tool broke".
// Without it a CI dry-run can only ever pass, which makes it useless as a check.
//
// # Outcomes combine; failure does not
//
// A single run can satisfy more than one outcome — a dry run that finds pending
// actions and also cannot reach a repository is both [Drift] and [Skipped]. The
// outcome codes are therefore disjoint bits that OR together, and that run exits
// 6. Ranking them would mean throwing half the answer away.
//
// [Error] is deliberately not part of that bit space. It is the classic Unix
// generic failure and it is exclusive: when a run fails, the live state is
// unknown, so "failed and drifted" is not a statement labelsync can honestly
// make. A failure exits 1 and nothing else — which is also what keeps every
// combination meaningful, since with Error in the mask 3 would have to mean
// "failed and drifted", the very claim the failure invalidates.
//
// The numbers are a public contract — CI pipelines branch on them — so bits may
// be added, never reassigned, and Error stays 1 forever.
package exit

import (
	"errors"
	"strconv"
)

// Code is a process exit code. The outcome codes are single bits and combine
// with |; see the package doc.
type Code int

const (
	// OK means the run succeeded and left nothing outstanding: either the live
	// state already matched the config, or every planned action was applied and
	// no repository was skipped.
	OK Code = 0

	// Error means the run failed. Config invalid, no token, an unrecoverable API
	// error, or a rate-limit wait that would exceed --max-wait. Nothing about the
	// live state can be inferred from this code, which is why it is exclusive
	// rather than a bit combined with the outcomes below.
	Error Code = 1

	// Drift means the run completed without writing and found pending actions:
	// `sync --dry-run` against repositories whose labels disagree with the
	// config. This is the code a pull-request check fails on.
	Drift Code = 1 << 1

	// Skipped means the plan was applied successfully, but one or more
	// repositories could not be reached — missing, archived, or outside the
	// token's scopes. Those failures are collected per repository rather than
	// aborting the run, so the work that could be done was done; this code is how
	// the caller learns it was not the whole set.
	Skipped Code = 1 << 2
)

// Err carries a code out of a command, because RunE returns an error and
// nothing else.
//
// A nil Err field means silent: the command has already reported everything the
// user needs through the output.Writer, and there is no failure to print. A dry
// run that found drift is not an error — the drift was the successful result,
// and the diff is already on stdout — but it still has to exit 2.
//
// An ordinary failure needs no carrier at all: [Of] maps every other error to
// [Error].
type Err struct {
	// Code is the code the process should exit with. Combine outcome bits with |.
	Code Code

	// Err is the underlying failure, or nil when the non-zero code reports an
	// outcome rather than a failure.
	Err error
}

// Error implements error. The message is only ever read by a caller that
// formats the carrier itself — main prints the wrapped error, or nothing.
func (e *Err) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}

	return "exit code " + e.Code.String()
}

// Unwrap exposes the underlying failure to errors.Is and errors.As, so putting
// an error in a carrier does not hide its sentinel from labelsync.KindOf.
func (e *Err) Unwrap() error { return e.Err }

// Of returns the code the process should exit with: OK for a nil error, the
// carried code for an [Err], and Error for anything else.
//
// A carrier holding a real failure never reports success, even when it was
// built with Code left unset — a zero exit on a failed run is the one mistake
// here that a pipeline cannot detect.
func Of(err error) Code {
	if err == nil {
		return OK
	}

	var carrier *Err
	if errors.As(err, &carrier) && carrier.Code != OK {
		return carrier.Code
	}

	return Error
}

// String renders a code for a debug log or a test failure. A combined code
// renders as its sum, which is what the shell sees.
func (c Code) String() string { return strconv.Itoa(int(c)) }
