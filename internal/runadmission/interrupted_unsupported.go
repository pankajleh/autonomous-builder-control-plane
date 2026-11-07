//go:build !linux

package runadmission

import "context"

// Pass is unsupported off Linux, where the protected-directory admission substrate is unavailable.
func (r *InterruptedResumer) Pass(context.Context) int { return 0 }
