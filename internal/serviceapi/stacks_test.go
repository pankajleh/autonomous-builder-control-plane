package serviceapi

import (
	"slices"
	"strings"
	"testing"
)

func TestAJobsStacksAreFewNamesEachOnceAndDigestInAnyOrder(t *testing.T) {
	stack := StackJobCreateV1{RequestID: "req-12345678", Source: "src-a", CacheKey: strings.Repeat("d", 64)}
	change := ChangeJobCreateV1{RequestID: "req-12345678", Source: "src-a", CacheKey: strings.Repeat("d", 64), Kind: "fix", Words: "fix it"}
	for _, stacks := range [][]string{nil, {}, {"python", "java", "node"}, {"go", "ruby", "php"}} {
		stack.Stacks, change.Stacks = stacks, stacks
		if !stack.Valid() || !change.Valid() {
			t.Errorf("%v refused", stacks)
		}
	}
	for _, stacks := range [][]string{{"python", "python"}, {"Go"}, {""}, {"node.js"}, {strings.Repeat("a", 17)}, {"a", "b", "c", "d", "e", "f", "g", "h", "i"}} {
		stack.Stacks, change.Stacks = stacks, stacks
		if stack.Valid() || change.Valid() {
			t.Errorf("%v accepted", stacks)
		}
	}
	if StacksDigestPart(nil) != nil || StacksDigestPart([]string{}) != nil {
		t.Fatal("no stacks changed a digest")
	}
	if a, b := StacksDigestPart([]string{"node", "go"}), StacksDigestPart([]string{"go", "node"}); !slices.Equal(a, b) || a[0] != "stacks:go,node" {
		t.Fatalf("digest parts %v %v", a, b)
	}
}

func TestPinnedVersionsAreFewAndShapedAndDigestInAnyOrder(t *testing.T) {
	job := StackJobCreateV1{RequestID: "req-12345678", Source: "src-a", CacheKey: strings.Repeat("d", 64)}
	for _, versions := range []map[string]string{nil, {}, {"java": "21"}, {"python": "3.12", "dotnet": "10.0"}} {
		job.Versions = versions
		if !job.Valid() {
			t.Errorf("%v refused", versions)
		}
	}
	for _, versions := range []map[string]string{{"Java": "21"}, {"java": "21.0.1"}, {"java": "latest"}, {"java": ""},
		{"a": "1", "b": "1", "c": "1", "d": "1", "e": "1", "f": "1", "g": "1", "h": "1", "i": "1"}} {
		job.Versions = versions
		if job.Valid() {
			t.Errorf("%v accepted", versions)
		}
	}
	if VersionsDigestPart(nil) != nil || !slices.Equal(VersionsDigestPart(map[string]string{"node": "22", "java": "21"}), []string{"version:java=21", "version:node=22"}) {
		t.Fatal("versions digest")
	}
}

func TestTargetsAreForAnAssessmentOrAMigrationAlone(t *testing.T) {
	job := ChangeJobCreateV1{RequestID: "req-12345678", Source: "src-a", CacheKey: strings.Repeat("d", 64), Kind: "assess", Words: "what would it take"}
	for _, targets := range []map[string]string{nil, {"java": "21"}, {"python": "3.13", "node": "24"}} {
		job.Targets = targets
		if !job.Valid() {
			t.Errorf("%v refused", targets)
		}
	}
	for _, targets := range []map[string]string{{"java": "latest"}, {"Java": "21"}} {
		job.Targets = targets
		if job.Valid() {
			t.Errorf("%v accepted", targets)
		}
	}
	job.Kind, job.Targets = "migrate", map[string]string{"java": "21"}
	if !job.Valid() {
		t.Fatal("a migration's step with targets refused")
	}
	job.Kind = "fix"
	if job.Valid() {
		t.Fatal("a fix with targets accepted")
	}
	job.Kind = "upgrade"
	if job.Targets = nil; job.Valid() {
		t.Fatal("an unknown kind accepted")
	}
}
