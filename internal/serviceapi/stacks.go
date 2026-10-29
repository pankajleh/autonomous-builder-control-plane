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

var stackVersion = regexp.MustCompile(`^\d{1,2}(?:\.\d{1,2})?$`)

// validVersions reports pinned versions of the right shape: at most eight, each a stack's name and a version like the
// profiles' ("21", "3.12").
func validVersions(versions map[string]string) bool {
	if len(versions) > maxJobStacks {
		return false
	}
	for stack, version := range versions {
		if !stackName.MatchString(stack) || !stackVersion.MatchString(version) {
			return false
		}
	}
	return true
}

// VersionsDigestPart is what pinned versions add to a request's digest: nothing when there are none; else each
// "stack=version" in the order of the alphabet.
func VersionsDigestPart(versions map[string]string) []string {
	if len(versions) == 0 {
		return nil
	}
	out := make([]string, 0, len(versions))
	for stack, version := range versions {
		out = append(out, "version:"+stack+"="+version)
	}
	sort.Strings(out)
	return out
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
