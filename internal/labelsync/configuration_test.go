package labelsync_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/adrg/xdg"
	"github.com/specsnl/labelsync/internal/labelsync"
)

func TestConfigDir_XDGOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })

	got := labelsync.ConfigDir()
	want := filepath.Join(tmp, "labelsync")

	if got != want {
		t.Errorf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestCacheDir_XDGOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })

	got := labelsync.CacheDir()
	want := filepath.Join(tmp, "labelsync")

	if got != want {
		t.Errorf("CacheDir() = %q, want %q", got, want)
	}
}

func TestConfigFileNames(t *testing.T) {
	want := []string{"labels.yml", "labels.yaml"}

	if !slices.Equal(labelsync.ConfigFileNames, want) {
		t.Errorf("ConfigFileNames = %q, want %q", labelsync.ConfigFileNames, want)
	}
}
