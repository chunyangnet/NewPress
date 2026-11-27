package main

import (
	"path/filepath"
	"testing"
)

func TestChildThemeFixtures(t *testing.T) {
	if err := check(filepath.Join("..", "..", "themes", "starter-child")); err != nil {
		t.Fatal(err)
	}
}

func TestChildThemeMissingDependency(t *testing.T) {
	if err := checkWithDependencies(filepath.Join("..", "..", "themes", "starter-child"), t.TempDir(), false); err == nil {
		t.Fatal("missing parent accepted")
	}
}
