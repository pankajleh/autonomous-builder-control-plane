package githublifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	ExpectedMergeContentPolicy = "phase3-ready-tree-v1"
	phase3DecisionKind         = "serial-integration-gate-decision"
	maxPhase3EvidenceBytes     = 16 * 1024 * 1024
)

// PinnedGitIdentity identifies the exact local Git executable used to derive
// expected merge content. It is observed by the controller, never asserted by
// a provider.
type PinnedGitIdentity struct {
	path         string
	version      string
	binarySHA256 string
}

func (g PinnedGitIdentity) Path() string         { return g.path }
func (g PinnedGitIdentity) Version() string      { return g.version }
func (g PinnedGitIdentity) BinarySHA256() string { return g.binarySHA256 }
func (g PinnedGitIdentity) valid() bool {
	return filepath.IsAbs(g.path) && filepath.Clean(g.path) == g.path &&
		validText(g.version, 256, false) && len(g.binarySHA256) == sha256.Size*2 && isLowerHex(g.binarySHA256)
}

// ExpectedMergeContent is controller-owned, copy-safe authority derived from
// exact Phase 3 READY_FOR_MERGE evidence and a local integrated commit.
type ExpectedMergeContent struct {
	policy         string
	sourceHead     GitSHA
	sourceBase     GitSHA
	sourceEvidence ledger.EvidenceRef
	git            PinnedGitIdentity
	expectedTree   GitSHA
	canonical      []byte
	digest         string
}

func (e ExpectedMergeContent) Policy() string                                { return e.policy }
func (e ExpectedMergeContent) SourceIntegratedHeadSHA() GitSHA               { return e.sourceHead }
func (e ExpectedMergeContent) SourceBaselineSHA() GitSHA                     { return e.sourceBase }
func (e ExpectedMergeContent) ExpectedResultTreeSHA() GitSHA                 { return e.expectedTree }
func (e ExpectedMergeContent) GitIdentity() PinnedGitIdentity                { return e.git }
func (e ExpectedMergeContent) SourceIntegrationEvidence() ledger.EvidenceRef { return e.sourceEvidence }
func (e ExpectedMergeContent) CanonicalJSON() []byte                         { return append([]byte(nil), e.canonical...) }
func (e ExpectedMergeContent) SHA256() string                                { return e.digest }
func (e ExpectedMergeContent) MarshalJSON() ([]byte, error) {
	if !e.valid() {
		return nil, errors.New("expected merge content is incomplete")
	}
	return e.CanonicalJSON(), nil
}
func (e ExpectedMergeContent) valid() bool {
	if e.policy != ExpectedMergeContentPolicy || !e.sourceHead.valid() || !e.sourceBase.valid() || !e.expectedTree.valid() || !e.git.valid() ||
		!validEvidenceRef(e.sourceEvidence) || len(e.canonical) == 0 || len(e.digest) != sha256.Size*2 || !isLowerHex(e.digest) {
		return false
	}
	canonical, digest, err := canonicalExpectedContent(e.policy, e.sourceHead, e.sourceBase, e.sourceEvidence, e.git, e.expectedTree)
	return err == nil && bytes.Equal(canonical, e.canonical) && digest == e.digest
}

// ParseCanonicalExpectedMergeContent strictly rehydrates controller-owned
// expected content for durable merge-input recovery.
func ParseCanonicalExpectedMergeContent(data []byte) (ExpectedMergeContent, error) {
	var wire struct {
		DerivationPolicy string             `json:"derivation_policy"`
		SourceHead       string             `json:"source_integrated_head_sha"`
		SourceBase       string             `json:"source_baseline_sha"`
		SourceEvidence   ledger.EvidenceRef `json:"source_integration_evidence"`
		Git              struct {
			Path         string `json:"path"`
			Version      string `json:"version"`
			BinarySHA256 string `json:"binary_sha256"`
		} `json:"pinned_git"`
		ExpectedTree string `json:"expected_result_tree_sha"`
	}
	if err := strictDecode(data, &wire); err != nil {
		return ExpectedMergeContent{}, fmt.Errorf("decode expected merge content: %w", err)
	}
	head, err := NewGitSHA(wire.SourceHead)
	if err != nil {
		return ExpectedMergeContent{}, err
	}
	base, err := NewGitSHA(wire.SourceBase)
	if err != nil {
		return ExpectedMergeContent{}, err
	}
	tree, err := NewGitSHA(wire.ExpectedTree)
	if err != nil {
		return ExpectedMergeContent{}, err
	}
	git := PinnedGitIdentity{wire.Git.Path, wire.Git.Version, wire.Git.BinarySHA256}
	canonical, digest, err := canonicalExpectedContent(wire.DerivationPolicy, head, base, wire.SourceEvidence, git, tree)
	if err != nil {
		return ExpectedMergeContent{}, err
	}
	value := ExpectedMergeContent{wire.DerivationPolicy, head, base, wire.SourceEvidence, git, tree, canonical, digest}
	if !value.valid() {
		return ExpectedMergeContent{}, errors.New("expected merge content is invalid")
	}
	if err := requireCanonical(data, canonical); err != nil {
		return ExpectedMergeContent{}, err
	}
	return value, nil
}

