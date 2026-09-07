// Package githublifecycle freezes the network-free contracts used by the
// GitHub pull-request, CI, merge, and post-merge lifecycle.
//
// Provider data is untrusted. Callers construct bounded immutable snapshots,
// then compare them with an Authority before using them as evidence. The
// package deliberately contains no HTTP client, credential, token, URL
// fragment, shell command, state transition, or implicit retry behavior.
//
// A remote write that may have been submitted is ambiguous when its response
// is lost, including cancellation or deadline expiry. Default retry authority
// is therefore zero until an explicit reconciliation proves that the prior
// write did not take effect.
package githublifecycle
