package migrations

import "embed"

// Files contains ordered, immutable NewPress database migrations.
//
//go:embed *.sql
var Files embed.FS