// DeriveExpectedMergeContent verifies the immutable Phase 3 decision, pins the
// exact Git executable, and resolves only the exact accepted integrated commit
// under the controller's replacement-ref-resistant Git environment.
func DeriveExpectedMergeContent(ctx context.Context, repositoryPath, evidenceRoot string, integratedHead, expectedBaseTip GitSHA,
	readyEvidence ledger.EvidenceRef, gitExecutable string) (ExpectedMergeContent, error) {
	if ctx == nil {
		return ExpectedMergeContent{}, errors.New("derivation context is required")
	}
	if !integratedHead.valid() || !expectedBaseTip.valid() {
		return ExpectedMergeContent{}, errors.New("Phase 3 integrated head or baseline is invalid")
	}
	if repositoryPath == "" || !filepath.IsAbs(repositoryPath) || filepath.Clean(repositoryPath) != repositoryPath {
		return ExpectedMergeContent{}, errors.New("integration repository must be an absolute clean path")
	}
	info, err := os.Stat(repositoryPath)
	if err != nil || !info.IsDir() {
		return ExpectedMergeContent{}, errors.New("integration repository is unavailable")
	}
	decision, err := evidence.ReadVerifiedLocal(evidenceRoot, readyEvidence, maxPhase3EvidenceBytes)
	if err != nil {
		return ExpectedMergeContent{}, fmt.Errorf("verify Phase 3 READY_FOR_MERGE evidence: %w", err)
	}
	if readyEvidence.Kind != phase3DecisionKind {
		return ExpectedMergeContent{}, errors.New("Phase 3 evidence is not a READY_FOR_MERGE gate decision")
	}
	if err := verifyReadyDecision(decision, integratedHead.String(), expectedBaseTip.String()); err != nil {
		return ExpectedMergeContent{}, err
	}
	git, err := observeGitIdentity(ctx, gitExecutable)
	if err != nil {
		return ExpectedMergeContent{}, err
	}
	objectType, err := runPinnedGit(ctx, repositoryPath, git.path, "cat-file", "-t", integratedHead.String())
	if err != nil || objectType != "commit\n" {
		return ExpectedMergeContent{}, errors.New("Phase 3 integrated head is not an exact local commit")
	}
	treeText, err := runPinnedGit(ctx, repositoryPath, git.path, "rev-parse", "--verify", "--end-of-options", integratedHead.String()+"^{tree}")
	if err != nil {
		return ExpectedMergeContent{}, fmt.Errorf("derive Phase 3 integrated tree: %w", err)
	}
	tree, err := NewGitSHA(strings.TrimSuffix(treeText, "\n"))
	if err != nil || treeText != tree.String()+"\n" {
		return ExpectedMergeContent{}, errors.New("Git returned an invalid or ambiguous integrated tree")
	}
	gitAfter, err := observeGitIdentity(ctx, git.path)
	if err != nil || gitAfter != git {
		return ExpectedMergeContent{}, errors.New("pinned Git executable changed during derivation")
	}
	canonical, digest, err := canonicalExpectedContent(ExpectedMergeContentPolicy, integratedHead, expectedBaseTip, readyEvidence, git, tree)
	if err != nil {
		return ExpectedMergeContent{}, err
	}
	return ExpectedMergeContent{ExpectedMergeContentPolicy, integratedHead, expectedBaseTip, readyEvidence, git, tree, canonical, digest}, nil
}

