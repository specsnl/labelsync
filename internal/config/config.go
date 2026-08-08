// Package config finds the labelsync config file, parses it, and normalises
// what it parsed. Nothing here validates and nothing here touches the network:
// the rules in the design's validation table live in validate.go, and turning
// groups into repository sets lives in resolve.go.
//
// Normalisation is the part every later stage depends on. Once Parse returns,
// colours are bare lowercase hex, label names are trimmed, every label carries
// the groups it belongs to — its own, or the ones defaults.groups supplies —
// and every group's skip_archived, skip_forks, and visibility hold a real
// value rather than a Go zero value that happens to mean "unset".
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// Visibility selects which repositories an org or user group enumerates.
type Visibility string

// The accepted visibility values. VisibilityAll is the default a group gets
// when it does not say.
const (
	VisibilityAll     Visibility = "all"
	VisibilityPublic  Visibility = "public"
	VisibilityPrivate Visibility = "private"
)

// Config is a parsed labels.yml.
type Config struct {
	Version  int      `yaml:"version"`
	Groups   Groups   `yaml:"groups,omitempty"`
	Defaults Defaults `yaml:"defaults,omitempty"`
	Renames  []Rename `yaml:"renames,omitempty"`
	Labels   []Label  `yaml:"labels,omitempty"`

	// Path is the file this Config was read from, for error messages. It is
	// not a config field: Parse leaves it empty, LoadFile fills it in.
	Path string `yaml:"-"`
}

// Groups is the config's groups section, keyed by group name.
type Groups map[string]Group

