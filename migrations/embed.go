package migrations

import "embed"

// FS contains the SQLite migrations applied by store.Open.
//
//go:embed *.sql
var FS embed.FS
