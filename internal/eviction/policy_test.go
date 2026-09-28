package eviction

import (
	"reflect"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func finishedAgo(run, repo string, finished, used time.Duration, bytes int64) Candidate {
	c := Candidate{Run: run, Repository: repo, Worktree: "/wt/" + run, Terminal: true, TerminalAt: now.Add(-finished), Bytes: bytes}
	if used >= 0 {
		c.LastAccess = now.Add(-used)
	}
	return c
}

func reasons(ds []Decision) map[string]string {
	out := map[string]string{}
	for _, d := range ds {
		out[d.Run] = d.Reason
	}
	return out
}

func TestDecideAppliesAgeIdleAndProtection(t *testing.T) {
	protected := finishedAgo("previewing", "r", 30*24*time.Hour, 0, 1)
	protected.Protected = true
	running := Candidate{Run: "running", Repository: "r", Terminal: false, Bytes: 1}
	tests := []struct {
		name  string
		given []Candidate
		want  map[string]string
	}{
		{"fresh finished run is kept", []Candidate{finishedAgo("a", "r", time.Hour, -1, 1)}, map[string]string{}},
		{"idle since finish", []Candidate{finishedAgo("a", "r", 25*time.Hour, -1, 1)}, map[string]string{"a": ReasonIdle}},
		{"recent use extends idle", []Candidate{finishedAgo("a", "r", 3*24*time.Hour, time.Hour, 1)}, map[string]string{}},
		{"max age wins over recent use", []Candidate{finishedAgo("a", "r", 8*24*time.Hour, time.Minute, 1)}, map[string]string{"a": ReasonMaxAge}},
		{"protected and unfinished are never evicted", []Candidate{protected, running}, map[string]string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reasons(DefaultPolicy().Decide(tc.given, now)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decisions = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDecideQuotaEvictsLeastRecentlyUsedPerRepository(t *testing.T) {
	const gib = int64(1) << 30
	hot := finishedAgo("hot", "repo-a", 2*time.Hour, time.Minute, 2*gib)
	warm := finishedAgo("warm", "repo-a", 3*time.Hour, time.Hour, 2*gib)
	cold := finishedAgo("cold", "repo-a", 4*time.Hour, 2*time.Hour, 2*gib)
	live := finishedAgo("live", "repo-a", 5*time.Hour, 3*time.Hour, gib)
	live.Protected = true
	other := finishedAgo("other", "repo-b", 5*time.Hour, 3*time.Hour, 4*gib)
	got := reasons(DefaultPolicy().Decide([]Candidate{hot, warm, cold, live, other}, now))
	// repo-a uses 7 GiB (the protected 1 GiB counts but cannot go): evicting the
	// coldest 2 GiB reaches 5 GiB. repo-b is under its own quota.
	if want := map[string]string{"cold": ReasonQuota}; !reflect.DeepEqual(got, want) {
		t.Fatalf("quota decisions = %v, want %v", got, want)
	}
	if got := reasons((Policy{QuotaBytes: 3 * gib}).Decide([]Candidate{hot, warm, cold, live}, now)); !reflect.DeepEqual(got, map[string]string{"cold": ReasonQuota, "warm": ReasonQuota}) {
		t.Fatalf("tighter quota decisions = %v", got)
	}
}

func TestDecideZeroDisablesEachRule(t *testing.T) {
	old := finishedAgo("old", "r", 30*24*time.Hour, -1, int64(10)<<30)
	if got := (Policy{}).Decide([]Candidate{old}, now); len(got) != 0 {
		t.Fatalf("disabled policy evicted %v", got)
	}
	if got := reasons((Policy{Idle: time.Hour}).Decide([]Candidate{old}, now)); got["old"] != ReasonIdle {
		t.Fatalf("idle-only policy = %v", got)
	}
}
