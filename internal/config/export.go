package config

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// ExportGroup is the name of the group an exported config declares. A config
// with no groups at all would parse and validate and then select nothing, so the
// export names the repository it came from and points defaults.groups at it —
// the file is usable as it lands rather than after an edit nothing told the user
// to make.
const ExportGroup = "exported"

// exportIndent matches what the scaffold and the documentation use. yaml.v3
// defaults to four, which makes an exported file look nothing like the example
// it is meant to be edited alongside.
const exportIndent = 2

// duplicateColorNote is put on both labels of a colour a repository uses twice.
//
// The export is deliberately faithful rather than repaired: the labels really do
// share a colour, and inventing a different one would produce a config that no
// longer describes the repository it came from. Colour uniqueness is a rule the
// loader enforces globally, so such a file is rejected until a human picks — and
// being told which two labels to look at is the whole difference between a
// puzzle and an edit.
const duplicateColorNote = "colours must be unique across the file; change one"

// Export renders labels as a config file for repo.
//
// Labels are sorted by name so that two exports of the same repository produce
// the same bytes, whatever order the API listed them in, and colours go through
// the same normalisation the loader applies — so export, load, export is a fixed
// point rather than a diff.
//
// The result validates clean, with one exception it flags rather than hides: a
// repository holding two labels of the same colour is exported as it is, with a
// comment on both saying so. See [duplicateColorNote].
func Export(repo string, labels []Label) ([]byte, error) {
	sorted := make([]Label, len(labels))

	for i, label := range labels {
		sorted[i] = Label{
			Name:        strings.TrimSpace(label.Name),
			Color:       normalizeColor(label.Color),
			Description: label.Description,
		}
	}

	// Case-insensitively, because that is how a reader scans a list and how
	// GitHub compares the names. The tie-break is exact, so the order is total
	// even though a repository cannot in fact hold two names differing by case.
	slices.SortFunc(sorted, func(a, b Label) int {
		if folded := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); folded != 0 {
			return folded
		}

		return strings.Compare(a.Name, b.Name)
	})

	doc, err := exportDocument(repo, sorted)
	if err != nil {
		return nil, err
	}

	var out strings.Builder

	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(exportIndent)

	if err := encoder.Encode(doc); err != nil {
		return nil, fmt.Errorf("rendering the exported config: %w", err)
	}

	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("rendering the exported config: %w", err)
	}

	return []byte(out.String()), nil
}

// DuplicateColors returns the colours more than one of labels uses, each with
// the names that share it, sorted.
//
// It is exported because the command warns about them on stderr as well as
// commenting them in the file: a redirected export is a file nobody reads until
// the next run rejects it.
func DuplicateColors(labels []Label) map[string][]string {
	byColor := make(map[string][]string, len(labels))

	for _, label := range labels {
		color := normalizeColor(label.Color)
		byColor[color] = append(byColor[color], strings.TrimSpace(label.Name))
	}

	for color, names := range byColor {
		if len(names) < 2 {
			delete(byColor, color)

			continue
		}

		slices.Sort(names)
	}

	return byColor
}

// exportDocument builds the YAML node tree, which is the only way to attach the
// comments — marshalling a struct would produce the same data and none of the
// prose, and the prose is what stops a user meeting the rules the hard way.
func exportDocument(repo string, labels []Label) (*yaml.Node, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}

	version := scalarNode("1")
	version.Tag = "!!int"

	appendField(root, "version", version)

	groups := &yaml.Node{Kind: yaml.MappingNode}
	appendField(groups, ExportGroup, mappingOf("repos", sequenceOf(repo)))

	group := field(root, "groups", groups)
	group.HeadComment = "\nThe repository this file was exported from. Add more, or replace this with an\n" +
		"org: or user: selector — see labelsync init for a worked example."

	defaults := mappingOf("groups", flowSequenceOf(ExportGroup))

	defaultsKey := field(root, "defaults", defaults)
	defaultsKey.HeadComment = "\nEvery label below belongs to this group, since none of them names one."

	labelsNode, err := labelNodes(labels)
	if err != nil {
		return nil, err
	}

	labelsKey := field(root, "labels", labelsNode)
	labelsKey.HeadComment = "\nExactly what the repository holds today, sorted by name."

	// Wrapped in a document node, and the header goes there rather than on the
	// first key: yaml.v3 hoists a leading key's head comment out of the document
	// and drops it.
	return &yaml.Node{
		Kind:        yaml.DocumentNode,
		HeadComment: exportHeader(repo),
		Content:     []*yaml.Node{root},
	}, nil
}

