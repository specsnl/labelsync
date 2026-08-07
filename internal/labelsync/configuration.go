package labelsync

import (
	"path/filepath"

	"github.com/adrg/xdg"
)

const (
	// AppName is the binary name, and the directory name used under both the XDG
	// config and cache homes.
	AppName = "labelsync"

	// Config file names. Both spellings are accepted; having both present in one
	// directory is ErrAmbiguousConfigFile.
	ConfigYMLFile  = "labels.yml"
	ConfigYAMLFile = "labels.yaml"
)

// ConfigFileNames lists the accepted config file names, in the order a directory
// is searched. A directory containing more than one of them is ambiguous.
var ConfigFileNames = []string{ConfigYMLFile, ConfigYAMLFile}

// ConfigDir returns the labelsync configuration directory.
// Defaults to $XDG_CONFIG_HOME/labelsync (~/.config/labelsync).
func ConfigDir() string {
	return filepath.Join(xdg.ConfigHome, AppName)
}

// CacheDir returns the directory holding the label/ETag cache.
// Defaults to $XDG_CACHE_HOME/labelsync (~/.cache/labelsync).
func CacheDir() string {
	return filepath.Join(xdg.CacheHome, AppName)
}
