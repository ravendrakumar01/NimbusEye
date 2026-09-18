// Package db embeds the SQL migrations.
//
// The embed directive lives here, beside the files, because go:embed cannot
// reach outside its own package directory. The alternative — copying the SQL
// into the migration runner's package at build time — means two copies of every
// migration and an eventual divergence between them.
package db

import "embed"

// Migrations holds db/migrations/*.sql, applied in filename order.
//
//go:embed migrations/*.sql
var Migrations embed.FS
