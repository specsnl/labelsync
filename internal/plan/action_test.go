package plan

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestKinds pins the wire values of the four kinds. They are the "kind" field of
// every emitted action and of any plan file written out, so a rename of a
// constant must not quietly become a rename of the string.
func TestKinds(t *testing.T) {
	tests := []struct {
		kind Kind
		want string
	}{
		{KindCreate, "create"},
		{KindUpdate, "update"},
		{KindDelete, "delete"},
		{KindNoOp, "noop"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.kind) != tt.want {
				t.Errorf("kind = %q, want %q", string(tt.kind), tt.want)
			}
		})
	}
}

// TestActionRoundTrip is the property the type exists to have: an Action
// survives being written out and read back unchanged, so that a future
// `plan -o file` / `apply file` split is json.Marshal and json.Unmarshal and
// nothing else.
//
// The optional fields are built with new(expr), which takes the address of a
// value rather than of a zero — so new("") is a pointer to the empty string,
// which is the "set to empty" these fields exist to tell apart from nil.
func TestActionRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		action Action
	}{
		{
			name:   "create",
			action: Action{Kind: KindCreate, Repo: "specsnl/labelsync", Name: "type: bug", Color: new("d73a4a"), Description: new("Something is broken")},
		},
		{
			name:   "update colour only",
			action: Action{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "bug", Color: new("1d76db")},
		},
		{
			name:   "update description only",
			action: Action{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "bug", Description: new("A defect")},
		},
		{
			name:   "rename",
			action: Action{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "bug", NewName: new("type: bug")},
		},
		{
			name:   "rename and recolour",
			action: Action{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "bug", NewName: new("type: bug"), Color: new("d73a4a")},
		},
		{
			name:   "displaced squatter carries its reason",
			action: Action{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "wontfix", Color: new("00a3a3"), Reason: `displaced by "type: bug"`},
		},
		{
			name:   "delete",
			action: Action{Kind: KindDelete, Repo: "specsnl/labelsync", Name: "duplicate"},
		},
		{
			name:   "noop",
			action: Action{Kind: KindNoOp, Repo: "specsnl/labelsync", Name: "type: bug"},
		},
		{
			name:   "cleared description",
			action: Action{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "bug", Description: new("")},
		},
		{
			name:   "zero value",
			action: Action{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.action)
			if err != nil {
				t.Fatalf("json.Marshal() = %v, want no error", err)
			}

			var got Action

			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("json.Unmarshal(%s) = %v, want no error", data, err)
			}

			// DeepEqual follows pointers, so this compares the pointed-to
			// strings and, separately, nil against non-nil — which is the
			// distinction the pointer fields are there to carry.
			if !reflect.DeepEqual(got, tt.action) {
				t.Errorf("round trip = %+v, want %+v (via %s)", got, tt.action, data)
			}
		})
	}
}

