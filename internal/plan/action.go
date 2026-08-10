// Package plan turns "these labels are configured" and "these labels exist"
// into an ordered list of changes. It is pure: nothing here touches the
// network, and the package never imports internal/github.
//
// action.go holds the vocabulary — [Kind], [Action], and [Plan]. All three are
// plain serialisable structs with no behaviour and no client reference, which is
// what keeps a future `plan -o file` / `apply file` split a thin serialisation
// shell rather than a restructuring exercise: writing a plan out is
// json.Marshal, and reading one back is json.Unmarshal.
package plan

// Kind is what an action does to a label.
type Kind string

// The four kinds of action. KindNoOp is emitted for a configured label that
// already matches: it is never sent to the API, and exists so reporting can
// show a label as checked rather than silently omitting it.
const (
	KindCreate Kind = "create"
	KindUpdate Kind = "update"
	KindDelete Kind = "delete"
	KindNoOp   Kind = "noop"
)

// Action is one change to one label in one repository.
//
// # Why the optional fields are pointers
//
// An update carries only the fields it changes, and a nil field means
// "unchanged". That distinction cannot be carried by plain strings, because the
// design makes descriptions authoritative: an omitted description in the config
// means "clear it", so an empty description is a value an update legitimately
// sets. With a plain string, "leave the description alone" and "set the
// description to empty" are the same zero value, and clearing a description
// would be indistinguishable from not touching one.
//
// The pointers survive a round trip for the same reason: omitempty drops a nil
// field, and a *string pointing at "" marshals as an explicit "" rather than
// disappearing.
//
// # Why Repo is on the action
//
// [Plan] already groups actions by repository, so Repo is redundant inside a
// plan. It is here anyway so that a single action is self-describing — a log
// line, an NDJSON record, or an error about one failed write carries its
// repository without its surrounding group having to be threaded along.
type Action struct {
	Kind Kind   `json:"kind"`
	Repo string `json:"repo"` // owner/repo

	// Name is the label's current name, and the lookup key against the
	// repository's existing labels. A rename changes the name the API sees;
	// Name stays the name to find it under.
	Name string `json:"name"`

	// NewName is set only by a rename, which is a PATCH so that the label's
	// issue and PR associations survive.
	NewName *string `json:"new_name,omitempty"`

	// Color is bare six-digit lowercase hex, no leading #, as GitHub stores it.
	Color *string `json:"color,omitempty"`

	// Description is authoritative when set. A pointer to the empty string
	// clears the label's description; nil leaves it alone.
	Description *string `json:"description,omitempty"`

	// Reason carries reporting context — for example `displaced by "type: bug"`
	// on a recoloured squatter, or the palette's exhaustion warning. A recolour
	// that looks arbitrary in a diff becomes obvious when annotated with what
	// displaced it. It never affects what is sent to the API.
	Reason string `json:"reason,omitempty"`
}

// Plan is a whole run's worth of actions, grouped per repository.
//
// Repos is a slice rather than a map keyed by repository: ordering is part of
// what a plan is. Actions within a repository are emitted in the order they
// have to be applied, and the repositories themselves are reported in a stable
// order, so that two runs over the same input render identically.
type Plan struct {
	Repos []RepoPlan `json:"repos"`
}

// RepoPlan is one repository's actions, in the order they must be applied:
// renames, then squatter recolours, then creates, then updates, then deletes.
type RepoPlan struct {
	Repo    string   `json:"repo"` // owner/repo
	Actions []Action `json:"actions"`

	// IssuesDisabled reports that the repository has issues turned off. It is a
	// **note**, not a warning and not a skip: it changes no action, no exit
	// code, and nothing that is sent to the API. Label endpoints are ungated on
	// the flag, so the repository syncs normally and its labels are used by pull
	// requests.
	//
	// It sits on the repository rather than being a synthetic action because it
	// is not something to apply. omitempty keeps it out of the stream for the
	// ordinary case, and out of the goldens of every test that predates it.
	//
	// False also covers "not known": an explicit repos entry is never
	// enumerated, and a note about a repository nothing looked at would be worse
	// than no note at all.
	IssuesDisabled bool `json:"issues_disabled,omitempty"`
}
