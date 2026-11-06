// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"testing"

	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// riverMigrations is the last of River's own migrations that production's
// schema has (framy.river_migration, 8 since 31 October 2026).
const riverMigrations = 8

// MigrateRiver applies every migration of the River framy is built with,
// and the test bot runs dev on production's schema. An upgrade of River
// that brings a migration would reach production's tables while production
// runs the River it was released with (docs/design.md, Stacks), so the
// upgrade stops here until someone has read the migration.
func TestRiverBringsNoMigrationProductionLacks(t *testing.T) {
	m, err := rivermigrate.New(riverpgxv5.New(nil), &rivermigrate.Config{Schema: Schema})
	if err != nil {
		t.Fatal(err)
	}
	all := m.AllVersions()
	if len(all) == 0 {
		t.Fatal("River lists no migrations")
	}
	last := all[len(all)-1]
	if last.Version != riverMigrations {
		t.Fatalf("this River goes up to its migration %03d (%s), and production's schema has up to %03d. "+
			"The test bot would apply it to production's schema while production runs the River it was released with. "+
			"Read the migration: if the released River works on top of it, raise riverMigrations; if not, the test bot "+
			"stays on the release before it until production runs a River that needs it (docs/design.md, Stacks).",
			last.Version, last.Name, riverMigrations)
	}
}
