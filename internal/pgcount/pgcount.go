// Package pgcount counts the rows of every ordinary table in a Postgres database, for a backup to say what it holds
// (Repo C design notes BF14_PROVIDER_NEUTRAL_DEPLOYMENT §11 and BF06_YOUR_APPS_DATA §4.1). The query is fixed; nothing
// from an app or a person is put into it, and its answer is read with bounds.
package pgcount

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Query answers each ordinary table's rows, by schema and name as quote_ident gives them, as one JSON object
// (query_to_xml counts each table; run it in one REPEATABLE READ transaction for one snapshot).
const Query = `SELECT coalesce(json_object_agg(t.name, t.n ORDER BY t.name), '{}') FROM (SELECT quote_ident(s.nspname) || '.' || ` +
	`quote_ident(c.relname) AS name, (xpath('/row/n/text()', query_to_xml(format('SELECT count(*) AS n FROM %I.%I', s.nspname, ` +
	`c.relname), false, true, '')))[1]::text::bigint AS n FROM pg_class c JOIN pg_namespace s ON s.oid = c.relnamespace ` +
	`WHERE c.relkind = 'r' AND s.nspname <> 'information_schema' AND s.nspname NOT LIKE 'pg\_%') t`

// A database's tables, as a backup counts them, and the length of a table's name as quote_ident gives it with its schema.
const (
	MaxTables    = 2000
	MaxTableName = 260
)

// ErrCounts is an answer that is not a count of tables.
var ErrCounts = errors.New("the rows could not be counted")

// Parse reads Query's answer: a JSON object of table names and counts.
func Parse(text string) (map[string]int64, error) {
	counts := map[string]int64{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &counts); err != nil || len(counts) > MaxTables {
		return nil, ErrCounts
	}
	for name, n := range counts {
		if name == "" || len(name) > MaxTableName || n < 0 {
			return nil, ErrCounts
		}
	}
	return counts, nil
}

// Totals is a database's tables and rows.
func Totals(counts map[string]int64) (int, int64) {
	var rows int64
	for _, n := range counts {
		rows += n
	}
	return len(counts), rows
}

// Top is the serviceapi.BackupCountLimit tables with the most rows, in name order: what a backup's answer names. A
// table whose name is longer than serviceapi.BackupCountNameBytes is counted in the totals but not named.
func Top(counts map[string]int64) []serviceapi.TableRowsV1 {
	all := make([]serviceapi.TableRowsV1, 0, len(counts))
	for name, n := range counts {
		if len(name) <= serviceapi.BackupCountNameBytes {
			all = append(all, serviceapi.TableRowsV1{Table: name, Rows: n})
		}
	}
	if len(all) == 0 {
		return nil
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Rows != all[j].Rows {
			return all[i].Rows > all[j].Rows
		}
		return all[i].Table < all[j].Table
	})
	if len(all) > serviceapi.BackupCountLimit {
		all = all[:serviceapi.BackupCountLimit]
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Table < all[j].Table })
	return all
}
