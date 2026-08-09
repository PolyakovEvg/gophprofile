// Package migrations embeds the database schema migration files so the
// application can apply them without depending on the filesystem layout at
// runtime.
package migrations

import "embed"

// Files contains the embedded SQL migration files.
//
//go:embed *.sql
var Files embed.FS
