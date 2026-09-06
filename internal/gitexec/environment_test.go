package gitexec

import (
	"strings"
	"testing"
)

func TestEnvironmentExcludesAmbientGitOverridesAndSecrets(t *testing.T) {
	t.Setenv("GIT_DIR", "/attacker/repository.git")
	t.Setenv("GIT_WORK_TREE", "/attacker/worktree")
	t.Setenv("GIT_CONFIG_COUNT", "99")
	t.Setenv("ABCP_CONTROLLER_SECRET", "secret")

	values := make(map[string]string)
	for _, item := range Environment() {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			t.Fatalf("invalid environment entry %q", item)
		}
		values[key] = value
	}
	for _, forbidden := range []string{"GIT_DIR", "GIT_WORK_TREE", "ABCP_CONTROLLER_SECRET"} {
		if _, ok := values[forbidden]; ok {
			t.Fatalf("controller Git environment inherited %s", forbidden)
		}
	}
	if values["GIT_CONFIG_COUNT"] != "3" || values["GIT_CONFIG_VALUE_1"] != "false" {
		t.Fatalf("controller Git safety configuration is incomplete: %#v", values)
	}
}
