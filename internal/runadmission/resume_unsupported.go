//go:build !linux

package runadmission

import (
	"context"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// ResumeRun is unsupported off Linux, where the protected-directory admission
// substrate and the repository execution lease are unavailable.
func (c *Controller) ResumeRun(context.Context, string) error {
	return serviceapi.ErrAdmissionUnavailable
}
