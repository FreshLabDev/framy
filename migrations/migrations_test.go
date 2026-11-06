// SPDX-License-Identifier: Apache-2.0
package migrations

import (
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The test bot runs dev on production's schema, so a migration reaches
// production's tables while production still runs the released binary
// (docs/design.md, Stacks). It has to leave that binary working: new
// tables, new columns that are NULL or have a default, new indexes. One
// that has to do more says so in a line of its own,
//
//	-- not additive: <why the released binary still works, or which
//	-- release production runs first>
//
// and goes to production in that order. The changelog names it anyway.

// beforeTheRule are the migrations from before this check that do more
// than add, and why each left the released binary working. A new one says
// it in its own file, with the line above.
var beforeTheRule = map[string]string{
	"005_fx.sql":       "job_fx ties together the columns added with it, which the released binary did not write",
	"022_guest.sql":    "widens job.surface by 'guest': every value the released binary writes stays allowed",
	"025_settings.sql": "widens site_delivery.state by 'off'",
	"026_joined.sql":   "widens asset.kind by 'joined' and states asset_check again for it; asset_thumb_missing is made again under its name",
	"029_stack.sql":    "changes album_item's key: v0.1.0-alpha.2 went to production before the test bot moved onto the schema",
}

// lastBeforeTheRule is the last migration beforeTheRule may name.
const lastBeforeTheRule = 30

var marker = regexp.MustCompile(`(?m)^\s*--\s*not additive:\s*\S`)

func TestMigrationsAreAdditive(t *testing.T) {
	names, err := fs.Glob(FS, "*.sql")
	if err != nil || len(names) == 0 {
		t.Fatalf("migrations: %v, %v", names, err)
	}
	for _, name := range names {
		b, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatal(err)
		}
		found := notAdditive(string(b))
		_, old := beforeTheRule[name]
		switch {
		case old && len(found) == 0:
			t.Errorf("%s is only additive: take it out of beforeTheRule", name)
		case old, marker.Match(b):
		case len(found) > 0:
			t.Errorf("%s is not additive: %s. The test bot applies it to production's schema while production runs the released binary (docs/design.md, Stacks). Make it additive, or say in it why the released binary still works, or which release production runs first, in a line \"-- not additive: …\".",
				name, strings.Join(found, "; "))
		}
	}
	for name := range beforeTheRule {
		n, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil || n > lastBeforeTheRule || !slices.Contains(names, name) {
			t.Errorf("beforeTheRule names %s: only a migration up to %03d that exists; a new one says why in its own file", name, lastBeforeTheRule)
		}
	}
}

var (
	lineComment = regexp.MustCompile(`--[^\n]*`)
	// A function's body; framy's migrations quote it with $$.
	dollarBody = regexp.MustCompile(`(?s)\$\$.*?\$\$`)
	literal    = regexp.MustCompile(`'(?:[^']|'')*'`)
	space      = regexp.MustCompile(`\s+`)

	createTable = regexp.MustCompile(`^create (?:unlogged )?table (?:if not exists )?([a-z0-9_."]+)`)
	alterTable  = regexp.MustCompile(`^alter table (?:if exists )?(?:only )?([a-z0-9_."]+) (.*)$`)
	uniqueIndex = regexp.MustCompile(`^create unique index (?:concurrently )?(?:if not exists )?(?:[a-z0-9_."]+ )?on (?:only )?([a-z0-9_."]+)`)
	dropObject  = regexp.MustCompile(`^drop (table|view|materialized view|function|procedure|trigger|type|domain|schema|sequence|index|extension)\b`)
	renameOther = regexp.MustCompile(`^alter (?:index|sequence|type|view|function|schema) .* rename\b`)

	actionRename    = regexp.MustCompile(`^rename\b`)
	actionDrop      = regexp.MustCompile(`^drop\b`)
	actionType      = regexp.MustCompile(`^alter (?:column )?\S+ (?:set data )?type\b`)
	actionNotNull   = regexp.MustCompile(`^alter (?:column )?\S+ set not null\b`)
	actionNoDef     = regexp.MustCompile(`^alter (?:column )?\S+ drop (?:default|expression|identity)\b`)
	actionKey       = regexp.MustCompile(`^add (?:constraint|primary key|unique|check|foreign key|exclude)\b`)
	actionAddCol    = regexp.MustCompile(`^add (?:column )?(?:if not exists )?\S+ (.*)$`)
	notNull         = regexp.MustCompile(`\bnot null\b`)
	defaultOrGenVal = regexp.MustCompile(`\b(?:default|generated|serial|bigserial|smallserial)\b`)
)