// TestActionMarshalOmitempty checks the wire form itself rather than the round
// trip: which keys an action puts on the wire is what a consumer of the JSON
// output sees, and "absent" is how a plan says "leave this field alone".
func TestActionMarshalOmitempty(t *testing.T) {
	tests := []struct {
		name   string
		action Action
		want   string
	}{
		{
			name:   "only the required fields are always present",
			action: Action{Kind: KindDelete, Repo: "specsnl/labelsync", Name: "duplicate"},
			want:   `{"kind":"delete","repo":"specsnl/labelsync","name":"duplicate"}`,
		},
		{
			name:   "an empty kind, repo, and name still appear",
			action: Action{},
			want:   `{"kind":"","repo":"","name":""}`,
		},
		{
			name:   "every optional field set",
			action: Action{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "bug", NewName: new("type: bug"), Color: new("d73a4a"), Description: new("Something is broken"), Reason: "casing drift"},
			want:   `{"kind":"update","repo":"specsnl/labelsync","name":"bug","new_name":"type: bug","color":"d73a4a","description":"Something is broken","reason":"casing drift"}`,
		},
		{
			name:   "an empty description is set, not omitted",
			action: Action{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "bug", Description: new("")},
			want:   `{"kind":"update","repo":"specsnl/labelsync","name":"bug","description":""}`,
		},
		{
			name:   "an empty new name and colour are set the same way",
			action: Action{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "bug", NewName: new(""), Color: new("")},
			want:   `{"kind":"update","repo":"specsnl/labelsync","name":"bug","new_name":"","color":""}`,
		},
		{
			name:   "an empty reason is omitted, because a plain string cannot say otherwise",
			action: Action{Kind: KindNoOp, Repo: "specsnl/labelsync", Name: "bug", Reason: ""},
			want:   `{"kind":"noop","repo":"specsnl/labelsync","name":"bug"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.action)
			if err != nil {
				t.Fatalf("json.Marshal() = %v, want no error", err)
			}

			if string(data) != tt.want {
				t.Errorf("json.Marshal() = %s, want %s", data, tt.want)
			}
		})
	}
}

// TestActionUnmarshalDistinguishesUnsetFromEmpty is the other half of the
// pointer contract: an absent key has to come back nil and an explicit "" has to
// come back as a pointer to "". Without both halves, clearing a description
// would be indistinguishable from not touching one — and the design makes
// descriptions authoritative, so clearing is a thing a plan does.
func TestActionUnmarshalDistinguishesUnsetFromEmpty(t *testing.T) {
	tests := []struct {
		name string
		data string
		want *string
	}{
		{name: "absent", data: `{"kind":"update","repo":"o/r","name":"bug"}`, want: nil},
		{name: "explicitly empty", data: `{"kind":"update","repo":"o/r","name":"bug","description":""}`, want: new("")},
		{name: "set", data: `{"kind":"update","repo":"o/r","name":"bug","description":"A defect"}`, want: new("A defect")},
		{name: "explicitly null", data: `{"kind":"update","repo":"o/r","name":"bug","description":null}`, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Action

			if err := json.Unmarshal([]byte(tt.data), &got); err != nil {
				t.Fatalf("json.Unmarshal(%s) = %v, want no error", tt.data, err)
			}

			if !reflect.DeepEqual(got.Description, tt.want) {
				t.Errorf("Description = %s, want %s", describe(got.Description), describe(tt.want))
			}
		})
	}
}

// TestPlanRoundTrip covers the grouping: a plan holds actions per repository,
// and both the repository order and the action order within a repository are
// part of what a plan is — they are the order the actions have to be applied in.
func TestPlanRoundTrip(t *testing.T) {
	want := Plan{
		Repos: []RepoPlan{
			{
				Repo: "specsnl/labelsync",
				Actions: []Action{
					{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "bug", NewName: new("type: bug")},
					{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "wontfix", Color: new("00a3a3"), Reason: `displaced by "type: bug"`},
					{Kind: KindCreate, Repo: "specsnl/labelsync", Name: "type: chore", Color: new("cfd3d7"), Description: new("Housekeeping")},
					{Kind: KindUpdate, Repo: "specsnl/labelsync", Name: "type: bug", Description: new("")},
					{Kind: KindDelete, Repo: "specsnl/labelsync", Name: "duplicate"},
				},
			},
			{
				Repo:    "specsnl/specs-cli",
				Actions: []Action{{Kind: KindNoOp, Repo: "specsnl/specs-cli", Name: "type: bug"}},
			},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() = %v, want no error", err)
	}

	var got Plan

	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(%s) = %v, want no error", data, err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v (via %s)", got, want, data)
	}

	// Marshalling the same plan twice has to produce the same bytes: a plan file
	// is diffed and committed, so a stable rendering is the point of Repos being
	// a slice rather than a map.
	again, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() = %v, want no error", err)
	}

	if string(again) != string(data) {
		t.Errorf("json.Marshal() is not stable:\nfirst  = %s\nsecond = %s", data, again)
	}
}

// TestPlanMarshalEmpty documents what an empty plan looks like on the wire. A
// run with no repositories is a normal outcome — every group can resolve to
// nothing — and it renders as a null rather than an empty array, because Repos
// carries no omitempty and a nil slice is what "no repositories" is.
func TestPlanMarshalEmpty(t *testing.T) {
	data, err := json.Marshal(Plan{})
	if err != nil {
		t.Fatalf("json.Marshal() = %v, want no error", err)
	}

	if want := `{"repos":null}`; string(data) != want {
		t.Errorf("json.Marshal(Plan{}) = %s, want %s", data, want)
	}

	var got Plan

	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(%s) = %v, want no error", data, err)
	}

	if got.Repos != nil {
		t.Errorf("Repos = %v, want nil", got.Repos)
	}
}

// describe renders an optional field the way the tests need to talk about it:
// nil and "" have to read differently in a failure message, which %q cannot do
// for a pointer.
func describe(s *string) string {
	if s == nil {
		return "unset"
	}

	return `set to "` + strings.ReplaceAll(*s, `"`, `\"`) + `"`
}