// exportHeader is the comment at the top of the file: what it is, and the one
// thing a reader has to know before running anything against it.
func exportHeader(repo string) string {
	return "labelsync export of " + repo + "\n" +
		"\n" +
		"Descriptions are authoritative: a label whose description this file does not\n" +
		"carry has its description cleared on the next sync. That is what this export\n" +
		"is for — it captures the ones the repository already has.\n" +
		"\n" +
		"Check it before it changes anything:\n" +
		"\n" +
		"  labelsync sync --dry-run --config " + defaultExportName
}

// defaultExportName is the file name the header suggests, and the one the
// command writes when --out names a directory.
const defaultExportName = "labels.yml"

// labelNodes renders the label list, annotating any colour more than one label
// uses.
func labelNodes(labels []Label) (*yaml.Node, error) {
	duplicates := DuplicateColors(labels)

	seq := &yaml.Node{Kind: yaml.SequenceNode}

	for _, label := range labels {
		entry := &yaml.Node{Kind: yaml.MappingNode}

		appendField(entry, "name", quotedNode(label.Name))

		color := quotedNode(label.Color)

		if names, dup := duplicates[label.Color]; dup {
			color.LineComment = fmt.Sprintf("also %s — %s", strings.Join(others(names, label.Name), ", "), duplicateColorNote)
		}

		appendField(entry, "color", color)

		// Written even when empty, and quoted, so that the file says what it
		// means: an absent key and an empty one are the same thing to the
		// loader, but only one of them tells a reader the description really is
		// blank rather than forgotten.
		appendField(entry, "description", quotedNode(label.Description))

		seq.Content = append(seq.Content, entry)
	}

	if len(seq.Content) == 0 {
		// The file it would write declares no labels, which is exactly the rule
		// the loader would reject it by — so it fails now, wrapped in that same
		// sentinel, rather than as a puzzle on the next run.
		return nil, fmt.Errorf("%w: there is nothing to export", labelsync.ErrEmptyConfig)
	}

	return seq, nil
}

// others is names without one of them, quoted for the comment.
func others(names []string, self string) []string {
	out := make([]string, 0, len(names)-1)

	for _, name := range names {
		if name != self {
			out = append(out, fmt.Sprintf("%q", name))
		}
	}

	return out
}

// field appends a key/value pair and returns the key node, so the caller can put
// a comment on it — yaml.v3 renders a mapping key's HeadComment above the pair.
func field(parent *yaml.Node, key string, value *yaml.Node) *yaml.Node {
	name := scalarNode(key)

	parent.Content = append(parent.Content, name, value)

	return name
}

// appendField is field for the pairs nothing has anything to say about.
func appendField(parent *yaml.Node, key string, value *yaml.Node) {
	_ = field(parent, key, value)
}

func mappingOf(key string, value *yaml.Node) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	appendField(node, key, value)

	return node
}

func sequenceOf(values ...string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode}
	for _, value := range values {
		node.Content = append(node.Content, quotedNode(value))
	}

	return node
}

// flowSequenceOf is sequenceOf on one line — `[exported]` — which is how
// defaults.groups reads in every example.
func flowSequenceOf(values ...string) *yaml.Node {
	node := sequenceOf(values...)
	node.Style = yaml.FlowStyle

	return node
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

// quotedNode is a scalar that is always quoted.
//
// Not decoration: a colour like `123456` is a string the loader wants and an int
// YAML would otherwise hand it, and a label named `yes` or `null` is a bool and a
// null. Quoting every value takes the question away rather than leaving it to
// whichever label somebody adds next.
func quotedNode(value string) *yaml.Node {
	node := scalarNode(value)
	node.Style = yaml.DoubleQuotedStyle
	node.Tag = "!!str"

	return node
}
