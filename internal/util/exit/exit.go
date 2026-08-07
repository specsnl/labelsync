// Package exit holds the process exit codes labelsync returns.
//
// The scheme is borrowed from `terraform plan -detailed-exitcode`: a dry run
// that finds pending work exits non-zero without that meaning "the tool broke".
// Without it a CI dry-run can only ever pass, which makes it useless as a check.
//
// The codes are a public contract — CI pipelines branch on them — so they may be
// added to, never renumbered.
package exit

const (
	// OK means the run succeeded and left nothing outstanding: either the live
	// state already matched the config, or every planned action was applied and
	// no repository was skipped.
	OK = 0

	// Error means the run failed. Config invalid, no token, an unrecoverable API
	// error, or a rate-limit wait that would exceed --max-wait. Nothing about the
	// live state can be inferred from this code.
	Error = 1

	// Drift means the run completed without writing and found pending actions:
	// `sync --dry-run` against repositories whose labels disagree with the
	// config. This is the code a pull-request check fails on.
	Drift = 2

	// Skipped means the plan was applied successfully, but one or more
	// repositories could not be reached — missing, archived, or outside the
	// token's scopes. Those failures are collected per repository rather than
	// aborting the run, so the work that could be done was done; this code is how
	// the caller learns it was not the whole set.
	Skipped = 3
)