// Group is one repository selector. Exactly one of Org, User, Repos, or
// IncludeGroups is the group's source — validate.go enforces that; parsing
// accepts whatever the file says.
type Group struct {
	Org           string   `yaml:"org,omitempty"`
	User          string   `yaml:"user,omitempty"`
	Repos         []string `yaml:"repos,omitempty"`
	IncludeGroups []string `yaml:"include_groups,omitempty"`

	// Include and Exclude are globs over the repository name only, not
	// owner/repo. Exclude is applied after Include.
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`

	// SkipArchived, SkipForks, and Visibility apply to org and user sources.
	// All three are defaulted while decoding, not afterwards, because an
	// explicit "skip_archived: false" and an omitted key are both false by the
	// time a plain struct has been filled in.
	SkipArchived bool       `yaml:"skip_archived"`
	SkipForks    bool       `yaml:"skip_forks"`
	Visibility   Visibility `yaml:"visibility"`
}

// Defaults holds the fallbacks applied to labels that do not speak for
// themselves.
type Defaults struct {
	// Groups is applied to every label that declares no groups of its own.
	Groups []string `yaml:"groups,omitempty"`
}

// Rename maps an existing label name onto a configured one. Renames are
// applied before matching, so the issue and PR associations survive.
type Rename struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// Label is one desired label.
//
// Description is authoritative and deliberately a plain string: the design
// defines an omitted description as "clear it", which is exactly what an empty
// string means downstream, so an omitted and an explicitly empty description
// need not be told apart.
type Label struct {
	Name        string   `yaml:"name"`
	Color       string   `yaml:"color"`
	Description string   `yaml:"description,omitempty"`
	Groups      []string `yaml:"groups,omitempty"`
}

// nullTag is the resolved tag of a YAML null, including the implicit one a
// bare key with nothing under it produces.
const nullTag = "!!null"

// defaultGroup is a group before the file has said anything about it.
func defaultGroup() Group {
	return Group{
		SkipArchived: true,
		SkipForks:    true,
		Visibility:   VisibilityAll,
	}
}

// UnmarshalYAML decodes a group over the defaults rather than over a zero
// value, which is the only way "skip_archived: false" can be distinguished
// from an omitted skip_archived once the result is a plain bool.
//
// The alias type is what stops this from recursing: it has the same fields and
// tags, and no UnmarshalYAML method.
func (g *Group) UnmarshalYAML(value *yaml.Node) error {
	type rawGroup Group

	raw := rawGroup(defaultGroup())

	if err := value.Decode(&raw); err != nil {
		return err
	}

	*g = Group(raw)

	return nil
}

// UnmarshalYAML decodes the groups section entry by entry, which is what a
// group written as a bare key ("self:") needs: yaml.v3 resolves a null node to
// a zero value without ever consulting the value's own UnmarshalYAML, so such
// a group would silently come out with skip_archived and skip_forks false and
// no visibility at all.
func (gs *Groups) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: groups must be a mapping of group name to group", value.Line)
	}

	out := make(Groups, len(value.Content)/2)

	for i := 0; i+1 < len(value.Content); i += 2 {
		var name string

		if err := value.Content[i].Decode(&name); err != nil {
			return err
		}

		group := defaultGroup()

		if node := value.Content[i+1]; node.ShortTag() != nullTag {
			if err := node.Decode(&group); err != nil {
				return err
			}
		}

		out[name] = group
	}

	*gs = out

	return nil
}

// Load resolves the config file and parses it. The explicit path is the
// --config flag; empty means "search". See Find for the resolution order.
func Load(explicit string) (*Config, error) {
	path, err := Find(explicit)
	if err != nil {
		return nil, err
	}

	return LoadFile(path)
}

// Find returns the path of the config file to use, in this order:
//
//  1. explicit, when --config was given
//  2. labels.yml or labels.yaml in the working directory
//  3. labels.yml or labels.yaml under the XDG config directory
//
// An explicit path that names a directory is searched the same way as the
// other two. A directory holding both spellings is ErrAmbiguousConfigFile;
// finding nothing anywhere is ErrConfigNotFound.
func Find(explicit string) (string, error) {
	if explicit != "" {
		return findExplicit(explicit)
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolving the working directory: %w", err)
	}

	dirs := []string{wd, labelsync.ConfigDir()}

	for _, dir := range dirs {
		path, err := findInDir(dir)
		if err != nil {
			return "", err
		}

		if path != "" {
			return path, nil
		}
	}

	return "", fmt.Errorf("%w: looked in %s", labelsync.ErrConfigNotFound, strings.Join(dirs, " and "))
}

// LoadFile reads, parses, and normalises one config file, and records where it
// came from on the returned Config.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", labelsync.ErrConfigNotFound, path)
		}

		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	cfg.Path = path

	return cfg, nil
}

// Parse decodes config YAML and normalises the result. It is the whole of the
// load path that a test can drive without a file, and the entry point the
// golden normalisation fixtures go through.
func Parse(data []byte) (*Config, error) {
	var cfg Config

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.normalize()

	return &cfg, nil
}

// findExplicit resolves a --config value: a file is taken as given, a
// directory is searched like any other.
func findExplicit(explicit string) (string, error) {
	info, err := os.Stat(explicit)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", labelsync.ErrConfigNotFound, explicit)
		}

		return "", fmt.Errorf("reading %s: %w", explicit, err)
	}

	if !info.IsDir() {
		return explicit, nil
	}

	path, err := findInDir(explicit)
	if err != nil {
		return "", err
	}

	if path == "" {
		return "", fmt.Errorf("%w: looked in %s", labelsync.ErrConfigNotFound, explicit)
	}

	return path, nil
}

// findInDir looks for the accepted config file names in one directory. It
// returns an empty path and no error when the directory holds none of them,
// and ErrAmbiguousConfigFile when it holds more than one — the same rule
// specs-cli applies to project.yml and project.yaml.
func findInDir(dir string) (string, error) {
	var found []string

	for _, name := range labelsync.ConfigFileNames {
		path := filepath.Join(dir, name)

		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}

		found = append(found, path)
	}

	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("%w: %s", labelsync.ErrAmbiguousConfigFile, dir)
	}
}

// normalize brings a freshly decoded Config into the shape the rest of the
// tool assumes. Group defaults are not applied here — they are applied while
// decoding, see Group.UnmarshalYAML.
func (c *Config) normalize() {
	for i := range c.Labels {
		label := &c.Labels[i]

		label.Name = strings.TrimSpace(label.Name)
		label.Color = normalizeColor(label.Color)

		// A label that names no groups inherits defaults.groups. The clone
		// keeps every label's slice its own, so a later edit cannot reach
		// across labels through a shared backing array.
		if len(label.Groups) == 0 && len(c.Defaults.Groups) > 0 {
			label.Groups = slices.Clone(c.Defaults.Groups)
		}
	}

	// Renames are matched against label names, so they are trimmed the same
	// way: a trailing space in "to" would otherwise fail validation against a
	// label that looks identical.
	for i := range c.Renames {
		c.Renames[i].From = strings.TrimSpace(c.Renames[i].From)
		c.Renames[i].To = strings.TrimSpace(c.Renames[i].To)
	}
}

// normalizeColor strips one leading # and lowercases the rest. Whether what is
// left is a 6-digit hex value is validate.go's question.
func normalizeColor(raw string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
}
