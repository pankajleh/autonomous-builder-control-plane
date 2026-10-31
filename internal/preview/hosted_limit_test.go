package preview

import "testing"

// The limit of hosted keys running at once is the pilot's 5 unless abcp serve sets another, from 1 to 200.
func TestHostedRunningLimitIsSetWithinItsBounds(t *testing.T) {
	h := &Hosting{}
	if got := h.runningLimit(); got != MaxHostedRunning {
		t.Fatalf("default limit = %d, want %d", got, MaxHostedRunning)
	}
	h.SetMaxRunning(20)
	if got := h.runningLimit(); got != 20 {
		t.Fatalf("limit after 20 = %d", got)
	}
	for _, bad := range []int{0, -1, 201} {
		h.SetMaxRunning(bad)
		if got := h.runningLimit(); got != MaxHostedRunning {
			t.Fatalf("limit after %d = %d, want the default %d", bad, got, MaxHostedRunning)
		}
	}
}
