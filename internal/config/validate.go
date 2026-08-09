package config

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// SchemaVersion is the config schema this binary understands. A file that does
// not name it is rejected rather than assumed, so a future version 2 cannot be
// read as though it were a version 1 with unfamiliar keys.
const SchemaVersion = 1

// The label bounds GitHub enforces. Both count Unicode code points, not bytes
// and not grapheme clusters — measured against the live API in #18, and
// recorded in docs/design.md. Overflow is a 422 rather than a truncation, so
// these mirror the API rather than being stricter than it.
const (
	MaxNameRunes        = 50
	MaxDescriptionRunes = 100
)

// zeroWidthJoiner glues the parts of a composed emoji together. It carries no
// text of its own, so it never makes a name "more than native emoji".
const zeroWidthJoiner = '‍'

// colorPattern matches a colour once normalisation has stripped its "#". Upper
// case is accepted so that a hand-built Config validates the same way a parsed
// one does; a parsed colour is lowercase by the time it gets here.
var colorPattern = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)

// emojiRanges is the pictographic part of Unicode, near enough for the
// emoji-only name rule. It errs towards *not* matching: a rune this table
// misses only means an emoji-only name reaches GitHub and comes back a 422,
// whereas a rune it wrongly matches would reject a name GitHub accepts.
var emojiRanges = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x00a9, Hi: 0x00ae, Stride: 0x0005}, // © ®
		{Lo: 0x203c, Hi: 0x203c, Stride: 1},      // ‼
		{Lo: 0x2049, Hi: 0x2049, Stride: 1},      // ⁉
		{Lo: 0x20e3, Hi: 0x20e3, Stride: 1},      // combining keycap
		{Lo: 0x2122, Hi: 0x2122, Stride: 1},      // ™
		{Lo: 0x2139, Hi: 0x2139, Stride: 1},      // ℹ
		{Lo: 0x2194, Hi: 0x21aa, Stride: 1},      // arrows with emoji presentation
		{Lo: 0x231a, Hi: 0x23fa, Stride: 1},      // ⌚ ⏰ ⏺ and friends
		{Lo: 0x24c2, Hi: 0x24c2, Stride: 1},      // Ⓜ
		{Lo: 0x25aa, Hi: 0x25fe, Stride: 1},      // geometric shapes
		{Lo: 0x2600, Hi: 0x27bf, Stride: 1},      // misc symbols and dingbats
		{Lo: 0x2934, Hi: 0x2935, Stride: 1},      // ⤴ ⤵
		{Lo: 0x2b00, Hi: 0x2bff, Stride: 1},      // ⬅ ⭐ and the rest of the block
		{Lo: 0x3030, Hi: 0x3030, Stride: 1},      // 〰
		{Lo: 0x303d, Hi: 0x303d, Stride: 1},      // 〽
		{Lo: 0x3297, Hi: 0x3299, Stride: 0x0002}, // ㊗ ㊙
		{Lo: 0xfe00, Hi: 0xfe0f, Stride: 1},      // variation selectors
	},
	R32: []unicode.Range32{
		{Lo: 0x1f000, Hi: 0x1faff, Stride: 1}, // the emoji planes proper
		{Lo: 0xe0020, Hi: 0xe007f, Stride: 1}, // tag characters, used by flag sequences
	},
}

// Validate applies every rule in the design's validation table to an already
// normalised Config, and returns the first one broken, wrapped in its sentinel.
//
// It runs entirely offline and is the whole of the tool's input checking: once
// it returns nil, no later stage re-asks whether a colour is hex or whether a
// group a label names exists. LoadFile calls it, so a config that reaches the
// planner has passed. Parse deliberately does not, which is what lets the
// normalisation tests drive fragments that were never meant to be whole files.
//
// The first broken rule wins rather than a collected list. A config file is
// edited by hand and re-run in a second; a wall of errors, most of them
// knock-on effects of the first, reads worse than one that names the line to
// fix.
func (c *Config) Validate() error {
	if c.Version != SchemaVersion {
		return fmt.Errorf("%w: %d, want %d", labelsync.ErrUnsupportedConfigVersion, c.Version, SchemaVersion)
	}

	if len(c.Labels) == 0 {
		return labelsync.ErrEmptyConfig
	}

	if err := c.validateGroups(); err != nil {
		return err
	}

	if err := c.validateLabels(); err != nil {
		return err
	}

	return c.validateRenames()
}

// validateGroups delegates every rule about the group graph — a group's source,
// its repository references, the groups include_groups names, and the absence
// of a cycle — to resolve.go, which has to establish all four anyway before it
// can produce a selector. One implementation, one set of messages: the earlier
// arrangement had a copy of each rule in both files, and they drifted, printing
// a cycle chain one way here and another way there.
//
// The login is empty because nothing checked here depends on who the token
// belongs to: it only picks which user endpoint a selector will use, and a
// selector is not what this call is after. The resolution is discarded — the
// caller that wants selectors resolves once the login is known.
func (c *Config) validateGroups() error {
	_, err := c.Resolve("")

	return err
}

