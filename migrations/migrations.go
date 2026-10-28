// Package migrations embeds the versioned SQL migrations (goose format) and
// declares which phase of the expand/contract change each one belongs to.
package migrations

import "embed"

// FS holds every migration file. It is embedded in the binary so the
// migration Job runs exactly the SQL that was reviewed and linted.
//
//go:embed *.sql
var FS embed.FS

// Phase versions. Each release pins the schema version it needs.
const (
	VersionBaseline = 1 // app v1
	VersionExpand   = 3 // app v2 and v3: new columns, sync trigger, index
	VersionContract = 6 // contract release: NOT NULL, drop "name"
)

// RetiredBefore lists, per migration version, the app versions that must no
// longer be running when that migration is applied. The migrator checks the
// app_instances heartbeats before it runs a guarded migration.
var RetiredBefore = map[int64][]string{
	6: {"v1", "v2"},
}