// notAdditive lists what a migration does that the released binary may not
// survive: it drops, renames or retypes something, makes a column NOT NULL,
// takes a default away, or adds a constraint, a unique index or a NOT NULL
// column without a default to a table that already has rows.
func notAdditive(sql string) []string {
	sql = lineComment.ReplaceAllString(sql, " ")
	sql = dollarBody.ReplaceAllString(sql, "$$$$")
	sql = literal.ReplaceAllString(sql, "''")
	sql = strings.ToLower(sql)
	created := map[string]bool{}
	var found []string
	for _, stmt := range strings.Split(sql, ";") {
		stmt = strings.TrimSpace(space.ReplaceAllString(stmt, " "))
		if m := createTable.FindStringSubmatch(stmt); m != nil {
			created[m[1]] = true
			continue
		}
		if m := dropObject.FindStringSubmatch(stmt); m != nil {
			found = append(found, "drops a "+m[1])
			continue
		}
		if strings.HasPrefix(stmt, "truncate ") {
			found = append(found, "empties a table")
			continue
		}
		if renameOther.MatchString(stmt) {
			found = append(found, "renames")
			continue
		}
		if m := uniqueIndex.FindStringSubmatch(stmt); m != nil && !created[m[1]] {
			found = append(found, "adds a unique index to "+m[1])
			continue
		}
		m := alterTable.FindStringSubmatch(stmt)
		if m == nil || created[m[1]] {
			continue
		}
		table := m[1]
		for _, action := range topLevel(m[2]) {
			switch {
			case actionRename.MatchString(action):
				found = append(found, "renames in "+table)
			case actionDrop.MatchString(action):
				found = append(found, "drops from "+table+" ("+action+")")
			case actionType.MatchString(action):
				found = append(found, "changes a type in "+table)
			case actionNotNull.MatchString(action):
				found = append(found, "makes a column of "+table+" NOT NULL")
			case actionNoDef.MatchString(action):
				found = append(found, "takes a default away in "+table)
			case actionKey.MatchString(action):
				found = append(found, "adds a constraint to "+table)
			default:
				if c := actionAddCol.FindStringSubmatch(action); c != nil && notNull.MatchString(c[1]) && !defaultOrGenVal.MatchString(c[1]) {
					found = append(found, "adds a NOT NULL column without a default to "+table)
				}
			}
		}
	}
	return found
}

// topLevel splits the actions of an ALTER TABLE at the commas outside
// parentheses.
func topLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

func TestNotAdditive(t *testing.T) {
	for _, tc := range []struct {
		sql      string
		breaking bool
	}{
		{"ALTER TABLE job DROP COLUMN prompt;", true},
		{"ALTER TABLE job DROP prompt;", true},
		{"ALTER TABLE job RENAME COLUMN prompt TO text;", true},
		{"ALTER TABLE job RENAME TO jobs;", true},
		{"ALTER TABLE job ALTER COLUMN prompt TYPE varchar(100);", true},
		{"ALTER TABLE job ALTER prompt SET DATA TYPE varchar(100);", true},
		{"ALTER TABLE job ALTER COLUMN prompt SET NOT NULL;", true},
		{"ALTER TABLE job ALTER COLUMN free DROP DEFAULT;", true},
		{"ALTER TABLE job ADD COLUMN size INTEGER NOT NULL;", true},
		{"ALTER TABLE job ADD COLUMN a TEXT, ADD COLUMN b INTEGER NOT NULL;", true},
		{"ALTER TABLE job ADD CONSTRAINT job_size CHECK (size > 0);", true},
		{"ALTER TABLE job DROP CONSTRAINT job_surface_check;", true},
		{"ALTER TABLE album_item ADD PRIMARY KEY (stack, chat_id, message_id);", true},
		{"CREATE UNIQUE INDEX job_key ON job (dedupe_key);", true},
		{"DROP TABLE album_item;", true},
		{"DROP INDEX album_item_group;", true},
		{"ALTER INDEX job_key RENAME TO job_dedupe;", true},
		{"TRUNCATE album_item;", true},

		{"ALTER TABLE job ADD COLUMN size INTEGER;", false},
		{"ALTER TABLE job ADD COLUMN stack TEXT NOT NULL DEFAULT '' CHECK (stack ~ '^([a-z][a-z0-9]{0,15})?$');", false},
		{"ALTER TABLE job ADD COLUMN batch TEXT, ADD COLUMN batch_frames INTEGER[];", false},
		{"ALTER TABLE job ALTER COLUMN prompt SET DEFAULT '';", false},
		{"CREATE INDEX job_batch ON job (user_id, batch) WHERE batch IS NOT NULL;", false},
		{"CREATE TABLE stack_state (stack TEXT PRIMARY KEY, n BIGINT NOT NULL);\nALTER TABLE stack_state ADD COLUMN m INTEGER NOT NULL;\nCREATE UNIQUE INDEX s ON stack_state (m);", false},
		{"-- DROP TABLE job; once nothing reads it\nALTER TABLE job ADD COLUMN x TEXT;", false},
		{"COMMENT ON COLUMN job.prompt IS 'drop table job; rename';", false},
		{"CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$\nBEGIN\n  DROP TABLE x;\n  RETURN NEW;\nEND $$;", false},
	} {
		if got := notAdditive(tc.sql); (len(got) > 0) != tc.breaking {
			t.Errorf("%q: %v", tc.sql, got)
		}
	}
}
