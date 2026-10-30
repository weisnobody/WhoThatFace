// Package schemas exposes the canonical database migration files to the
// application without duplicating them under an implementation package.
package schemas

import "embed"

// Migrations contains the ordered SQL migrations used by the database layer.
//
//go:embed migrations/*.sql
var Migrations embed.FS
