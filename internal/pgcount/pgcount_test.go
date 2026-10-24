package pgcount

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// BF-06a: the counts are read with bounds, and a backup names its biggest tables in name order.
func TestCountsAreReadWithBoundsAndTheBiggestTablesNamedInOrder(t *testing.T) {
	counts, err := Parse(` {"public.orders":1204,"public.customers":318,"public.autobuild_migrations":3} ` + "\n")
	if err != nil || len(counts) != 3 || counts["public.orders"] != 1204 {
		t.Fatal(counts, err)
	}
	if tables, rows := Totals(counts); tables != 3 || rows != 1525 {
		t.Fatal(tables, rows)
	}
	top := Top(counts)
	if len(top) != 3 || top[0].Table != "public.autobuild_migrations" || top[2] != (serviceapi.TableRowsV1{Table: "public.orders", Rows: 1204}) || serviceapi.ValidateTableRows(top) != nil {
		t.Fatal(top)
	}
	for _, bad := range []string{"", "[]", `{"public.x":-1}`, `{"":1}`, `{"` + strings.Repeat("x", MaxTableName+1) + `":1}`, `{"public.x":"one"}`} {
		if _, err := Parse(bad); err != ErrCounts {
			t.Fatalf("%q was taken", bad)
		}
	}
	many := map[string]int64{}
	for i := 0; i < serviceapi.BackupCountLimit+10; i++ {
		many[fmt.Sprintf("public.t%03d", i)] = int64(i)
	}
	top = Top(many)
	if len(top) != serviceapi.BackupCountLimit || top[0].Table != "public.t010" || top[len(top)-1].Table != "public.t029" || serviceapi.ValidateTableRows(top) != nil {
		t.Fatalf("the biggest %d, in name order: %v ... %v", len(top), top[0], top[len(top)-1])
	}
	if Top(nil) != nil {
		t.Fatal("no tables named for none")
	}
	long := map[string]int64{"public." + strings.Repeat("x", serviceapi.BackupCountNameBytes): 9, "public.short": 1}
	if top := Top(long); len(top) != 1 || top[0].Table != "public.short" {
		t.Fatal("a long name is counted but not named", top)
	}
}
