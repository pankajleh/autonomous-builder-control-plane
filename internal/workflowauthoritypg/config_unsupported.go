//go:build !linux

package workflowauthoritypg

func readProtectedConfiguration(string, int) ([]byte, error) {
	return nil, errConfiguration
}