func verifyReadyDecision(data []byte, integratedHead, expectedBaseTip string) error {
	var decision struct {
		State    string `json:"state"`
		Combined struct {
			Target struct {
				HeadSHA     string `json:"head_sha"`
				Integration struct {
					IntegratedHeadSHA string `json:"integrated_head_sha"`
					BaselineSHA       string `json:"baseline_sha"`
				} `json:"integration"`
			} `json:"target"`
		} `json:"combined"`
	}
	if err := json.Unmarshal(data, &decision); err != nil {
		return fmt.Errorf("decode Phase 3 READY_FOR_MERGE evidence: %w", err)
	}
	if decision.State != "READY_FOR_MERGE" || decision.Combined.Target.HeadSHA != integratedHead ||
		decision.Combined.Target.Integration.IntegratedHeadSHA != integratedHead ||
		decision.Combined.Target.Integration.BaselineSHA != expectedBaseTip {
		return errors.New("Phase 3 evidence does not bind READY_FOR_MERGE to the exact integrated head")
	}
	return nil
}

func observeGitIdentity(ctx context.Context, executable string) (PinnedGitIdentity, error) {
	if executable == "" {
		return PinnedGitIdentity{}, errors.New("explicit Git executable is required")
	}
	path, err := exec.LookPath(executable)
	if err != nil {
		return PinnedGitIdentity{}, fmt.Errorf("resolve Git executable: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return PinnedGitIdentity{}, fmt.Errorf("resolve absolute Git executable: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return PinnedGitIdentity{}, fmt.Errorf("canonicalize Git executable: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return PinnedGitIdentity{}, fmt.Errorf("read Git executable identity: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 128*1024*1024 {
		file.Close()
		return PinnedGitIdentity{}, errors.New("Git executable must be a bounded regular file")
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(hasher, io.LimitReader(file, 128*1024*1024+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return PinnedGitIdentity{}, fmt.Errorf("hash Git executable: %w", errors.Join(copyErr, closeErr))
	}
	version, err := runPinnedGit(ctx, "", path, "--version")
	if err != nil {
		return PinnedGitIdentity{}, fmt.Errorf("observe Git version: %w", err)
	}
	version = strings.TrimSuffix(version, "\n")
	identity := PinnedGitIdentity{path, version, hex.EncodeToString(hasher.Sum(nil))}
	if !identity.valid() {
		return PinnedGitIdentity{}, errors.New("observed Git identity is invalid")
	}
	return identity, nil
}

func runPinnedGit(ctx context.Context, directory, executable string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, executable, append([]string{"--no-replace-objects"}, args...)...)
	command.Dir = directory
	command.Env = append(gitexec.Environment(), "LC_ALL=C")
	var stdout, stderr boundedGitBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("pinned Git failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stderr.Len() != 0 || stdout.overflow {
		return "", errors.New("pinned Git returned unexpected or oversized output")
	}
	return stdout.String(), nil
}

type boundedGitBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedGitBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := 4097 - b.Len()
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = b.Buffer.Write(data[:remaining])
	}
	if b.Len() > 4096 || remaining < len(data) {
		b.overflow = true
	}
	return written, nil
}

func canonicalExpectedContent(policy string, head, base GitSHA, ref ledger.EvidenceRef, git PinnedGitIdentity, tree GitSHA) ([]byte, string, error) {
	return canonicalJSON(struct {
		DerivationPolicy string             `json:"derivation_policy"`
		SourceHead       string             `json:"source_integrated_head_sha"`
		SourceBase       string             `json:"source_baseline_sha"`
		SourceEvidence   ledger.EvidenceRef `json:"source_integration_evidence"`
		Git              struct {
			Path         string `json:"path"`
			Version      string `json:"version"`
			BinarySHA256 string `json:"binary_sha256"`
		} `json:"pinned_git"`
		ExpectedTree string `json:"expected_result_tree_sha"`
	}{policy, head.String(), base.String(), ref, struct {
		Path         string `json:"path"`
		Version      string `json:"version"`
		BinarySHA256 string `json:"binary_sha256"`
	}{git.path, git.version, git.binarySHA256}, tree.String()})
}

func validEvidenceRef(ref ledger.EvidenceRef) bool {
	return validText(ref.URI, 4096, false) && validOpaqueID(ref.Kind, 256) && len(ref.SHA256) == sha256.Size*2 && isLowerHex(ref.SHA256)
}
