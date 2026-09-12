// Package migrations embeds the schema used by gateway-migrate.
package migrations

import "embed"

// Files contains ordered, immutable forward migrations.
//
//go:embed *.up.sql
var Files embed.FS
