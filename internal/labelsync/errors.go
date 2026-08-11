// Package labelsync holds the values every other labelsync package depends on:
// the sentinel errors that describe how a run can fail, and the XDG paths and
// file names that describe where its files live. It has no behaviour of its own
// and imports nothing from the rest of the tree, so any package may import it.
//
// # Wrapping rule
//
// Sentinels are never returned bare from a call site that has context to add.
// Always wrap with %w so the caller keeps both a readable message and a
// machine-comparable identity:
//
//	return fmt.Errorf("%w: %s", labelsync.ErrInvalidColor, raw)
//
// Callers match with errors.Is; the JSON output layer calls KindOf to render the
// stable error_kind field. Returning a sentinel through %v or a freshly
// constructed error breaks both.
package labelsync

import "errors"

// Sentinel errors for every way a labelsync run can fail. Each one maps to a
// stable kind string in KindOf — see the error table in docs/design.md.
var (
	// ErrConfigNotFound is returned when no config file is found at the --config
	// path, in the working directory, or under the XDG config directory.
	ErrConfigNotFound = errors.New("no config file found")

	// ErrAmbiguousConfigFile is returned when both labels.yml and labels.yaml
	// exist in the same directory. Only one is allowed.
	ErrAmbiguousConfigFile = errors.New("ambiguous config file: both labels.yml and labels.yaml exist — remove one")

	// ErrConfigExists is returned when `labelsync init` is asked to scaffold a
	// config file over one that is already there. Overwriting a hand-edited
	// catalogue is not a thing to do by accident, so it takes --force.
	ErrConfigExists = errors.New("config file already exists")

	// ErrUnsupportedConfigVersion is returned when the config's version field is
	// missing, or names a schema version this binary does not understand.
	ErrUnsupportedConfigVersion = errors.New("unsupported config version")

	// ErrEmptyConfig is returned when the config parses but declares no labels,
	// leaving nothing to reconcile.
	ErrEmptyConfig = errors.New("config declares no labels")

	// ErrDuplicateLabelName is returned when two label entries share a name.
	// Comparison is case-insensitive, because GitHub treats label names that way.
	ErrDuplicateLabelName = errors.New("duplicate label name")

	// ErrDuplicateLabelColor is returned when two label entries share a colour.
	// Distinct labels need distinct colours for the diff to stay readable.
	ErrDuplicateLabelColor = errors.New("duplicate label colour")

	// ErrInvalidColor is returned when a colour is not a 6-digit hex value, with
	// or without a leading #.
	ErrInvalidColor = errors.New("invalid colour: want a 6-digit hex value")

	// ErrInvalidLabelName is returned when a label name is empty, consists only
	// of emoji, or is longer than the 50 code points GitHub accepts.
	ErrInvalidLabelName = errors.New("invalid label name")

	// ErrDescriptionTooLong is returned when a label description exceeds the 100
	// code points GitHub accepts.
	ErrDescriptionTooLong = errors.New("label description is too long")

	// ErrUnknownGroup is returned when a label, or defaults.groups, references a
	// group name that the groups section does not define.
	ErrUnknownGroup = errors.New("unknown group")

	// ErrAmbiguousGroupSource is returned when a group mixes sources. Exactly one
	// of org, user, repos, or include_groups must be set.
	ErrAmbiguousGroupSource = errors.New("ambiguous group source: set exactly one of org, user, repos, or include_groups")

	// ErrCyclicGroup is returned when include_groups forms a cycle, so the group
	// cannot be resolved to a repository set.
	ErrCyclicGroup = errors.New("cyclic group composition")

	// ErrInvalidRepoRef is returned when a repository reference is not in
	// owner/repo form.
	ErrInvalidRepoRef = errors.New("invalid repository reference: want owner/repo")

	// ErrInvalidRename is returned when a rename entry is malformed: an empty
	// from or to, a rename to a name no label declares, or two renames targeting
	// the same name.
	ErrInvalidRename = errors.New("invalid rename")

	// ErrNoToken is returned when the token resolution chain finds no GitHub
	// credential to authenticate with.
	ErrNoToken = errors.New("no GitHub token found")

	// ErrInteractiveRequired is returned when an operation needs a prompt but
	// stdin is not a TTY — prune without --prune=all in CI, for example.
	ErrInteractiveRequired = errors.New("operation requires an interactive terminal")

	// ErrRepoInaccessible is returned when a repository cannot be reached with
	// the current token: missing, archived, or outside the token's scopes. It is
	// reported per repository and does not abort the run.
	ErrRepoInaccessible = errors.New("repository is inaccessible")

	// ErrUnsafeCacheDir is returned when a cache command is pointed at a
	// directory outside the XDG cache home. The path comes from the
	// environment and the command then deletes what is in it, so the bound is
	// explicit rather than assumed.
	ErrUnsafeCacheDir = errors.New("refusing to touch a cache directory outside the cache home")

	// ErrMaxWaitExceeded is returned when a rate-limit backoff would sleep for
	// longer than the --max-wait ceiling allows.
	ErrMaxWaitExceeded = errors.New("rate limit wait exceeds --max-wait")
)

// KindOf returns a stable, machine-readable string identifier for the sentinel
// wrapped in err, or "" when err wraps no known sentinel.
// The returned strings are a public contract: they are embedded in JSON output
// as the error_kind field, so they may be added to but never renamed.
func KindOf(err error) string {
	switch {
	case errors.Is(err, ErrConfigNotFound):
		return "config_not_found"
	case errors.Is(err, ErrAmbiguousConfigFile):
		return "ambiguous_config_file"
	case errors.Is(err, ErrConfigExists):
		return "config_exists"
	case errors.Is(err, ErrUnsupportedConfigVersion):
		return "unsupported_config_version"
	case errors.Is(err, ErrEmptyConfig):
		return "empty_config"
	case errors.Is(err, ErrDuplicateLabelName):
		return "duplicate_label_name"
	case errors.Is(err, ErrDuplicateLabelColor):
		return "duplicate_label_color"
	case errors.Is(err, ErrInvalidColor):
		return "invalid_color"
	case errors.Is(err, ErrInvalidLabelName):
		return "invalid_label_name"
	case errors.Is(err, ErrDescriptionTooLong):
		return "description_too_long"
	case errors.Is(err, ErrUnknownGroup):
		return "unknown_group"
	case errors.Is(err, ErrAmbiguousGroupSource):
		return "ambiguous_group_source"
	case errors.Is(err, ErrCyclicGroup):
		return "cyclic_group"
	case errors.Is(err, ErrInvalidRepoRef):
		return "invalid_repo_ref"
	case errors.Is(err, ErrInvalidRename):
		return "invalid_rename"
	case errors.Is(err, ErrNoToken):
		return "no_token"
	case errors.Is(err, ErrInteractiveRequired):
		return "interactive_required"
	case errors.Is(err, ErrRepoInaccessible):
		return "repo_inaccessible"
	case errors.Is(err, ErrUnsafeCacheDir):
		return "unsafe_cache_dir"
	case errors.Is(err, ErrMaxWaitExceeded):
		return "max_wait_exceeded"
	default:
		return ""
	}
}
