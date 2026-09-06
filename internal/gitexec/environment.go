// Package gitexec defines the environment boundary for controller-owned Git
// subprocesses. Governed child processes must not inherit ambient Git routing
// overrides or unrelated controller credentials.
package gitexec

import "os"

// Environment returns the allowlisted environment for local controller-owned
// Git commands. Command-scoped configuration disables executable hooks,
// fsmonitor helpers, credential helpers, and host Git configuration.
func Environment() []string {
	keys := []string{"PATH", "LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "TZ", "TMPDIR"}
	environment := make([]string, 0, len(keys)+11)
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	environment = append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0="+os.DevNull,
		"GIT_CONFIG_KEY_1=core.fsmonitor",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=credential.helper",
		"GIT_CONFIG_VALUE_2=",
	)
	return environment
}
