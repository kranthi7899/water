package config

import (
	"os"
	"path/filepath"
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
