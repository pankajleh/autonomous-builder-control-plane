package applog

import "testing"

// Anything that reads as a secret, a sign-in token, an email address or a
// phone number is replaced; what tells what went wrong stays.
func TestRedactHidesSecretsAndPeopleButKeepsTheError(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Error: connect ECONNREFUSED postgres://app_crm:5f2b9c0e7a@autobuild-postgres:5432/app_crm",
			"Error: connect ECONNREFUSED postgres://[hidden]@autobuild-postgres:5432/app_crm"},
		{`error: password authentication failed for user "app_crm"`, `error: password authentication failed for user "app_crm"`},
		{"Authorization: Bearer abcdefgh12345678", "Authorization: [hidden] [token]"},
		{`failed with {"apiKey": "live-1234", "user":"x"}`, `failed with {"apiKey": "[hidden]", "user":"x"}`},
		{"DB_PASSWORD=hunter2hunter2 refused", "DB_PASSWORD=[hidden] refused"},
		{"bad token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U given",
			"bad token [token] given"},
		{"Razorpay refused rzp_live_ABCDEFGH12345678", "Razorpay refused [key]"},
		{"Slack failed xoxb-1234567890-abcdefghij", "Slack failed [key]"},
		{"Error: no booking for asha.k@example.co.in", "Error: no booking for [email]"},
		{"failed to send to +91 98765 43210 and 9876543210", "failed to send to [number] and [number]"},
		{"2026-10-24 14:52:31 Error: listen EADDRINUSE: address already in use :::8080",
			"2026-10-24 14:52:31 Error: listen EADDRINUSE: address already in use :::8080"},
		{"2026-10-24 9876543210 failed", "2026-10-24 [number] failed"},
		{"    at Object.<anonymous> (/source/node_modules/fastify-plugin/lib/getPluginName.js:12:15)",
			"    at Object.<anonymous> (/source/node_modules/fastify-plugin/lib/getPluginName.js:12:15)"},
		{"webhook signature mismatch 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			"webhook signature mismatch [secret]"},
		{"refused dGhpcyBpcyBhIHZlcnkgbG9uZyBTZWNyZXQgdmFsdWUgMTIzNDU2Nzg5MA== now", "refused [secret] now"},
		{"Error: order 12345 failed at 10.0.0.2:5432", "Error: order 12345 failed at 10.0.0.2:5432"},
		{"a\x00b Error", "ab Error"},
	} {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}
