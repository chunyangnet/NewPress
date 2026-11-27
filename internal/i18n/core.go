package i18n

import (
	"embed"
	"sync"
)

//go:embed locales/*.json
var coreFiles embed.FS

var coreCatalog = sync.OnceValue(func() *Catalog {
	catalog, err := LoadFS(coreFiles, "locales", DefaultLocale)
	if err != nil {
		panic(err)
	} // Invalid embedded resources are a build defect.
	return catalog
})

func Core() *Catalog { return coreCatalog() }