// validateLabels checks each label on its own, then the two rules that are
// about the set: names unique case-insensitively, and colours unique globally.
func (c *Config) validateLabels() error {
	// Colour uniqueness is global rather than per-repository, deliberately.
	// Two labels in groups that never share a repository could reuse a colour
	// without ever colliding, but global uniqueness needs no group resolution,
	// holds offline, and keeps a diff readable at a glance. Do not "fix" this
	// into a per-repository check: it would trade a rule a user can hold in
	// their head for one that depends on what the API returns that day.
	names := make(map[string]string, len(c.Labels))
	colors := make(map[string]string, len(c.Labels))

	for _, label := range c.Labels {
		if err := validateLabelName(label.Name); err != nil {
			return err
		}

		if !colorPattern.MatchString(label.Color) {
			return fmt.Errorf("%w: label %q has %q", labelsync.ErrInvalidColor, label.Name, label.Color)
		}

		if n := utf8.RuneCountInString(label.Description); n > MaxDescriptionRunes {
			return fmt.Errorf("%w: label %q has %d code points, the maximum is %d",
				labelsync.ErrDescriptionTooLong, label.Name, n, MaxDescriptionRunes)
		}

		key := strings.ToLower(label.Name)
		if first, ok := names[key]; ok {
			return fmt.Errorf("%w: %q and %q differ only by case", labelsync.ErrDuplicateLabelName, first, label.Name)
		}

		names[key] = label.Name

		color := strings.ToLower(label.Color)
		if first, ok := colors[color]; ok {
			return fmt.Errorf("%w: %q and %q are both %s", labelsync.ErrDuplicateLabelColor, first, label.Name, color)
		}

		colors[color] = label.Name

		for _, group := range label.Groups {
			if _, ok := c.Groups[group]; !ok {
				return fmt.Errorf("%w: label %q is in %q", labelsync.ErrUnknownGroup, label.Name, group)
			}
		}
	}

	// defaults.groups is checked separately because normalisation only copies
	// it onto labels that declare no groups of their own: a config where every
	// label names its groups would otherwise never look at it, and the typo
	// would surface the day someone added a label without groups.
	for _, group := range c.Defaults.Groups {
		if _, ok := c.Groups[group]; !ok {
			return fmt.Errorf("%w: defaults.groups names %q", labelsync.ErrUnknownGroup, group)
		}
	}

	return nil
}

// validateLabelName enforces the three name rules, all of them GitHub's own:
// non-empty, at most 50 code points, and never emoji alone. The name arrives
// trimmed — normalisation does that, because GitHub stores names trimmed and a
// bound measured before trimming would reject a name the API would have taken.
func validateLabelName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: a label name cannot be empty", labelsync.ErrInvalidLabelName)

	case utf8.RuneCountInString(name) > MaxNameRunes:
		return fmt.Errorf("%w: %q is %d code points, the maximum is %d",
			labelsync.ErrInvalidLabelName, name, utf8.RuneCountInString(name), MaxNameRunes)

	case isEmojiOnly(name):
		return fmt.Errorf("%w: %q is emoji only, GitHub requires at least one other character",
			labelsync.ErrInvalidLabelName, name)
	}

	return nil
}

// isEmojiOnly reports whether a name carries nothing but emoji — the rule
// behind GitHub's "name must contain more than native emoji" 422. Emoji in a
// name are fine ("🐛 bug"); an emoji as the whole of one is not ("🐛").
//
// Whitespace and the zero-width joiner do not count as the "more": "🐛 🐞" is
// as emoji-only as "🐛🐞" is.
func isEmojiOnly(name string) bool {
	for _, r := range name {
		if r == zeroWidthJoiner || unicode.IsSpace(r) || unicode.Is(emojiRanges, r) {
			continue
		}

		return false
	}

	return true
}

// validateRenames checks the two rules that keep a rename meaningful, both of
// them case-insensitive because a label's identity is.
func (c *Config) validateRenames() error {
	configured := make(map[string]string, len(c.Labels))
	for _, label := range c.Labels {
		configured[strings.ToLower(label.Name)] = label.Name
	}

	froms := make(map[string]string, len(c.Renames))
	tos := make(map[string]string, len(c.Renames))

	for _, rename := range c.Renames {
		if rename.From == "" || rename.To == "" {
			return fmt.Errorf("%w: both from and to are required, got from %q and to %q",
				labelsync.ErrInvalidRename, rename.From, rename.To)
		}

		from, to := strings.ToLower(rename.From), strings.ToLower(rename.To)

		if _, ok := configured[to]; !ok {
			return fmt.Errorf("%w: %q renames to %q, which no label declares",
				labelsync.ErrInvalidRename, rename.From, rename.To)
		}

		// A "from" that is itself configured describes a label the tool is
		// already converging, which includes the case-only rename "bug" → "Bug":
		// step 5 of the algorithm fixes casing drift on its own, so such an entry
		// is redundant at best and, read literally, asks to rename a label to
		// itself.
		if name, ok := configured[from]; ok {
			return fmt.Errorf("%w: %q is itself a configured label (%q)", labelsync.ErrInvalidRename, rename.From, name)
		}

		if first, ok := froms[from]; ok {
			return fmt.Errorf("%w: %q is renamed twice (also as %q)", labelsync.ErrInvalidRename, rename.From, first)
		}

		// Two renames onto one name would have the second collide with the label
		// the first just created — a 422 already_exists, halfway through a run.
		if first, ok := tos[to]; ok {
			return fmt.Errorf("%w: %q and %q both rename to %q", labelsync.ErrInvalidRename, first, rename.From, rename.To)
		}

		froms[from], tos[to] = rename.From, rename.From
	}

	// Chained renames — a → b → c — cannot survive the rules above, because b
	// would have to be both a configured label (as the first entry's target)
	// and not one (as the second entry's source). Checked anyway, so that the
	// message names the chain instead of leaving the user to work out why the
	// middle name is the one being complained about.
	for _, rename := range c.Renames {
		if first, ok := froms[strings.ToLower(rename.To)]; ok {
			return fmt.Errorf("%w: %q renames to %q, which is itself renamed to %q — renames are not chained",
				labelsync.ErrInvalidRename, rename.From, rename.To, first)
		}
	}

	return nil
}
