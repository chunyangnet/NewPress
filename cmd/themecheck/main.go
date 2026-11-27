// themecheck validates a theme package and renders public/v1 contract fixtures.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"newpress.local/newpress/internal/presentation"
	"newpress.local/newpress/internal/theme"
)

func main() {
	source := flag.String("source", "themes/starter", "theme directory")
	root := flag.String("themes-dir", "", "local parent themes directory (default: source siblings)")
	structure := flag.Bool("structure-only", false, "validate composition without public fixture rendering")
	flag.Parse()
	if err := checkWithDependencies(*source, *root, *structure); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func check(source string) error { return checkWithDependencies(source, "", false) }
func checkWithDependencies(source, root string, structureOnly bool) error {
	directory, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	if root == "" {
		root = filepath.Dir(directory)
	}
	manager := theme.NewManager(root, filepath.Base(directory))
	if err := manager.LoadSource(directory); err != nil {
		return err
	}
	info := manager.Active()
	if structureOnly {
		fmt.Println(info.Slug + ": composition validated; render fixtures NOT run")
		return nil
	}
	if info.View != theme.PublicViewV1 {
		return fmt.Errorf("%s uses the Chunnel view; fixture validation requires view=public/v1", info.Slug)
	}
	count := 0
	for name, p := range presentation.Fixtures() {
		p.Settings = manager.SettingsSnapshot().Values
		for page := range info.Templates {
			p.Kind = page
			var output theme.RenderBuffer
			if err := manager.Render(&output, page, p); err != nil {
				return fmt.Errorf("%s/%s: %w", name, page, err)
			}
			count++
		}
	}
	fmt.Printf("%s %s: manifest, settings, paths, templates and %d fixture renders passed\n", info.Slug, info.Version, count)
	return nil
}
