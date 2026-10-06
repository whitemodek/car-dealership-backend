package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCreateLocalFileNeverOverwritesConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := createLocalFile(path, "existing-local-configuration\n"); err != nil {
		t.Fatal(err)
	}
	if err := createLocalFile(path, "replacement\n"); err == nil {
		t.Fatal("existing configuration was overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing-local-configuration\n" {
		t.Fatal("configuration content changed")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatal("environment file must be readable only by its owner")
		}
	}
}
