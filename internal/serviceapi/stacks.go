package serviceapi

import (
	"regexp"
	"sort"
	"strings"
)

// maxJobStacks bounds a request's stacks.
const maxJobStacks = 8

var stackName = regexp.MustCompile(`^[a-z]{1,16}$`)

// validStacks reports a job's stacks of the right shape: at most eight names, each once. Which of them the profiles serve
// is the job's to answer, not the request's.
func validStacks(stacks []string) bool {
	if len(stacks) > maxJobStacks {
		return false
	}
	seen := map[string]bool{}
	for _, s := range stacks {
		if !stackName.MatchString(s) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}

// StacksDigestPart is what a job's stacks add to its request's digest: nothing when it names none, so a request from
// before stacks were named digests as it did; else the names in order of the alphabet, so their order does not matter.
func StacksDigestPart(stacks []string) []string {
	if len(stacks) == 0 {
		return nil
	}
	sorted := append([]string(nil), stacks...)
	sort.Strings(sorted)
	return []string{"stacks:" + strings.Join(sorted, ",")}
}
