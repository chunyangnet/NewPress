package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/extension"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugin"
	"newpress.local/newpress/internal/version"
)

type releaseDetails struct {
	Release extension.Metadata `json:"release"`
	Notes   string             `json:"notes"`
}

func decorateReleases(catalog *plugin.Catalog, channel string, compatible bool, notesFile, metadataFile string) error {
	notes := ""
	if notesFile != "" {
		f, info, err := packagefs.OpenRegularNoSymlink(notesFile)
		if err != nil {
			return err
		}
		f.Close()
		if info.Size() > 16000 {
			return errors.New("release notes exceed 16000 bytes")
		}
		data, err := os.ReadFile(notesFile)
		if err != nil {
			return err
		}
		notes = strings.TrimSpace(string(data))
	}
	metadata := map[string]releaseDetails{}
	if metadataFile != "" {
		var err error
		metadata, err = extension.ReadJSON[map[string]releaseDetails](metadataFile, 256<<10)
		if err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	apply := func(kind, slug, value, requires string, builtin bool, current string) (*extension.Metadata, string, error) {
		v, err := version.Parse(value)
		if err != nil {
			return nil, "", err
		}
		identity := kind + "/" + slug + "@" + v.Identity()
		if seen[identity] {
			return nil, "", errors.New("duplicate semantic release (build metadata does not create a new version)")
		}
		seen[identity] = true
		if !builtin {
			if err := version.ValidateRange(requires); err != nil {
				return nil, "", err
			}
		}
		details := releaseDetails{Release: extension.Metadata{Channel: channel, PublishedAt: time.Now().UTC(), DataCompatible: compatible}, Notes: current}
		if notes != "" {
			details.Notes = notes
		}
		key := kind + "/" + slug + "@" + value
		if override, ok := metadata[key]; ok {
			details = override
			delete(metadata, key)
		}
		if err := details.Release.Validate(value); err != nil {
			return nil, "", fmt.Errorf("%s: %w", key, err)
		}
		if !utf8.ValidString(details.Notes) || len([]rune(details.Notes)) > 4000 || strings.TrimSpace(details.Notes) != details.Notes {
			return nil, "", errors.New("release notes must be valid UTF-8 and at most 4000 characters")
		}
		for _, c := range details.Notes {
			if c < 32 && c != '\n' && c != '\r' && c != '\t' || c == 127 {
				return nil, "", errors.New("release notes contain control characters")
			}
		}
		return &details.Release, details.Notes, nil
	}
	for i := range catalog.Plugins {
		r := &catalog.Plugins[i]
		meta, notes, err := apply("plugin", r.Manifest.Slug, r.Manifest.Version, r.Manifest.NewPress, isReviewedBuiltin(r.Manifest), r.Notes)
		if err != nil {
			return err
		}
		r.Release, r.Notes = meta, notes
	}
	for i := range catalog.Themes {
		r := &catalog.Themes[i]
		meta, notes, err := apply("theme", r.Manifest.Slug, r.Manifest.Version, r.Manifest.NewPress, false, r.Notes)
		if err != nil {
			return err
		}
		r.Release, r.Notes = meta, notes
	}
	if len(catalog.Plugins) > 100 || len(catalog.Themes) > 100 {
		return errors.New("at most 100 releases per kind")
	}
	if len(metadata) > 0 {
		return errors.New("release metadata contains an unmatched kind/slug@version")
	}
	return nil
}
