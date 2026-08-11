package plan

// prune.go is the selection half of prune mode: naming the removal candidates a
// computed plan carries, and narrowing a plan down to the ones that were chosen.
//
// [Compute] records every unconfigured label as a candidate and decides nothing
// about which of them are removed. That decision belongs to the caller — an
// interactive selection, or --prune=all — and it arrives back here as a set of
// [Candidate]s for [RetainDeletes] to filter the plan by. Keeping both halves
// pure is what makes prune semantics testable with two slices and no terminal.

// Candidate identifies one removal candidate: a repository, and the name of the
// label in it that the config does not mention.
//
// It is a comparable value rather than an [Action] so a selection can be carried
// as a set, and so that the thing a user is shown and the thing they chose are
// the same type. The name is the one the repository will hold *after* the rename
// pass, because that is what [Compute] emitted and a delete is applied last.
type Candidate struct {
	Repo string `json:"repo"` // owner/repo
	Name string `json:"name"`
}

// Candidates lists every removal candidate in p, in plan order: repositories in
// the order the plan holds them, and within each one the ascending name order
// [Compute] emitted.
//
// Order is the whole reason this exists rather than a caller walking the plan
// itself. What a user is offered has to read the same way twice over the same
// input, because the alternative is a destructive prompt whose rows move between
// runs.
func Candidates(p Plan) []Candidate {
	var out []Candidate

	for _, repoPlan := range p.Repos {
		for _, action := range repoPlan.Actions {
			if action.Kind == KindDelete {
				out = append(out, Candidate{Repo: repoPlan.Repo, Name: action.Name})
			}
		}
	}

	return out
}

// RetainDeletes returns p with every delete that keep does not name removed.
// Every other action survives untouched, and so does every repository: one whose
// candidates were all deselected is still a repository the run visited and still
// applies its creates and updates.
//
// Filtering rather than adding is deliberate. The plan the user was shown is the
// plan that gets applied, minus what they declined — so a candidate can only ever
// be dropped between the report and the writes, never introduced. Passing every
// candidate back, which is what --prune=all does, returns p unchanged.
//
// The repository a candidate belongs to is [RepoPlan.Repo] rather than
// [Action.Repo]: grouping is what a plan *is*, and an action carrying a
// disagreeing repository is a plan that was read back rather than computed.
func RetainDeletes(p Plan, keep []Candidate) Plan {
	kept := make(map[Candidate]struct{}, len(keep))
	for _, candidate := range keep {
		kept[candidate] = struct{}{}
	}

	out := Plan{Repos: make([]RepoPlan, 0, len(p.Repos))}

	for _, repoPlan := range p.Repos {
		filtered := make([]Action, 0, len(repoPlan.Actions))

		for _, action := range repoPlan.Actions {
			if action.Kind == KindDelete {
				if _, ok := kept[Candidate{Repo: repoPlan.Repo, Name: action.Name}]; !ok {
					continue
				}
			}

			filtered = append(filtered, action)
		}

		// Nil rather than an empty slice, so that a plan with nothing left to do
		// is indistinguishable from one Compute produced for a repository that
		// needed nothing. The difference is invisible in JSON and visible to
		// reflect.DeepEqual, which is the one place it would bite.
		if len(filtered) == 0 {
			filtered = nil
		}

		repoPlan.Actions = filtered

		out.Repos = append(out.Repos, repoPlan)
	}

	return out
}
