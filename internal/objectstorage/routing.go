package objectstorage

import (
	"errors"
	"maps"

	"newpress.local/newpress/internal/media"
)

// An omitted route inherits the default profile; local explicitly excludes an
// entry from both automatic and manual cloud discovery.
type UploadRoute struct {
	Target string `json:"target"`
	Mode   string `json:"mode,omitempty"`
}

func validateRoutes(c Settings) error {
	for source, route := range c.UploadRoutes {
		if source != "circle-attachment" && !media.ValidUploadSource(source) {
			return errors.New("未知的上传入口")
		}
		if route.Target != "" && route.Target != "local" {
			if _, ok := profileByID(c, route.Target); !ok {
				return errors.New("上传入口引用的存储不存在，请先调整入口路由")
			}
		}
		if route.Mode != "" && route.Mode != "mirror" && route.Mode != "redirect" {
			return errors.New("上传入口访问方式不正确")
		}
	}
	return nil
}

func routeFor(c Settings, source string) (target, mode string) {
	if source != "circle-attachment" && !media.ValidUploadSource(source) {
		return "", "mirror"
	}
	r := c.UploadRoutes[source]
	if source == "circle-attachment" && r.Target == "" {
		return "", "mirror"
	}
	target, mode = r.Target, r.Mode
	if target == "" {
		target = c.ActiveProfile
	}
	if mode == "" {
		mode = c.Mode
	}
	if target == "local" {
		return "", "mirror"
	}
	return
}

func UploadEntries() []media.UploadSource {
	return append(media.UploadSources(), media.UploadSource{ID: "circle-attachment", Name: "圈子私有附件（鉴权代理读取）"})
}

func cloneRoutes(c Settings) Settings {
	c.UploadRoutes = maps.Clone(c.UploadRoutes)
	return c
}

func routeAllows(c Settings, source, profile string) bool {
	if source != "circle-attachment" && !media.ValidUploadSource(source) {
		return false
	}
	target, _ := routeFor(c, source)
	if source == "circle-attachment" && target == "" {
		return false
	}
	return c.UploadRoutes[source].Target == "" || target == profile
}
