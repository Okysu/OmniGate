// Package migrations embeds the SQL schema migrations (forward-only in
// production; goose Down sections exist for development).
//
// postgres/ and sqlite/ hold one file per schema version with identical
// version numbers (ADR-0009). goose records only version numbers, so a
// database migrated before the files moved into postgres/ sees no change.
package migrations

import "embed"

//go:embed postgres/*.sql sqlite/*.sql
var FS embed.FS
