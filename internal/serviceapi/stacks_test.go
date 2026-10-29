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
