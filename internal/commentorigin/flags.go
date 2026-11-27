package commentorigin

import (
	"embed"
	"io/fs"
	"strings"
)

// Flag artwork is served locally. The embedded filenames are also the country
// allowlist, so neither provider results nor URLs can introduce arbitrary paths.
//
//go:embed flags/*.svg
var flags embed.FS

func normalizeCountryCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if len(code) != 2 || code[0] < 'a' || code[0] > 'z' || code[1] < 'a' || code[1] > 'z' {
		return ""
	}
	if _, err := fs.Stat(flags, "flags/"+code+".svg"); err != nil {
		return ""
	}
	return strings.ToUpper(code)
}

func flagCode(mode, code string) string {
	if mode != "all" && mode != "foreign" {
		return ""
	}
	code = normalizeCountryCode(code)
	if mode == "foreign" {
		switch code {
		case "CN", "HK", "MO", "TW":
			return ""
		}
	}
	return strings.ToLower(code)
}

// FlagSVG returns only bundled ISO 3166-1 country/territory flag artwork.
func FlagSVG(code string) ([]byte, error) {
	if normalized := normalizeCountryCode(code); normalized == "" || strings.ToLower(normalized) != code {
		return nil, fs.ErrNotExist
	}
	return flags.ReadFile("flags/" + code + ".svg")
}
