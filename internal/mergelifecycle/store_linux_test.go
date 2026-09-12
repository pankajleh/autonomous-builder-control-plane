//go:build linux

package mergelifecycle

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPublicationRecoveryAfterAmbiguousFileSync(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := newDurableStore(root, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	attempt, err := store.openAttempt(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer attempt.close()
	original := store.syncFile
	failed := false
	store.syncFile = func(file *os.File) error {
		if !failed {
			failed = true
			return errors.New("injected ambiguous fsync")
		}
		return original(file)
	}
	record := []byte(`{"schema":"merge-commit-preparation-v1","recipe_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","result_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","evidence_refs":[{"uri":"evidence/prepare","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","kind":"prepare"}]}`)
	if _, _, err := attempt.publish("commit-preparation.json", record); err == nil {
		t.Fatal("injected ambiguous fsync unexpectedly succeeded")
	}
	store.syncFile = original
	stored, _, err := attempt.publish("commit-preparation.json", record)
	if err != nil {
		t.Fatalf("exact publication recovery failed: %v", err)
	}
	if !bytes.Equal(stored, record) {
		t.Fatal("publication recovery changed bytes")
	}
	entries, err := os.ReadDir(attempt.temporary)
	if err != nil || len(entries) != 0 {
		t.Fatalf("recovered publication left temporary files: %v %#v", err, entries)
	}
}

func TestProductionLimitsIdentityIsStable(t *testing.T) {
	first := ProductionLimitsCanonicalJSON()
	second := ProductionLimitsCanonicalJSON()
	if len(first) == 0 || !bytes.Equal(first, second) || ProductionLimitsSHA256() != digest(first) {
		t.Fatal("production limit identity is empty or unstable")
	}
	first[0] ^= 1
	if bytes.Equal(first, ProductionLimitsCanonicalJSON()) {
		t.Fatal("production limits returned mutable canonical bytes")
	}
}

func TestCumulativeCounterLimitsExactAndPlusOne(t *testing.T) {
	limits := productionLimits()
	exact := Counters{Schema: "merge-counters-v1", Sequence: 1, PreSubmitCalls: limits.preSubmitCalls,
		CommitSubmissions: limits.commitSubmissions, TargetSubmissions: limits.targetSubmissions,
		PostMergeCalls: limits.postMergeCalls, ReconciliationRounds: limits.reconciliationRounds,
		ReconciliationCalls: limits.reconciliationCalls, TotalProviderCalls: limits.providerCalls,
		CumulativeRequestBytes: limits.cumulativeRequestBytes, CumulativeHeaderBytes: limits.cumulativeHeaderBytes,
		CumulativeCompressedBytes: limits.cumulativeCompressedBytes, CumulativeDecompressedBytes: limits.cumulativeDecompressedBytes,
		CumulativeCallNanos: int64(limits.cumulativeProviderTime), LastInvocationNanos: int64(limits.invocationTimeout)}
	if err := exact.validate(limits); err != nil {
		t.Fatalf("exact cumulative limits failed: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Counters)
	}{
		{"pre-submit calls", func(c *Counters) { c.PreSubmitCalls++ }},
		{"commit submissions", func(c *Counters) { c.CommitSubmissions++ }},
		{"target submissions", func(c *Counters) { c.TargetSubmissions++ }},
		{"post-merge calls", func(c *Counters) { c.PostMergeCalls++ }},
		{"reconciliation rounds", func(c *Counters) { c.ReconciliationRounds++ }},
		{"reconciliation calls", func(c *Counters) { c.ReconciliationCalls++ }},
		{"provider calls", func(c *Counters) { c.TotalProviderCalls++ }},
		{"request bytes", func(c *Counters) { c.CumulativeRequestBytes++ }},
		{"header bytes", func(c *Counters) { c.CumulativeHeaderBytes++ }},
		{"compressed response bytes", func(c *Counters) { c.CumulativeCompressedBytes++ }},
		{"decompressed response bytes", func(c *Counters) { c.CumulativeDecompressedBytes++ }},
		{"provider time", func(c *Counters) { c.CumulativeCallNanos++ }},
		{"invocation time", func(c *Counters) { c.LastInvocationNanos++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := exact
			test.mutate(&value)
			if err := value.validate(limits); err == nil {
				t.Fatal("limit+1 counter was accepted")
			}
		})
	}
}

func TestStateNamespaceReplacementFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := newDurableStore(root, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	channels := root + "/channels"
	if err := os.Rename(channels, channels+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(channels, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"state":"replacement"}`)
	if _, err := store.appendChannel("terminal-intents", "replacement", digest(data), data, MaxTerminalRecordBytes); err == nil {
		t.Fatal("replacement channel namespace was followed")
	}
	entries, err := os.ReadDir(channels)
	if err != nil || len(entries) != 0 {
		t.Fatalf("replacement namespace was mutated: entries=%v err=%v", entries, err)
	}
}
