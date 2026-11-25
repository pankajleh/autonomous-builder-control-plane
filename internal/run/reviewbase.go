package run

import (
	"context"
	"regexp"
)

// reviewBaseBranch is the branch a walled build's review diffs against when the build starts from a library template
// (Repo C design note TEMPLATE_FIRST_BUILDS.md, the review base). Ralphex reviews `git diff <base-ref>...HEAD`, and its
// review agents each read that diff first; from the start commit that is the whole template copy (530 files and 40,254
// added lines for a full-stack salon on 2026-11-24), so a settings-only change of 57 lines cost a review of the copy.
// The branch starts at the start commit, so a review reads what it read before, and moves to the commit that holds the
// copy once ABCP has found that commit in the brought-back branch (requireTemplateCopy, as acceptance checks it), so
// the review reads only what the build did after the copy. Acceptance still checks the whole branch from the start.
const reviewBaseBranch = "abcp-review-base"

var fullCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// startReviewBase points the review base at the commit it last pointed at, or the start commit, before Ralphex starts.
// It answers false when the base cannot be written: the build then runs with Ralphex's own base, as before.
func (w *walledRun) startReviewBase() bool {
	if w.reviewBase == "" {
		return false
	}
	w.mu.Lock()
	at := w.reviewBaseAt
	w.mu.Unlock()
	if at == "" {
		at = w.startSHA
	}
	if err := pointRunBranch(w.folder, w.reviewBase, at); err != nil {
		w.evidence["review_base_unavailable"] = err.Error()
		return false
	}
	w.evidence["review_base"] = w.reviewBase
	return true
}

// moveReviewBase moves the review base to the commit that holds the template copy once the brought-back branch has one.
// It reads only the product repository and the template mirror, never the run's repository; a base it cannot move
// stays where it was, and one already on the branch's history is left alone.
func (w *walledRun) moveReviewBase(ctx context.Context) {
	if w.reviewBase == "" || w.template == nil {
		return
	}
	w.mu.Lock()
	head, at := w.fetched, w.reviewBaseAt
	w.mu.Unlock()
	if head == "" || head == w.startSHA {
		return
	}
	if at != "" {
		if _, err := gitOutput(ctx, w.product, "merge-base", "--is-ancestor", at, head); err == nil {
			return
		}
	}
	commit, err := requireTemplateCopy(ctx, w.product, w.startSHA, head, *w.template)
	if err != nil || commit == at || !fullCommitSHA.MatchString(commit) {
		return
	}
	if err := pointRunBranch(w.folder, w.reviewBase, commit); err != nil {
		return
	}
	w.mu.Lock()
	w.reviewBaseAt = commit
	w.mu.Unlock()
}
