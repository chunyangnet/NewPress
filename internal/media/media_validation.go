package media

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func normalizeQuery(query Query) (Query, error) {
	if query.Status == "" {
		query.Status = StatusActive
	}
	if query.Status != StatusActive && query.Status != StatusTrash && query.Status != StatusAll {
		return Query{}, fmt.Errorf("invalid media status: %w", ErrInvalidInput)
	}
	query.Search = strings.TrimSpace(query.Search)
	if utf8.RuneCountInString(query.Search) > 100 || !utf8.ValidString(query.Search) {
		return Query{}, fmt.Errorf("invalid media search: %w", ErrInvalidInput)
	}
	if query.Kind != "" && !validKind(query.Kind) {
		return Query{}, fmt.Errorf("invalid media kind: %w", ErrInvalidInput)
	}
	if query.Limit == 0 {
		query.Limit = 24
	}
	if query.Limit < 1 || query.Limit > 100 || query.Offset < 0 {
		return Query{}, fmt.Errorf("invalid media pagination: %w", ErrInvalidInput)
	}
	return query, nil
}

func normalizeUploadInput(input UploadInput) (UploadInput, error) {
	if input.UploadSource == "" {
		input.UploadSource = "media-library"
	}
	if !ValidUploadSource(input.UploadSource) {
		return UploadInput{}, ErrInvalidInput
	}
	input.Filename = strings.TrimSpace(input.Filename)
	if input.Filename == "" || !utf8.ValidString(input.Filename) || strings.ContainsRune(input.Filename, '\x00') {
		return UploadInput{}, fmt.Errorf("filename is required: %w", ErrInvalidInput)
	}
	for name, candidate := range map[string]*string{
		"title": &input.Title, "alt_text": &input.AltText, "caption": &input.Caption, "uploaded_by": &input.UploadedBy,
	} {
		maximum := maxTitleRunes
		required := false
		switch name {
		case "alt_text":
			maximum = maxAltTextRunes
		case "caption":
			maximum = maxCaptionRunes
		case "uploaded_by":
			maximum = 80
		}
		*candidate = strings.TrimSpace(*candidate)
		if err := validateText(name, *candidate, required, maximum); err != nil {
			return UploadInput{}, err
		}
	}
	return input, nil
}

func normalizeUpdateInput(input UpdateInput) (UpdateInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.AltText = strings.TrimSpace(input.AltText)
	input.Caption = strings.TrimSpace(input.Caption)
	if err := validateText("title", input.Title, true, maxTitleRunes); err != nil {
		return UpdateInput{}, err
	}
	if err := validateText("alt_text", input.AltText, false, maxAltTextRunes); err != nil {
		return UpdateInput{}, err
	}
	if err := validateText("caption", input.Caption, false, maxCaptionRunes); err != nil {
		return UpdateInput{}, err
	}
	return input, nil
}

func validateText(name, value string, required bool, maximum int) error {
	if required && value == "" {
		return fmt.Errorf("%s is required: %w", name, ErrInvalidInput)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("%s is invalid: %w", name, ErrInvalidInput)
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return fmt.Errorf("%s contains control characters: %w", name, ErrInvalidInput)
		}
	}
	return nil
}

func normalizedFilename(original, extension string) string {
	base := strings.ReplaceAll(original, "\\", "/")
	base = filepath.Base(base)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = strings.Trim(stem, " .")
	var builder strings.Builder
	lastSeparator := false
	for _, character := range stem {
		allowed := unicode.IsLetter(character) || unicode.IsDigit(character) || character == '-' || character == '_' || character == ' ' || character == '(' || character == ')'
		if !allowed {
			character = '-'
		}
		separator := character == '-' || character == ' '
		if separator && lastSeparator {
			continue
		}
		builder.WriteRune(character)
		lastSeparator = separator
		if utf8.RuneCountInString(builder.String()) >= maxFilenameRunes-len([]rune(extension)) {
			break
		}
	}
	stem = strings.Trim(builder.String(), " .-_")
	if stem == "" {
		stem = "media"
	}
	return stem + extension
}

func validateStoredItem(item Item) error {
	if !ValidUploadSource(item.UploadSource) {
		return ErrInvalidInput
	}
	if item.ID <= 0 || item.Version <= 0 || item.SizeBytes <= 0 || item.Storage != "local" || !validKind(item.Kind) {
		return errors.New("invalid core media metadata")
	}
	if _, extension, kind, err := classifyStoredMIME(item.MIMEType); err != nil || kind != item.Kind || !strings.HasSuffix(item.ObjectKey, extension) {
		return errors.New("invalid stored media type")
	}
	if len(item.ChecksumSHA256) != 64 {
		return errors.New("invalid media checksum length")
	}
	if _, err := hex.DecodeString(item.ChecksumSHA256); err != nil {
		return errors.New("invalid media checksum")
	}
	if err := validateObjectKey(item.ObjectKey); err != nil {
		return err
	}
	expectedPrefix := "objects/" + item.ChecksumSHA256[:2] + "/" + item.ChecksumSHA256
	if !strings.HasPrefix(item.ObjectKey, expectedPrefix) {
		return errors.New("media object key does not match checksum")
	}
	if item.PublicURL != "/media/"+fmt.Sprintf("%d", item.ID)+"/"+url.PathEscape(item.Filename) {
		return errors.New("media public URL is not canonical")
	}
	if err := validateText("filename", item.Filename, true, maxFilenameRunes); err != nil {
		return err
	}
	if err := validateText("title", item.Title, true, maxTitleRunes); err != nil {
		return err
	}
	if err := validateText("alt_text", item.AltText, false, maxAltTextRunes); err != nil {
		return err
	}
	if err := validateText("caption", item.Caption, false, maxCaptionRunes); err != nil {
		return err
	}
	if item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) {
		return errors.New("invalid media timestamps")
	}
	if !item.TrashedAt.IsZero() && (item.Version < 2 || item.TrashedAt.Before(item.CreatedAt) || item.TrashedAt.After(item.UpdatedAt)) {
		return errors.New("invalid media trash timestamp")
	}
	if item.Kind == KindImage {
		if item.Width <= 0 || item.Height <= 0 || item.Width > maxImageDimension || item.Height > maxImageDimension || int64(item.Width)*int64(item.Height) > maxImagePixels {
			return errors.New("invalid stored image dimensions")
		}
	} else if item.Width != 0 || item.Height != 0 {
		return errors.New("non-image media cannot carry dimensions")
	}
	return validateImageSet(item)
}

func validateObjectKey(objectKey string) error {
	if objectKey == "" || strings.Contains(objectKey, "\\") || strings.Contains(objectKey, ":") || strings.ContainsRune(objectKey, '\x00') {
		return errors.New("invalid media object key")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(objectKey)))
	if clean != objectKey || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") || !strings.HasPrefix(clean, "objects/") {
		return errors.New("unsafe media object key")
	}
	return nil
}
