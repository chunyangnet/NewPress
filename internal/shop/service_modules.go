package shop

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
)

const MaxServiceModules = 60

var ErrServiceModules = errors.New("invalid product service modules")

// A nil configuration selects automatic service descriptions.
type ServiceModules struct {
	Mode  string          `json:"mode"`
	Items []ServiceModule `json:"items,omitempty"`
}

type ServiceModule struct {
	Template    string `json:"template,omitempty"`
	Icon        string `json:"icon"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Link        string `json:"link,omitempty"`
}

type ServiceTemplate struct {
	ServiceModule
	Category string `json:"category"`
}

type ServiceIcon struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

type ServiceLibrary struct {
	Templates []ServiceTemplate `json:"templates"`
	Icons     []ServiceIcon     `json:"icons"`
	Limit     int               `json:"limit"`
}

//go:embed service_templates.json
var serviceTemplatesJSON []byte

var serviceLibrary = func() ServiceLibrary {
	var library ServiceLibrary
	if err := json.Unmarshal(serviceTemplatesJSON, &library); err != nil {
		panic(err)
	}
	library.Limit = MaxServiceModules
	return library
}()

func ServiceModuleLibrary() ServiceLibrary {
	library := serviceLibrary
	library.Templates = slices.Clone(library.Templates)
	library.Icons = slices.Clone(library.Icons)
	return library
}

func serviceLinkOK(value string) bool {
	if value == "" || member(value, "#shop-product-description", "#shop-product-reviews", "#shop-product-specifications", "#shop-product-rules") {
		return true
	}
	if len(value) > 2000 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\\\r\n\t\x00<>") || strings.HasPrefix(value, "//") {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.User != nil || strings.ContainsAny(u.Path, "\\\r\n\t\x00") {
		return false
	}
	return u.Scheme == "https" && u.Hostname() != "" || u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//")
}

func serviceModulesOK(config *ServiceModules) bool {
	if config == nil {
		return true
	}
	if !member(config.Mode, "auto", "custom", "hidden") || len(config.Items) > MaxServiceModules {
		return false
	}
	seen := map[string]bool{}
	for _, item := range config.Items {
		if !nameOK(item.Title, 24) || !textOK(item.Description, 100) || !serviceLinkOK(item.Link) || !slices.ContainsFunc(serviceLibrary.Icons, func(icon ServiceIcon) bool { return icon.Key == item.Icon }) {
			return false
		}
		if item.Template != "" {
			if seen[item.Template] || !slices.ContainsFunc(serviceLibrary.Templates, func(template ServiceTemplate) bool { return template.Template == item.Template }) {
				return false
			}
			seen[item.Template] = true
		}
	}
	return true
}
