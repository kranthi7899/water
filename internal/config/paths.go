package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Home returns the water data directory: $WATER_HOME or ~/.water.
func Home() string {
	if v := os.Getenv("WATER_HOME"); v != "" {
		return v
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".water"
	}
	return filepath.Join(h, ".water")
}

// Path returns the config file location.
func Path() string { return filepath.Join(Home(), "config.yaml") }

// MemoryDir is where writable per-role memory lives.
func MemoryDir() string { return filepath.Join(Home(), "memory") }

// Expand resolves a leading ~ against the user's home.
func Expand(p string) string {
	if strings.HasPrefix(p, "~/") || p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
