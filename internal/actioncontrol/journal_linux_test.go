//go:build linux

package actioncontrol

import (
	"bufio"
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
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

var journalTestTime = time.Date(2026, 9, 14, 6, 0, 0, 0, time.UTC)

func TestJournalDurableIdempotencyAndOutcomeProgression(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	input := journalReceiptInput("request-1", digestText("request-one"))
	binderCalls := 0
	receipt, created, err := journal.CreateReceipt(context.Background(), input, func(sequence uint64) (AdmissionBinding, error) {
		binderCalls++
		if sequence != 1 {
			t.Fatalf("sequence = %d", sequence)
		}
		return AdmissionBinding{OwnerLeaseID: digestText("lease")}, nil
	})
	if err != nil || !created || binderCalls != 1 {
		t.Fatalf("first receipt = %+v, created=%v calls=%d err=%v", receipt, created, binderCalls, err)
	}
	replayed, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{}, errors.New("idempotent replay must not rebind")
	})
	if err != nil || created || !reflect.DeepEqual(replayed, receipt) || binderCalls != 1 {
		t.Fatalf("replayed receipt = %+v, created=%v calls=%d err=%v", replayed, created, binderCalls, err)
	}
	conflict := input
	conflict.RequestSHA256 = digestText("different-intent")
	if _, _, err := journal.CreateReceipt(context.Background(), conflict, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting request error = %v", err)
	}
	claim, created, err := journal.CreateClaim(context.Background(), input.RunID, receipt.OperationID, receipt.OwnerLeaseID, CancelEventDomain)
	if err != nil || !created || claim.ControllerEventID != DeterministicEventID(CancelEventDomain, receipt.OperationID) {
		t.Fatalf("claim = %+v, created=%v err=%v", claim, created, err)
	}
	requestEventID := claim.ControllerEventID
	cancelledEventID := "cancelled-event-1"
	if _, err := journal.AppendOutcome(context.Background(), input.RunID, receipt.OperationID, StatusApplied, []string{requestEventID, cancelledEventID}, ""); err != nil {
		t.Fatal(err)
	}
	operation, err := journal.Read(context.Background(), input.RunID, receipt.OperationID)
	if err != nil || operation.Status != StatusApplied || len(operation.AuthoritativeEventIDs()) != 2 {
		t.Fatalf("operation = %+v, err=%v", operation, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "actions", input.RunID, "00000001.jsonl"))
	if err != nil || !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatalf("durable segment = %d bytes, err=%v", len(data), err)
	}
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var compact bytes.Buffer
		if json.Compact(&compact, line) != nil || !bytes.Equal(compact.Bytes(), line) {
			t.Fatalf("non-canonical journal line: %q", line)
		}
	}
}

func TestJournalRequestIdentityIsGlobalAcrossRunsAndProcesses(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenWithClock(root, func() time.Time { return journalTestTime.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	start := make(chan struct{})
	type result struct {
		receipt ActionReceiptV1
		err     error
	}
	results := make(chan result, 2)
	for index, journal := range []*Journal{first, second} {
		go func(index int, journal *Journal) {
			<-start
			input := journalReceiptInputForRun(fmt.Sprintf("global-run-%d", index+1))
			input.RequestID = "service-global-request"
			input.RequestSHA256 = digestText(input.RunID)
			receipt, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("lease-" + input.RunID)}, nil
			})
			results <- result{receipt: receipt, err: err}
		}(index, journal)
	}
	close(start)
	var admitted ActionReceiptV1
	conflicts := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			admitted = result.receipt
		} else if errors.Is(result.err, ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("global request race error = %v", result.err)
		}
	}
	if admitted.OperationID == "" || conflicts != 1 {
		t.Fatalf("global request race admitted=%+v conflicts=%d", admitted, conflicts)
	}
	otherRun := "global-run-1"
	if admitted.RunID == otherRun {
		otherRun = "global-run-2"
	}
	operation, err := second.ReadByRequest(context.Background(), otherRun, admitted.PrincipalID, admitted.RequestID)
	if err != nil || operation.Receipt.OperationID != admitted.OperationID || operation.Receipt.RunID != admitted.RunID {
		t.Fatalf("global request lookup = %+v, err=%v", operation, err)
	}
}

func TestJournalIssuanceHistoryAdvancesWithinOneCanonicalShard(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenWithClock(root, func() time.Time { return journalTestTime.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	inputA := journalReceiptInputForRun("same-shard-run-a")
	lookupA := LookupKey(inputA.PrincipalID, inputA.RequestID)
	inputB := journalReceiptInputForRun("same-shard-run-b")
	for candidate := 0; ; candidate++ {
		inputB.RequestID = fmt.Sprintf("same-shard-request-%d", candidate)
		inputB.RequestSHA256 = digestText(inputB.RequestID)
		if lookupB := LookupKey(inputB.PrincipalID, inputB.RequestID); lookupB != lookupA && lookupB[:2] == lookupA[:2] {
			break
		}
	}
	receiptA, created, err := first.CreateReceipt(context.Background(), inputA, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("same-shard-lease-a")}, nil
	})
	if err != nil || !created {
		t.Fatalf("first same-shard receipt=%+v created=%v err=%v", receiptA, created, err)
	}
	receiptB, created, err := second.CreateReceipt(context.Background(), inputB, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("same-shard-lease-b")}, nil
	})
	if err != nil || !created {
		t.Fatalf("second same-shard receipt=%+v created=%v err=%v", receiptB, created, err)
	}
	authorityData, found, err := fgetRootXattr(first.rootFD, journalShardAuthorityXattr(lookupA[:2]))
	if err != nil || !found {
		t.Fatalf("same-shard authority found=%v err=%v", found, err)
	}
	authority, _, established, err := decodeRootGenerationAuthority(authorityData, journalShardAuthorityKind, first.rootDev, first.rootIno)
	if err != nil || !established || authority.IssuanceCount != 2 || !validDigest(authority.IssuanceFinalRecordSHA256) {
		t.Fatalf("same-shard authority=%+v established=%v err=%v", authority, established, err)
	}
	history, err := os.ReadFile(filepath.Join(root, "actions", requestIndexDirectory, lookupA[:2], requestIssuanceHistoryName))
	if err != nil || len(bytes.Split(bytes.TrimSuffix(history, []byte("\n")), []byte("\n"))) != 2 {
		t.Fatalf("same-shard issuance history bytes=%d err=%v", len(history), err)
	}
	replayed, created, err := second.CreateReceipt(context.Background(), inputA, nil)
	if err != nil || created || !reflect.DeepEqual(replayed, receiptA) {
		t.Fatalf("same-shard replay=%+v created=%v err=%v", replayed, created, err)
	}
	conflict := inputB
	conflict.RequestSHA256 = digestText("same-shard-conflicting-intent")
	if _, created, err := first.CreateReceipt(context.Background(), conflict, nil); !errors.Is(err, ErrConflict) || created {
		t.Fatalf("same-shard conflict created=%v err=%v", created, err)
	}
}

func TestJournalRequestLookupIsDirectAndDoesNotMutateMisses(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	input := journalReceiptInputForRun("indexed-run")
	receipt, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("indexed-lease")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A malformed unrelated run journal would have made the former global
	// scanner fail. Direct lookup must inspect only the key shard and indexed
	// exact run journal.
	unrelated := filepath.Join(root, "actions", "unrelated-run")
	if err := os.Mkdir(unrelated, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unrelated, "not-a-segment"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation, err := journal.ReadByRequest(context.Background(), "caller-run-is-not-used-for-search", input.PrincipalID, input.RequestID)
	if err != nil || operation.Receipt.OperationID != receipt.OperationID {
		t.Fatalf("direct lookup = %+v, err=%v", operation, err)
	}

	missingRun := "request-miss-must-not-create-run"
	if _, err := journal.ReadByRequest(context.Background(), missingRun, input.PrincipalID, "missing-request"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing direct lookup error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "actions", missingRun)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("request miss mutated run storage: %v", err)
	}
}

func TestJournalIssuanceLookupIsBoundedToCanonicalShard(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	inputA := journalReceiptInputForRun("bounded-issuance-run-a")
	lookupA := LookupKey(inputA.PrincipalID, inputA.RequestID)
	inputB := journalReceiptInputForRun("bounded-issuance-run-b")
	for candidate := 0; ; candidate++ {
		inputB.RequestID = fmt.Sprintf("bounded-issuance-request-%d", candidate)
		inputB.RequestSHA256 = digestText(inputB.RequestID)
		if LookupKey(inputB.PrincipalID, inputB.RequestID)[:2] != lookupA[:2] {
			break
		}
	}
	receiptA, _, err := journal.CreateReceipt(context.Background(), inputA, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("bounded-issuance-lease-a")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.CreateReceipt(context.Background(), inputB, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("bounded-issuance-lease-b")}, nil
	}); err != nil {
		t.Fatal(err)
	}
	lookupB := LookupKey(inputB.PrincipalID, inputB.RequestID)
	identityB := filepath.Join(root, "actions", requestIndexDirectory, lookupB[:2], lookupB+".json")
	if err := os.WriteFile(identityB, []byte("corrupt-unrelated-shard"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation, err := journal.ReadByRequest(context.Background(), inputA.RunID, inputA.PrincipalID, inputA.RequestID)
	if err != nil || operation.Receipt.OperationID != receiptA.OperationID {
		t.Fatalf("canonical-shard lookup operation=%+v err=%v", operation, err)
	}
}

func TestJournalImmutableRequestIndexSafety(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"mode", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, path string) {
			t.Helper()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(path))), "replacement-index")
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"hard-link", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Link(path, path+".second-link"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			input := journalReceiptInputForRun("safe-index-run")
			if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("safe-index-lease")}, nil
			}); err != nil {
				t.Fatal(err)
			}
			lookup := LookupKey(input.PrincipalID, input.RequestID)
			path := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2], lookup+".json")
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("index metadata mode=%v err=%v", info.Mode().Perm(), err)
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
				t.Fatalf("published index link count = %v", info.Sys())
			}
			test.mutate(t, path)
			if _, err := journal.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("unsafe immutable index error = %v", err)
			}
		})
	}
}

func TestJournalIssuedRequestIdentityDeletionFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	input := journalReceiptInputForRun("issued-identity-deletion-run")
	receipt, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("issued-identity-deletion-lease")}, nil
	})
	if err != nil || !created {
		_ = journal.Close()
		t.Fatalf("initial receipt=%+v created=%v err=%v", receipt, created, err)
	}
	lookup := LookupKey(input.PrincipalID, input.RequestID)
	shard := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2])
	identityPath := filepath.Join(shard, lookup+".json")
	historyPath := filepath.Join(shard, requestIssuanceHistoryName)
	shardInfoBefore, err := os.Stat(shard)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	shardStatBefore, ok := shardInfoBefore.Sys().(*syscall.Stat_t)
	if !ok {
		_ = journal.Close()
		t.Fatal("request-index shard has no stat identity")
	}
	anchorPath := filepath.Join(filepath.Dir(shard), "."+lookup[:2]+".identity.json")
	anchorBefore, err := os.ReadFile(anchorPath)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	historyBefore, err := os.ReadFile(historyPath)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	authorityName := journalShardAuthorityXattr(lookup[:2])
	authorityBefore, found, err := fgetRootXattr(journal.rootFD, authorityName)
	if err != nil || !found {
		_ = journal.Close()
		t.Fatalf("issuance authority found=%v err=%v", found, err)
	}
	segmentPath := filepath.Join(root, "actions", input.RunID, "00000001.jsonl")
	segmentBefore, err := os.ReadFile(segmentPath)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(identityPath); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	binderCalls := 0
	if _, created, err := reopened.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{}, nil
	}); !errors.Is(err, ErrIntegrity) || created {
		t.Fatalf("exact deleted identity replay created=%v err=%v", created, err)
	}
	conflict := input
	conflict.RunID = "issued-identity-deletion-conflict"
	conflict.RequestSHA256 = digestText("issued-identity-deletion-conflict")
	if _, created, err := reopened.CreateReceipt(context.Background(), conflict, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{}, nil
	}); !errors.Is(err, ErrIntegrity) || created {
		t.Fatalf("conflicting deleted identity reuse created=%v err=%v", created, err)
	}
	if binderCalls != 0 {
		t.Fatalf("deleted identity reached admission binding %d times", binderCalls)
	}
	if _, err := reopened.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("deleted identity read error = %v", err)
	}
	historyAfter, err := os.ReadFile(historyPath)
	if err != nil || !bytes.Equal(historyAfter, historyBefore) {
		t.Fatalf("deletion mutated issuance history: before=%d after=%d err=%v", len(historyBefore), len(historyAfter), err)
	}
	authorityAfter, found, err := fgetRootXattr(reopened.rootFD, authorityName)
	if err != nil || !found || !bytes.Equal(authorityAfter, authorityBefore) {
		t.Fatalf("deletion mutated issuance authority: found=%v err=%v", found, err)
	}
	segmentAfter, err := os.ReadFile(segmentPath)
	if err != nil || !bytes.Equal(segmentAfter, segmentBefore) {
		t.Fatalf("deletion mutated journal: before=%d after=%d err=%v", len(segmentBefore), len(segmentAfter), err)
	}
	if _, err := os.Stat(filepath.Join(root, "actions", conflict.RunID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("conflicting reuse created a second run: %v", err)
	}
	shardInfoAfter, err := os.Stat(shard)
	if err != nil {
		t.Fatalf("cannot restat shard after deletion: %v", err)
	}
	shardStatAfter, ok := shardInfoAfter.Sys().(*syscall.Stat_t)
	if !ok || shardStatAfter.Dev != shardStatBefore.Dev || shardStatAfter.Ino != shardStatBefore.Ino {
		t.Fatalf("deletion changed shard generation: before=%+v after=%+v err=%v", shardStatBefore, shardStatAfter, err)
	}
	anchorAfter, err := os.ReadFile(anchorPath)
	if err != nil || !bytes.Equal(anchorAfter, anchorBefore) {
		t.Fatalf("deletion changed shard anchor: before=%d after=%d err=%v", len(anchorBefore), len(anchorAfter), err)
	}
}

func TestJournalIssuedRequestIdentityReplacementFailsClosed(t *testing.T) {
	for _, replacement := range []string{"new-inode-same-bytes", "same-inode-different-intent"} {
		t.Run(replacement, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			input := journalReceiptInputForRun("issued-identity-replacement-run")
			receipt, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("issued-identity-replacement-lease")}, nil
			})
			if err != nil || !created {
				_ = journal.Close()
				t.Fatalf("initial replacement receipt=%+v created=%v err=%v", receipt, created, err)
			}
			lookup := LookupKey(input.PrincipalID, input.RequestID)
			shard := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2])
			identityPath := filepath.Join(shard, lookup+".json")
			original, err := os.ReadFile(identityPath)
			if err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			historyPath := filepath.Join(shard, requestIssuanceHistoryName)
			historyBefore, err := os.ReadFile(historyPath)
			if err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			authorityName := journalShardAuthorityXattr(lookup[:2])
			authorityBefore, found, err := fgetRootXattr(journal.rootFD, authorityName)
			if err != nil || !found {
				_ = journal.Close()
				t.Fatalf("replacement authority found=%v err=%v", found, err)
			}
			anchorPath := filepath.Join(filepath.Dir(shard), "."+lookup[:2]+".identity.json")
			anchorBefore, err := os.ReadFile(anchorPath)
			if err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			shardInfoBefore, err := os.Stat(shard)
			if err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			shardStatBefore, ok := shardInfoBefore.Sys().(*syscall.Stat_t)
			if !ok {
				_ = journal.Close()
				t.Fatal("request-index shard has no stat identity")
			}
			segmentPath := filepath.Join(root, "actions", input.RunID, "00000001.jsonl")
			segmentBefore, err := os.ReadFile(segmentPath)
			if err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			var installed []byte
			if replacement == "new-inode-same-bytes" {
				if err := os.Rename(identityPath, filepath.Join(root, "replaced-request-identity")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(identityPath, original, 0o600); err != nil {
					t.Fatal(err)
				}
				installed = original
			} else {
				var altered requestIdentityV1
				if json.Unmarshal(original, &altered) != nil {
					t.Fatal("cannot decode original identity")
				}
				altered.RequestSHA256 = digestText("attacker-replacement-intent")
				data, err := json.Marshal(altered)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(identityPath, data, 0o600); err != nil {
					t.Fatal(err)
				}
				installed = data
			}

			reopened, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			binderCalls := 0
			if _, created, err := reopened.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{}, nil
			}); !errors.Is(err, ErrIntegrity) || created {
				t.Fatalf("exact replacement replay created=%v err=%v", created, err)
			}
			replay := ReceiptReplayInput{PrincipalID: input.PrincipalID, PrincipalType: input.PrincipalType, RequestID: input.RequestID,
				RequestSHA256: input.RequestSHA256, Action: input.Action, RunID: input.RunID, AttemptID: input.AttemptID}
			if _, err := reopened.ReplayReceipt(context.Background(), replay); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("attacker replacement replay error = %v", err)
			}
			if _, err := reopened.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("replacement read error = %v", err)
			}
			conflict := input
			conflict.RunID = "issued-identity-replacement-conflict"
			conflict.RequestSHA256 = digestText("issued-identity-replacement-conflict")
			if _, created, err := reopened.CreateReceipt(context.Background(), conflict, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{}, nil
			}); !errors.Is(err, ErrIntegrity) || created {
				t.Fatalf("replacement reuse created=%v err=%v", created, err)
			}
			if binderCalls != 0 {
				t.Fatalf("replacement reached admission binding %d times", binderCalls)
			}
			historyAfter, err := os.ReadFile(historyPath)
			if err != nil || !bytes.Equal(historyAfter, historyBefore) {
				t.Fatalf("replacement mutated issuance history: before=%d after=%d err=%v", len(historyBefore), len(historyAfter), err)
			}
			authorityAfter, found, err := fgetRootXattr(reopened.rootFD, authorityName)
			if err != nil || !found || !bytes.Equal(authorityAfter, authorityBefore) {
				t.Fatalf("replacement mutated issuance authority: found=%v err=%v", found, err)
			}
			attackerAfter, err := os.ReadFile(identityPath)
			if err != nil || !bytes.Equal(attackerAfter, installed) {
				t.Fatalf("replacement bytes changed: installed=%d after=%d err=%v", len(installed), len(attackerAfter), err)
			}
			segmentAfter, err := os.ReadFile(segmentPath)
			if err != nil || !bytes.Equal(segmentAfter, segmentBefore) {
				t.Fatalf("replacement mutated journal: before=%d after=%d err=%v", len(segmentBefore), len(segmentAfter), err)
			}
			if _, err := os.Stat(filepath.Join(root, "actions", conflict.RunID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("replacement reuse created a second run: %v", err)
			}
			shardInfoAfter, err := os.Stat(shard)
			if err != nil {
				t.Fatalf("cannot restat shard after replacement: %v", err)
			}
			shardStatAfter, ok := shardInfoAfter.Sys().(*syscall.Stat_t)
			if !ok || shardStatAfter.Dev != shardStatBefore.Dev || shardStatAfter.Ino != shardStatBefore.Ino {
				t.Fatalf("replacement changed shard generation: before=%+v after=%+v err=%v", shardStatBefore, shardStatAfter, err)
			}
			anchorAfter, err := os.ReadFile(anchorPath)
			if err != nil || !bytes.Equal(anchorAfter, anchorBefore) {
				t.Fatalf("replacement changed shard anchor: before=%d after=%d err=%v", len(anchorBefore), len(anchorAfter), err)
			}
		})
	}
}

func TestJournalOrphanRequestIdentityIsNotAdopted(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	input := journalReceiptInputForRun("orphan-identity-run")
	lookup := LookupKey(input.PrincipalID, input.RequestID)
	guard, err := journal.acquireActions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	shardFD, err := guard.openIndexShard(lookup[:2], true)
	if err != nil {
		_ = guard.close()
		t.Fatal(err)
	}
	if err := syscall.Close(shardFD); err != nil {
		_ = guard.close()
		t.Fatal(err)
	}
	if err := guard.close(); err != nil {
		t.Fatal(err)
	}
	shard := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2])
	historyPath := filepath.Join(shard, requestIssuanceHistoryName)
	historyBefore, err := os.ReadFile(historyPath)
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	authorityName := journalShardAuthorityXattr(lookup[:2])
	authorityBefore, found, err := fgetRootXattr(journal.rootFD, authorityName)
	if err != nil || !found {
		_ = journal.Close()
		t.Fatalf("orphan authority found=%v err=%v", found, err)
	}
	receipt := ActionReceiptV1{Kind: "ActionReceiptV1", SchemaVersion: JournalSchemaVersion,
		LookupKeySHA256: lookup, OperationID: operationIDForLookup(lookup), PrincipalID: input.PrincipalID, PrincipalType: input.PrincipalType,
		RequestID: input.RequestID, RequestSHA256: input.RequestSHA256, Action: input.Action, RunID: input.RunID, AttemptID: input.AttemptID,
		ExpectedState: input.ExpectedState, ExpectedRevision: input.ExpectedRevision, Reason: input.Reason, Payload: input.Payload,
		AdmittedStateTransitionID: input.StateTransitionID, ReceivedAt: canonicalTime(journalTestTime)}
	data, err := json.Marshal(identityFromReceipt(receipt))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shard, lookup+".json"), data, 0o600); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	binderCalls := 0
	if _, created, err := reopened.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{}, nil
	}); !errors.Is(err, ErrIntegrity) || created {
		t.Fatalf("orphan identity adoption created=%v err=%v", created, err)
	}
	if binderCalls != 0 {
		t.Fatalf("orphan identity reached admission binding %d times", binderCalls)
	}
	replay := ReceiptReplayInput{PrincipalID: input.PrincipalID, PrincipalType: input.PrincipalType, RequestID: input.RequestID,
		RequestSHA256: input.RequestSHA256, Action: input.Action, RunID: input.RunID, AttemptID: input.AttemptID}
	if _, err := reopened.ReplayReceipt(context.Background(), replay); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("orphan replay error = %v", err)
	}
	historyAfter, err := os.ReadFile(historyPath)
	if err != nil || !bytes.Equal(historyAfter, historyBefore) {
		t.Fatalf("orphan identity mutated history: before=%d after=%d err=%v", len(historyBefore), len(historyAfter), err)
	}
	authorityAfter, found, err := fgetRootXattr(reopened.rootFD, authorityName)
	if err != nil || !found || !bytes.Equal(authorityAfter, authorityBefore) {
		t.Fatalf("orphan identity mutated authority: found=%v err=%v", found, err)
	}
	if orphanAfter, err := os.ReadFile(filepath.Join(shard, lookup+".json")); err != nil || !bytes.Equal(orphanAfter, data) {
		t.Fatalf("orphan identity bytes changed: before=%d after=%d err=%v", len(data), len(orphanAfter), err)
	}
	before := countJournalHelperDescriptors(t)
	for attempt := 0; attempt < 8; attempt++ {
		if _, _, err := reopened.CreateReceipt(context.Background(), input, nil); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("repeated orphan rejection %d error = %v", attempt, err)
		}
	}
	if after := countJournalHelperDescriptors(t); after != before {
		t.Fatalf("orphan rejections leaked descriptors: before=%d after=%d", before, after)
	}
}

func TestJournalIssuanceHistoryGenerationIsNonReissuable(t *testing.T) {
	for _, attack := range []string{"loss", "replacement"} {
		t.Run(attack, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			input := journalReceiptInputForRun("issuance-history-generation-run")
			if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("issuance-history-generation-lease")}, nil
			}); err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			lookup := LookupKey(input.PrincipalID, input.RequestID)
			shard := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2])
			historyPath := filepath.Join(shard, requestIssuanceHistoryName)
			historyBefore, err := os.ReadFile(historyPath)
			if err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			authorityName := journalShardAuthorityXattr(lookup[:2])
			authorityBefore, found, err := fgetRootXattr(journal.rootFD, authorityName)
			if err != nil || !found {
				_ = journal.Close()
				t.Fatalf("history-generation authority found=%v err=%v", found, err)
			}
			anchorPath := filepath.Join(filepath.Dir(shard), "."+lookup[:2]+".identity.json")
			anchorBefore, err := os.ReadFile(anchorPath)
			if err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			originalPath := historyPath + "." + attack
			if attack == "loss" {
				if err := os.Rename(historyPath, originalPath); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(historyPath, originalPath); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(historyPath, historyBefore, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			assertRejectedOpen := func() {
				t.Helper()
				fresh, err := Open(root)
				if fresh != nil {
					_ = fresh.Close()
					t.Fatal("fresh Open adopted a replacement issuance-history generation")
				}
				if !errors.Is(err, ErrIntegrity) {
					t.Fatalf("issuance-history %s error = %v", attack, err)
				}
			}
			assertRejectedOpen()
			before := countJournalHelperDescriptors(t)
			for attempt := 0; attempt < 8; attempt++ {
				assertRejectedOpen()
			}
			if after := countJournalHelperDescriptors(t); after != before {
				t.Fatalf("failed history-generation opens leaked descriptors: before=%d after=%d", before, after)
			}
			originalAfter, err := os.ReadFile(originalPath)
			if err != nil || !bytes.Equal(originalAfter, historyBefore) {
				t.Fatalf("failed opens mutated original history: before=%d after=%d err=%v", len(historyBefore), len(originalAfter), err)
			}
			if attack == "loss" {
				if _, err := os.Stat(historyPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed opens recreated lost history: %v", err)
				}
			} else if replacementAfter, err := os.ReadFile(historyPath); err != nil || !bytes.Equal(replacementAfter, historyBefore) {
				t.Fatalf("failed opens mutated replacement history: before=%d after=%d err=%v", len(historyBefore), len(replacementAfter), err)
			}
			rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
			if err != nil {
				t.Fatal(err)
			}
			authorityAfter, found, authorityErr := fgetRootXattr(rootFD, authorityName)
			closeErr := syscall.Close(rootFD)
			if authorityErr != nil || closeErr != nil || !found || !bytes.Equal(authorityAfter, authorityBefore) {
				t.Fatalf("failed opens mutated history authority: found=%v authorityErr=%v closeErr=%v", found, authorityErr, closeErr)
			}
			anchorAfter, err := os.ReadFile(anchorPath)
			if err != nil || !bytes.Equal(anchorAfter, anchorBefore) {
				t.Fatalf("failed opens mutated shard anchor: before=%d after=%d err=%v", len(anchorBefore), len(anchorAfter), err)
			}
		})
	}
}

func TestJournalRecoversCompletePendingIndexPublication(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	input := journalReceiptInputForRun("pending-index-publication-run")
	lookup := LookupKey(input.PrincipalID, input.RequestID)
	receipt := ActionReceiptV1{Kind: "ActionReceiptV1", SchemaVersion: JournalSchemaVersion,
		LookupKeySHA256: lookup, OperationID: operationIDForLookup(lookup), PrincipalID: input.PrincipalID, PrincipalType: input.PrincipalType,
		RequestID: input.RequestID, RequestSHA256: input.RequestSHA256, Action: input.Action, RunID: input.RunID, AttemptID: input.AttemptID,
		ExpectedState: input.ExpectedState, ExpectedRevision: input.ExpectedRevision, Reason: input.Reason,
		Payload: input.Payload, AdmittedStateTransitionID: input.StateTransitionID, OwnerLeaseID: digestText("pending-index-publication-lease"),
		ReceivedAt: canonicalTime(journalTestTime)}
	identity := identityFromReceipt(receipt)
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := journal.acquireActions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	shardFD, err := guard.openIndexShard(lookup[:2], true)
	if err != nil {
		_ = guard.close()
		t.Fatal(err)
	}
	if err := syscall.Close(shardFD); err != nil {
		_ = guard.close()
		t.Fatal(err)
	}
	if err := guard.close(); err != nil {
		t.Fatal(err)
	}
	shard := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2])
	pending := filepath.Join(shard, "."+lookup+".pending")
	if err := os.WriteFile(pending, data, 0o600); err != nil {
		t.Fatal(err)
	}
	guard, err = journal.acquireActions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	shardFD, err = guard.openIndexShard(lookup[:2], false)
	if err != nil {
		_ = guard.close()
		t.Fatal(err)
	}
	state, history, _, err := guard.loadRequestIssuanceState(shardFD, lookup[:2])
	if err != nil {
		_ = syscall.Close(shardFD)
		_ = guard.close()
		t.Fatal(err)
	}
	if err := history.Close(); err != nil {
		_ = syscall.Close(shardFD)
		_ = guard.close()
		t.Fatal(err)
	}
	pendingInfo, err := os.Stat(pending)
	if err != nil {
		_ = syscall.Close(shardFD)
		_ = guard.close()
		t.Fatal(err)
	}
	pendingStat, ok := pendingInfo.Sys().(*syscall.Stat_t)
	if !ok {
		_ = syscall.Close(shardFD)
		_ = guard.close()
		t.Fatal("pending identity has no stat identity")
	}
	committed, err := guard.commitRequestIssuance(shardFD, lookup[:2], state, identity, data, *pendingStat)
	if err != nil || !committed {
		_ = syscall.Close(shardFD)
		_ = guard.close()
		t.Fatalf("pending issuance commit committed=%v err=%v", committed, err)
	}
	if err := syscall.Close(shardFD); err != nil {
		_ = guard.close()
		t.Fatal(err)
	}
	if err := guard.close(); err != nil {
		t.Fatal(err)
	}
	replay := ReceiptReplayInput{PrincipalID: input.PrincipalID, PrincipalType: input.PrincipalType, RequestID: input.RequestID,
		RequestSHA256: input.RequestSHA256, Action: input.Action, RunID: input.RunID, AttemptID: input.AttemptID}
	operation, err := journal.ReplayReceipt(context.Background(), replay)
	if err != nil || operation.Receipt.OperationID != identity.OperationID || operation.Receipt.ReceivedAt != identity.ReceivedAt {
		t.Fatalf("pending publication recovery operation=%+v err=%v", operation, err)
	}
	if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending index publication survived recovery: %v", err)
	}
	info, err := os.Stat(filepath.Join(shard, lookup+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
		t.Fatalf("recovered index link count = %v", info.Sys())
	}
}

func TestJournalIssuanceStagesIdentityBeforeHistoryCommit(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	defaults := defaultJournalIO()
	staged := false
	historyStarted := false
	journal.io.syncDir = func(object string, fd int) error {
		if object == "request-index-stage" {
			staged = true
		}
		return defaults.syncDir(object, fd)
	}
	journal.io.write = func(object string, file *os.File, data []byte) error {
		if object == "request-issuance-history" {
			historyStarted = true
			if !staged {
				return errors.New("issuance history preceded durable staging")
			}
		}
		return defaults.write(object, file, data)
	}
	input := journalReceiptInputForRun("issuance-stage-order-run")
	if _, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("issuance-stage-order-lease")}, nil
	}); err != nil || !created {
		t.Fatalf("staged issuance created=%v err=%v", created, err)
	}
	if !staged || !historyStarted {
		t.Fatalf("issuance ordering staged=%v historyStarted=%v", staged, historyStarted)
	}
}

func TestJournalIssuanceStageSyncFailureLeavesNoCommittedIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	defaults := defaultJournalIO()
	failed := false
	journal.io.syncDir = func(object string, fd int) error {
		if object == "request-index-stage" && !failed {
			failed = true
			return syscall.EIO
		}
		return defaults.syncDir(object, fd)
	}
	input := journalReceiptInputForRun("issuance-stage-failure-run")
	if _, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("issuance-stage-failure-lease")}, nil
	}); err == nil || created {
		t.Fatalf("stage-sync failure created=%v err=%v", created, err)
	}
	if !failed {
		t.Fatal("stage-sync fault was not reached")
	}
	journal.io = defaults
	lookup := LookupKey(input.PrincipalID, input.RequestID)
	shard := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2])
	history, err := os.ReadFile(filepath.Join(shard, requestIssuanceHistoryName))
	if err != nil || len(history) != 0 {
		t.Fatalf("failed stage committed history bytes=%d err=%v", len(history), err)
	}
	if _, err := os.Stat(filepath.Join(shard, "."+lookup+".pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed stage left pending identity: %v", err)
	}
	conflict := input
	conflict.RunID = "issuance-stage-failure-retry-run"
	conflict.RequestSHA256 = digestText("issuance-stage-failure-retry")
	if _, created, err := journal.CreateReceipt(context.Background(), conflict, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("issuance-stage-failure-retry-lease")}, nil
	}); err != nil || !created {
		t.Fatalf("uncommitted key retry created=%v err=%v", created, err)
	}
}

func TestJournalIssuanceHistoryFailuresRollbackBeforeAuthority(t *testing.T) {
	for _, fault := range []string{"append", "file-sync"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			defaults := defaultJournalIO()
			failed := false
			journal.io.write = func(object string, file *os.File, data []byte) error {
				if object == "request-issuance-history" && fault == "append" && !failed {
					failed = true
					if err := defaults.write(object, file, data); err != nil {
						return err
					}
					return syscall.EIO
				}
				return defaults.write(object, file, data)
			}
			journal.io.syncFile = func(object string, file *os.File) error {
				if object == "request-issuance-history" && fault == "file-sync" && !failed {
					failed = true
					if err := defaults.syncFile(object, file); err != nil {
						return err
					}
					return syscall.EIO
				}
				return defaults.syncFile(object, file)
			}
			input := journalReceiptInputForRun("issuance-history-failure-" + fault)
			if _, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("issuance-history-failure-lease-" + fault)}, nil
			}); err == nil || created {
				t.Fatalf("history %s failure created=%v err=%v", fault, created, err)
			}
			if !failed {
				t.Fatalf("history %s fault was not reached", fault)
			}
			journal.io = defaults
			lookup := LookupKey(input.PrincipalID, input.RequestID)
			shard := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2])
			history, err := os.ReadFile(filepath.Join(shard, requestIssuanceHistoryName))
			if err != nil || len(history) != 0 {
				t.Fatalf("history %s rollback bytes=%d err=%v", fault, len(history), err)
			}
			if _, err := os.Stat(filepath.Join(shard, "."+lookup+".pending")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("history %s rollback left pending identity: %v", fault, err)
			}
			conflict := input
			conflict.RunID += "-retry"
			conflict.RequestSHA256 = digestText("issuance-history-failure-retry-" + fault)
			if _, created, err := journal.CreateReceipt(context.Background(), conflict, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("issuance-history-failure-retry-lease-" + fault)}, nil
			}); err != nil || !created {
				t.Fatalf("history %s uncommitted retry created=%v err=%v", fault, created, err)
			}
		})
	}
}

func TestJournalIssuanceAuthoritySyncAmbiguityRetainsExactPendingIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	defaults := defaultJournalIO()
	failed := false
	journal.io.syncDir = func(object string, fd int) error {
		if object == "request-issuance-authority" && !failed {
			failed = true
			return syscall.EIO
		}
		return defaults.syncDir(object, fd)
	}
	input := journalReceiptInputForRun("issuance-authority-ambiguity-run")
	binderCalls := 0
	if _, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{OwnerLeaseID: digestText("issuance-authority-ambiguity-lease")}, nil
	}); err == nil || created {
		_ = journal.Close()
		t.Fatalf("authority-sync ambiguity created=%v err=%v", created, err)
	}
	if !failed {
		_ = journal.Close()
		t.Fatal("authority-sync fault was not reached")
	}
	lookup := LookupKey(input.PrincipalID, input.RequestID)
	shard := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2])
	pending := filepath.Join(shard, "."+lookup+".pending")
	final := filepath.Join(shard, lookup+".json")
	pendingData, err := os.ReadFile(pending)
	if err != nil {
		_ = journal.Close()
		t.Fatalf("ambiguous authority lost staged identity: %v", err)
	}
	if _, err := os.Stat(final); !errors.Is(err, os.ErrNotExist) {
		_ = journal.Close()
		t.Fatalf("ambiguous authority prematurely published final identity: %v", err)
	}
	var expected requestIdentityV1
	if json.Unmarshal(pendingData, &expected) != nil || validateRequestIdentity(expected) != nil || expected.LookupKeySHA256 != lookup {
		_ = journal.Close()
		t.Fatal("ambiguous authority retained invalid staged identity")
	}
	journal.io = defaults
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := OpenWithClock(root, func() time.Time { return journalTestTime.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	conflict := input
	conflict.RequestSHA256 = digestText("issuance-authority-conflict")
	if _, created, err := restarted.CreateReceipt(context.Background(), conflict, nil); !errors.Is(err, ErrConflict) || created {
		t.Fatalf("ambiguous authority conflict created=%v err=%v", created, err)
	}
	receipt, created, err := restarted.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{}, errors.New("committed identity must not rebind")
	})
	if err != nil || !created || binderCalls != 1 || !reflect.DeepEqual(identityFromReceipt(receipt), expected) {
		t.Fatalf("authority recovery receipt=%+v created=%v binderCalls=%d err=%v", receipt, created, binderCalls, err)
	}
	if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authority recovery retained pending identity: %v", err)
	}
	finalData, err := os.ReadFile(final)
	if err != nil || !bytes.Equal(finalData, pendingData) {
		t.Fatalf("authority recovery changed identity: pending=%d final=%d err=%v", len(pendingData), len(finalData), err)
	}
}

func TestJournalIssuancePublicationFaultsRecoverOnlyFrozenIdentity(t *testing.T) {
	for _, fault := range []string{"final-link-dir-sync", "pending-remove", "pending-remove-dir-sync"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
			if err != nil {
				t.Fatal(err)
			}
			defaults := defaultJournalIO()
			failed := false
			journal.io.syncDir = func(object string, fd int) error {
				if err := defaults.syncDir(object, fd); err != nil {
					return err
				}
				if !failed && ((fault == "final-link-dir-sync" && object == "request-index") ||
					(fault == "pending-remove-dir-sync" && object == "request-index-publish")) {
					failed = true
					return syscall.EIO
				}
				return nil
			}
			journal.io.removeAt = func(object string, parent int, name string) error {
				if !failed && fault == "pending-remove" && object == "request-index-publish" {
					failed = true
					return syscall.EIO
				}
				return defaults.removeAt(object, parent, name)
			}
			input := journalReceiptInputForRun("issuance-publication-fault-" + fault)
			binderCalls := 0
			if _, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{OwnerLeaseID: digestText("issuance-publication-lease-" + fault)}, nil
			}); err == nil || created {
				_ = journal.Close()
				t.Fatalf("publication fault %s created=%v err=%v", fault, created, err)
			}
			if !failed || binderCalls != 1 {
				_ = journal.Close()
				t.Fatalf("publication fault %s failed=%v binderCalls=%d", fault, failed, binderCalls)
			}
			journal.io = defaults
			lookup := LookupKey(input.PrincipalID, input.RequestID)
			shard := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2])
			pendingPath := filepath.Join(shard, "."+lookup+".pending")
			finalPath := filepath.Join(shard, lookup+".json")
			frozenData, err := os.ReadFile(finalPath)
			if errors.Is(err, os.ErrNotExist) {
				frozenData, err = os.ReadFile(pendingPath)
			}
			if err != nil {
				_ = journal.Close()
				t.Fatalf("publication fault %s lost frozen identity: %v", fault, err)
			}
			var frozen requestIdentityV1
			if json.Unmarshal(frozenData, &frozen) != nil || validateRequestIdentity(frozen) != nil || frozen.LookupKeySHA256 != lookup {
				_ = journal.Close()
				t.Fatalf("publication fault %s retained invalid identity", fault)
			}
			historyPath := filepath.Join(shard, requestIssuanceHistoryName)
			historyBefore, err := os.ReadFile(historyPath)
			if err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			authorityName := journalShardAuthorityXattr(lookup[:2])
			authorityBefore, found, err := fgetRootXattr(journal.rootFD, authorityName)
			if err != nil || !found {
				_ = journal.Close()
				t.Fatalf("publication fault authority found=%v err=%v", found, err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}

			restarted, err := OpenWithClock(root, func() time.Time { return journalTestTime.Add(time.Hour) })
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			conflict := input
			conflict.RunID += "-conflict"
			conflict.RequestSHA256 = digestText("issuance-publication-conflict-" + fault)
			if _, created, err := restarted.CreateReceipt(context.Background(), conflict, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{}, nil
			}); !errors.Is(err, ErrConflict) || created {
				t.Fatalf("publication fault %s conflicting reuse created=%v err=%v", fault, created, err)
			}
			receipt, created, err := restarted.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{}, errors.New("committed identity must not rebind")
			})
			if err != nil || !created || !reflect.DeepEqual(identityFromReceipt(receipt), frozen) || binderCalls != 1 {
				t.Fatalf("publication fault %s recovery receipt=%+v created=%v binderCalls=%d err=%v", fault, receipt, created, binderCalls, err)
			}
			replay := ReceiptReplayInput{PrincipalID: input.PrincipalID, PrincipalType: input.PrincipalType, RequestID: input.RequestID,
				RequestSHA256: input.RequestSHA256, Action: input.Action, RunID: input.RunID, AttemptID: input.AttemptID}
			operation, err := restarted.ReplayReceipt(context.Background(), replay)
			if err != nil || !reflect.DeepEqual(operation.Receipt, receipt) {
				t.Fatalf("publication fault %s replay operation=%+v err=%v", fault, operation, err)
			}
			if _, err := os.Stat(pendingPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("publication fault %s retained pending identity: %v", fault, err)
			}
			finalData, err := os.ReadFile(finalPath)
			if err != nil || !bytes.Equal(finalData, frozenData) {
				t.Fatalf("publication fault %s changed frozen identity: before=%d after=%d err=%v", fault, len(frozenData), len(finalData), err)
			}
			historyAfter, err := os.ReadFile(historyPath)
			if err != nil || !bytes.Equal(historyAfter, historyBefore) {
				t.Fatalf("publication fault %s mutated issuance history: before=%d after=%d err=%v", fault, len(historyBefore), len(historyAfter), err)
			}
			authorityAfter, found, err := fgetRootXattr(restarted.rootFD, authorityName)
			if err != nil || !found || !bytes.Equal(authorityAfter, authorityBefore) {
				t.Fatalf("publication fault %s mutated authority: found=%v err=%v", fault, found, err)
			}
			if _, err := os.Stat(filepath.Join(root, "actions", conflict.RunID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("publication fault %s created conflicting run: %v", fault, err)
			}
		})
	}
}

func TestJournalIssuanceHistoryRejectsNonCanonicalDuplicateAndBrokenChronology(t *testing.T) {
	for _, attack := range []string{"non-canonical", "duplicate-key", "sequence", "hash-chain"} {
		t.Run(attack, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			first := journalReceiptInputForRun("issuance-history-validation-run")
			if _, _, err := journal.CreateReceipt(context.Background(), first, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("issuance-history-validation-lease-1")}, nil
			}); err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			firstLookup := LookupKey(first.PrincipalID, first.RequestID)
			second := first
			for candidate := 0; ; candidate++ {
				second.RequestID = fmt.Sprintf("issuance-history-validation-%d", candidate)
				second.RequestSHA256 = digestText(second.RequestID)
				if LookupKey(second.PrincipalID, second.RequestID)[:2] == firstLookup[:2] {
					break
				}
			}
			if _, _, err := journal.CreateReceipt(context.Background(), second, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("issuance-history-validation-lease-2")}, nil
			}); err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			shard := firstLookup[:2]
			historyPath := filepath.Join(root, "actions", requestIndexDirectory, shard, requestIssuanceHistoryName)
			history, err := os.ReadFile(historyPath)
			if err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			lines := bytes.Split(bytes.TrimSuffix(history, []byte{'\n'}), []byte{'\n'})
			if len(lines) != 2 {
				_ = journal.Close()
				t.Fatalf("issuance history records = %d", len(lines))
			}
			switch attack {
			case "non-canonical":
				lines[1] = append([]byte{' '}, lines[1]...)
			case "duplicate-key", "sequence", "hash-chain":
				var record requestIssuanceV1
				if json.Unmarshal(lines[1], &record) != nil {
					_ = journal.Close()
					t.Fatal("cannot decode issuance record")
				}
				switch attack {
				case "duplicate-key":
					record.LookupKeySHA256 = firstLookup
				case "sequence":
					record.Sequence++
				case "hash-chain":
					record.PriorRecordSHA256 = digestText("broken-issuance-chain")
				}
				lines[1], err = json.Marshal(record)
				if err != nil {
					_ = journal.Close()
					t.Fatal(err)
				}
			}
			altered := append(bytes.Join(lines, []byte{'\n'}), '\n')
			if err := os.WriteFile(historyPath, altered, 0o600); err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			authorityName := journalShardAuthorityXattr(shard)
			authority, _, found, err := readRootGenerationAuthority(journal.rootFD, authorityName,
				journalShardAuthorityKind, journal.rootDev, journal.rootIno)
			if err != nil || !found {
				_ = journal.Close()
				t.Fatalf("issuance authority found=%v err=%v", found, err)
			}
			authority.IssuanceFinalRecordSHA256 = sha256Hex(lines[1])
			authorityData, err := json.Marshal(authority)
			if err != nil || fsetRootXattr(journal.rootFD, authorityName, authorityData, rootGenerationXattrReplace) != nil {
				_ = journal.Close()
				t.Fatalf("cannot checkpoint altered history: %v", err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}

			restarted, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if _, err := restarted.ReadByRequest(context.Background(), first.RunID, first.PrincipalID, first.RequestID); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("%s issuance history error = %v", attack, err)
			}
		})
	}
}

func TestJournalIssuanceHistoryRejectsUnsafeMetadataAndByteCeiling(t *testing.T) {
	valid := syscall.Stat_t{Mode: syscall.S_IFREG | 0o600, Uid: uint32(os.Geteuid()), Nlink: 1, Size: requestIssuanceHistoryMaxBytes}
	if err := validateRequestIssuanceHistoryStat(valid); err != nil {
		t.Fatalf("valid issuance history metadata: %v", err)
	}
	wrongOwner := valid
	wrongOwner.Uid++
	if err := validateRequestIssuanceHistoryStat(wrongOwner); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("wrong-owner issuance history error = %v", err)
	}
	for _, attack := range []string{"mode", "hard-link", "symlink", "byte-ceiling"} {
		t.Run(attack, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			input := journalReceiptInputForRun("issuance-history-metadata-run")
			if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("issuance-history-metadata-lease")}, nil
			}); err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			lookup := LookupKey(input.PrincipalID, input.RequestID)
			historyPath := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2], requestIssuanceHistoryName)
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			switch attack {
			case "mode":
				err = os.Chmod(historyPath, 0o640)
			case "hard-link":
				err = os.Link(historyPath, historyPath+".link")
			case "symlink":
				data, readErr := os.ReadFile(historyPath)
				if readErr != nil {
					t.Fatal(readErr)
				}
				target := filepath.Join(root, "replacement-issuance-history")
				if writeErr := os.WriteFile(target, data, 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
				if removeErr := os.Remove(historyPath); removeErr != nil {
					t.Fatal(removeErr)
				}
				err = os.Symlink(target, historyPath)
			case "byte-ceiling":
				err = os.Truncate(historyPath, requestIssuanceHistoryMaxBytes+1)
			}
			if err != nil {
				t.Fatal(err)
			}
			restarted, err := Open(root)
			if restarted != nil {
				_ = restarted.Close()
				t.Fatal("unsafe issuance history opened")
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("issuance history %s error = %v", attack, err)
			}
		})
	}
}

func TestJournalIssuanceAuthorityRejectsCountAboveShardCeiling(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	input := journalReceiptInputForRun("issuance-count-ceiling-run")
	if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("issuance-count-ceiling-lease")}, nil
	}); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	shard := LookupKey(input.PrincipalID, input.RequestID)[:2]
	authorityName := journalShardAuthorityXattr(shard)
	authority, _, found, err := readRootGenerationAuthority(journal.rootFD, authorityName,
		journalShardAuthorityKind, journal.rootDev, journal.rootIno)
	if err != nil || !found {
		_ = journal.Close()
		t.Fatalf("issuance authority found=%v err=%v", found, err)
	}
	authority.IssuanceCount = MaxRequestsPerShard + 1
	authorityData, err := json.Marshal(authority)
	if err != nil || fsetRootXattr(journal.rootFD, authorityName, authorityData, rootGenerationXattrReplace) != nil {
		_ = journal.Close()
		t.Fatalf("cannot install over-limit issuance authority: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(root)
	if restarted != nil {
		_ = restarted.Close()
		t.Fatal("over-limit issuance authority opened")
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("over-limit issuance authority error = %v", err)
	}
}

func TestJournalRequestIndexShardCeiling(t *testing.T) {
	if RequestIndexShards != 256 {
		t.Fatalf("request index shard count = %d", RequestIndexShards)
	}
	shard := t.TempDir()
	if err := os.Chmod(shard, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxRequestsPerShard; index++ {
		name := fmt.Sprintf("%064x.json", index)
		if err := os.WriteFile(filepath.Join(shard, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := syscall.Open(shard, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	if err := validateIndexShard(fd); !errors.Is(err, ErrExhausted) {
		t.Fatalf("full request-index shard error = %v", err)
	}
}

func TestJournalRequestIndexDescriptorReplacementFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	input := journalReceiptInputForRun("descriptor-index-run")
	if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("descriptor-index-lease")}, nil
	}); err != nil {
		t.Fatal(err)
	}
	lookup := LookupKey(input.PrincipalID, input.RequestID)
	path := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2], lookup+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults := defaultJournalIO()
	replaced := false
	journal.io.syncFile = func(object string, file *os.File) error {
		if err := defaults.syncFile(object, file); err != nil {
			return err
		}
		if object == "request-index" && !replaced {
			replaced = true
			if err := os.Rename(path, path+".replaced"); err != nil {
				return err
			}
			return os.WriteFile(path, data, 0o600)
		}
		return nil
	}
	if _, err := journal.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("descriptor replacement error = %v", err)
	}
}

func TestJournalReceiptAppendFaultsRollbackBeforeVisibility(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*Journal)
	}{
		{"write", func(journal *Journal) {
			defaults := defaultJournalIO()
			failed := false
			journal.io.write = func(object string, file *os.File, data []byte) error {
				if object == "journal-segment" && !failed {
					failed = true
					return io.ErrShortWrite
				}
				return defaults.write(object, file, data)
			}
		}},
		{"file-sync", func(journal *Journal) {
			defaults := defaultJournalIO()
			failed := false
			journal.io.syncFile = func(object string, file *os.File) error {
				if object == "journal-segment" && !failed {
					failed = true
					return syscall.EIO
				}
				return defaults.syncFile(object, file)
			}
		}},
		{"directory-sync", func(journal *Journal) {
			defaults := defaultJournalIO()
			failed := false
			journal.io.syncDir = func(object string, fd int) error {
				if object == "journal-segment" && !failed {
					failed = true
					return syscall.EIO
				}
				return defaults.syncDir(object, fd)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			input := journalReceiptInputForRun("faulted-receipt-run")
			binderCalls := 0
			test.inject(journal)
			if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{OwnerLeaseID: digestText("faulted-receipt-lease")}, nil
			}); err == nil {
				t.Fatal("faulted append succeeded")
			}
			journal.io = defaultJournalIO()
			operations, err := journal.OperationsForLease(context.Background(), input.RunID, digestText("faulted-receipt-lease"), nil)
			if err != nil || len(operations) != 0 {
				t.Fatalf("failed receipt became visible: operations=%+v err=%v", operations, err)
			}
			guard, err := journal.acquireActions(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			identity, found, err := guard.readIdentity(LookupKey(input.PrincipalID, input.RequestID))
			guard.close()
			if err != nil || !found || identity.ReceivedAt != canonicalTime(journalTestTime) {
				t.Fatalf("frozen identity = %+v found=%v err=%v", identity, found, err)
			}
			conflict := input
			conflict.RequestSHA256 = digestText("conflicting-replay")
			if _, _, err := journal.CreateReceipt(context.Background(), conflict, nil); !errors.Is(err, ErrConflict) {
				t.Fatalf("conflicting frozen replay error = %v", err)
			}
			receipt, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{}, errors.New("recovery must not rebind")
			})
			if err != nil || !created || binderCalls != 1 || receipt.OperationID != identity.OperationID || receipt.ReceivedAt != identity.ReceivedAt {
				t.Fatalf("recovered receipt=%+v created=%v binderCalls=%d err=%v", receipt, created, binderCalls, err)
			}
		})
	}
}

func TestJournalAppendCommitDirectorySyncFailureRollsBackBeforeLiveVisibility(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	input := journalReceiptInputForRun("append-commit-live-run")
	leaseID := digestText("append-commit-live-lease")
	defaults := defaultJournalIO()
	failed := false
	journal.io.syncDir = func(object string, fd int) error {
		if object == "append-commit" && !failed {
			failed = true
			return syscall.EIO
		}
		return defaults.syncDir(object, fd)
	}
	if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: leaseID}, nil
	}); err == nil {
		t.Fatal("append-commit directory-sync fault succeeded")
	}
	if !failed {
		t.Fatal("append-commit directory-sync fault was not reached")
	}

	// OperationsForLease is the exact watcher visibility feed. The failed
	// admission must not become claimable in the live process.
	operations, err := journal.OperationsForLease(context.Background(), input.RunID, leaseID, nil)
	if err != nil || len(operations) != 0 {
		t.Fatalf("failed receipt reached watcher feed: operations=%+v err=%v", operations, err)
	}
	if _, err := journal.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID); !errors.Is(err, ErrReceiptPending) {
		t.Fatalf("failed receipt read visibility error = %v", err)
	}
	segment, err := os.ReadFile(filepath.Join(root, "actions", input.RunID, "00000001.jsonl"))
	if err != nil || len(segment) != 0 {
		t.Fatalf("failed receipt was not rolled back: bytes=%d err=%v", len(segment), err)
	}

	replay := ReceiptReplayInput{PrincipalID: input.PrincipalID, PrincipalType: input.PrincipalType, RequestID: input.RequestID,
		RequestSHA256: input.RequestSHA256, Action: input.Action, RunID: input.RunID, AttemptID: input.AttemptID}
	operation, err := journal.ReplayReceipt(context.Background(), replay)
	if err != nil || operation.Status != StatusReceived || operation.Receipt.Sequence != 1 || operation.Receipt.ReceivedAt != canonicalTime(journalTestTime) {
		t.Fatalf("exact live replay operation=%+v err=%v", operation, err)
	}
}

func TestJournalAppendCommitDirectorySyncFailureRecoversAfterRestart(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	runID := "append-commit-restart-run"
	first := journalReceiptInput("append-commit-first", digestText("append-commit-first"))
	first.RunID = runID
	firstLease := digestText("append-commit-first-lease")
	firstReceipt, _, err := journal.CreateReceipt(context.Background(), first, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: firstLease}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	second := journalReceiptInput("append-commit-second", digestText("append-commit-second"))
	second.RunID = runID
	secondLease := digestText("append-commit-second-lease")
	defaults := defaultJournalIO()
	journal.io.syncDir = func(object string, fd int) error {
		if object == "append-commit" {
			return syscall.EIO
		}
		return defaults.syncDir(object, fd)
	}
	if _, _, err := journal.CreateReceipt(context.Background(), second, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: secondLease}, nil
	}); err == nil {
		t.Fatal("append-commit directory-sync fault succeeded")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := OpenWithClock(root, func() time.Time { return journalTestTime.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	firstOperation, err := restarted.Read(context.Background(), runID, firstReceipt.OperationID)
	if err != nil || firstOperation.Receipt.Sequence != 1 {
		t.Fatalf("committed prefix operation=%+v err=%v", firstOperation, err)
	}
	if _, err := restarted.ReadByRequest(context.Background(), runID, second.PrincipalID, second.RequestID); !errors.Is(err, ErrReceiptPending) {
		t.Fatalf("failed receipt after restart error = %v", err)
	}
	if operations, err := restarted.OperationsForLease(context.Background(), runID, secondLease, nil); err != nil || len(operations) != 0 {
		t.Fatalf("failed receipt reached watcher after restart: operations=%+v err=%v", operations, err)
	}
	replay := ReceiptReplayInput{PrincipalID: second.PrincipalID, PrincipalType: second.PrincipalType, RequestID: second.RequestID,
		RequestSHA256: second.RequestSHA256, Action: second.Action, RunID: second.RunID, AttemptID: second.AttemptID}
	operation, err := restarted.ReplayReceipt(context.Background(), replay)
	if err != nil || operation.Status != StatusReceived || operation.Receipt.Sequence != 2 || operation.Receipt.ReceivedAt != canonicalTime(journalTestTime) {
		t.Fatalf("restart replay operation=%+v err=%v", operation, err)
	}
}

func TestJournalAppendCommitAmbiguityRestoresMarkerOrFailsClosed(t *testing.T) {
	t.Run("durable recovery marker", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		journal, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.Close()
		input := journalReceiptInputForRun("append-marker-restore-run")
		leaseID := digestText("append-marker-restore-lease")
		defaults := defaultJournalIO()
		journal.io.syncDir = func(object string, fd int) error {
			if object == "append-commit" {
				return syscall.EIO
			}
			return defaults.syncDir(object, fd)
		}
		journal.io.truncate = func(object string, file *os.File, size int64) error {
			if object == "append-rollback" {
				return syscall.EIO
			}
			return defaults.truncate(object, file, size)
		}
		if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
			return AdmissionBinding{OwnerLeaseID: leaseID}, nil
		}); err == nil {
			t.Fatal("unproven append succeeded")
		}
		marker := filepath.Join(root, "actions", input.RunID, appendPendingName)
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("recovery marker was not restored: %v", err)
		}
		journal.io = defaults
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
		restarted, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		if operations, err := restarted.OperationsForLease(context.Background(), input.RunID, leaseID, nil); err != nil || len(operations) != 0 {
			t.Fatalf("restored marker did not block watcher: operations=%+v err=%v", operations, err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovered marker remains: %v", err)
		}
	})

	t.Run("instance latch", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		journal, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		input := journalReceiptInputForRun("append-fail-closed-run")
		leaseID := digestText("append-fail-closed-lease")
		defaults := defaultJournalIO()
		journal.io.syncDir = func(object string, fd int) error {
			if object == "append-commit" || object == "append-recovery-marker" {
				return syscall.EIO
			}
			return defaults.syncDir(object, fd)
		}
		journal.io.truncate = func(object string, file *os.File, size int64) error {
			if object == "append-rollback" {
				return syscall.EIO
			}
			return defaults.truncate(object, file, size)
		}
		if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
			return AdmissionBinding{OwnerLeaseID: leaseID}, nil
		}); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("unrecoverable append error = %v", err)
		}
		if !journal.failedClosed.Load() {
			t.Fatal("journal instance did not latch fail-closed state")
		}
		journal.io = defaults
		if operations, err := journal.OperationsForLease(context.Background(), input.RunID, leaseID, nil); !errors.Is(err, ErrIntegrity) || len(operations) != 0 {
			t.Fatalf("latched journal admitted effect path: operations=%+v err=%v", operations, err)
		}
		peer, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		peerContext, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if operations, err := peer.OperationsForLease(peerContext, input.RunID, leaseID, nil); !errors.Is(err, ErrBusy) || len(operations) != 0 {
			t.Fatalf("retained global lock admitted peer effect path: operations=%+v err=%v", operations, err)
		}
		if err := peer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}

		restarted, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		if operations, err := restarted.OperationsForLease(context.Background(), input.RunID, leaseID, nil); err != nil || len(operations) != 0 {
			t.Fatalf("restart recovery exposed failed receipt: operations=%+v err=%v", operations, err)
		}
	})
}

func TestJournalIndexDirectorySyncFailureRecoversFrozenReceipt(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	defaults := defaultJournalIO()
	failed := false
	journal.io.syncDir = func(object string, fd int) error {
		if object == "request-index" && !failed {
			failed = true
			return syscall.EIO
		}
		return defaults.syncDir(object, fd)
	}
	input := journalReceiptInputForRun("index-sync-run")
	binderCalls := 0
	if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{OwnerLeaseID: digestText("index-sync-lease")}, nil
	}); err == nil {
		t.Fatal("index directory-sync fault succeeded")
	}
	journal.io = defaults
	if _, err := journal.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID); !errors.Is(err, ErrReceiptPending) {
		t.Fatalf("durable index without receipt error = %v", err)
	}
	receipt, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{}, errors.New("frozen reservation must not rebind")
	})
	if err != nil || !created || binderCalls != 1 || receipt.ReceivedAt != canonicalTime(journalTestTime) {
		t.Fatalf("index recovery receipt=%+v created=%v calls=%d err=%v", receipt, created, binderCalls, err)
	}
}

func TestJournalIndexWriteAndFileSyncFailuresLeaveNoReservation(t *testing.T) {
	for _, object := range []string{"write", "file-sync"} {
		t.Run(object, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			defaults := defaultJournalIO()
			if object == "write" {
				journal.io.write = func(kind string, file *os.File, data []byte) error {
					if kind == "request-index" {
						return io.ErrShortWrite
					}
					return defaults.write(kind, file, data)
				}
			} else {
				journal.io.syncFile = func(kind string, file *os.File) error {
					if kind == "request-index" {
						return syscall.EIO
					}
					return defaults.syncFile(kind, file)
				}
			}
			input := journalReceiptInputForRun("index-local-fault-run")
			binderCalls := 0
			if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{OwnerLeaseID: digestText("index-local-fault-lease")}, nil
			}); err == nil {
				t.Fatal("faulted index creation succeeded")
			}
			journal.io = defaults
			if _, err := journal.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("failed index write left a reservation: %v", err)
			}
			if operations, err := journal.OperationsForLease(context.Background(), input.RunID, digestText("index-local-fault-lease"), nil); err != nil || len(operations) != 0 {
				t.Fatalf("failed index write exposed receipt: operations=%+v err=%v", operations, err)
			}
			if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{OwnerLeaseID: digestText("index-local-fault-lease")}, nil
			}); err != nil || binderCalls != 2 {
				t.Fatalf("clean retry calls=%d err=%v", binderCalls, err)
			}
		})
	}
}

func TestJournalDirectorySyncFailureIsRetriedBeforeReservation(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	defaults := defaultJournalIO()
	failed := false
	journal.io.syncDir = func(object string, fd int) error {
		if object == "directory" && !failed {
			failed = true
			return syscall.EIO
		}
		return defaults.syncDir(object, fd)
	}
	input := journalReceiptInputForRun("directory-sync-retry-run")
	binderCalls := 0
	if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{OwnerLeaseID: digestText("directory-sync-retry-lease")}, nil
	}); err == nil {
		t.Fatal("new run-directory sync failure was discarded")
	}
	if binderCalls != 0 {
		t.Fatalf("binding occurred before run directory durability: %d", binderCalls)
	}
	journal.io = defaults
	if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{OwnerLeaseID: digestText("directory-sync-retry-lease")}, nil
	}); err != nil || binderCalls != 1 {
		t.Fatalf("directory durability retry calls=%d err=%v", binderCalls, err)
	}
}

func TestJournalPendingAppendBlocksEffectsUntilCrashRollback(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	input := journalReceiptInputForRun("crash-window-run")
	leaseID := digestText("crash-window-lease")
	defaults := defaultJournalIO()
	journal.io.write = func(object string, file *os.File, data []byte) error {
		if err := defaults.write(object, file, data); err != nil {
			return err
		}
		if object == "journal-segment" {
			return syscall.EIO
		}
		return nil
	}
	journal.io.truncate = func(object string, file *os.File, size int64) error {
		if object == "append-rollback" {
			return syscall.EIO
		}
		return defaults.truncate(object, file, size)
	}
	if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: leaseID}, nil
	}); err == nil {
		t.Fatal("ambiguous append unexpectedly succeeded")
	}
	if operations, err := journal.OperationsForLease(context.Background(), input.RunID, leaseID, nil); err == nil || len(operations) != 0 {
		t.Fatalf("poisoned append was executable: operations=%+v err=%v", operations, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := OpenWithClock(root, func() time.Time { return journalTestTime.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	operations, err := restarted.OperationsForLease(context.Background(), input.RunID, leaseID, nil)
	if err != nil || len(operations) != 0 {
		t.Fatalf("restart rollback exposed failed receipt: operations=%+v err=%v", operations, err)
	}
	replay := ReceiptReplayInput{PrincipalID: input.PrincipalID, PrincipalType: input.PrincipalType, RequestID: input.RequestID,
		RequestSHA256: input.RequestSHA256, Action: input.Action, RunID: input.RunID, AttemptID: input.AttemptID}
	operation, err := restarted.ReplayReceipt(context.Background(), replay)
	if err != nil || operation.Status != StatusReceived || operation.Receipt.ReceivedAt != canonicalTime(journalTestTime) {
		t.Fatalf("crash recovery operation=%+v err=%v", operation, err)
	}
}

func TestJournalReceiptEnforcesFrozenCommandBounds(t *testing.T) {
	valid := journalReceiptInput("r"+strings.Repeat("e", maxReceiptRequestID-1), digestText("boundary-request"))
	valid.ExpectedState = "S" + strings.Repeat("T", 127)
	valid.Reason = strings.Repeat("r", maxReceiptReason)

	tests := []struct {
		name   string
		mutate func(*ReceiptInput)
	}{
		{"request ID over 128 bytes", func(input *ReceiptInput) { input.RequestID = strings.Repeat("r", maxReceiptRequestID+1) }},
		{"lowercase state name", func(input *ReceiptInput) { input.ExpectedState = "implementing" }},
		{"state name over 128 bytes", func(input *ReceiptInput) { input.ExpectedState = "S" + strings.Repeat("T", 128) }},
		{"reason over 1 KiB", func(input *ReceiptInput) { input.Reason = strings.Repeat("r", maxReceiptReason+1) }},
		{"reason with invalid UTF-8", func(input *ReceiptInput) { input.Reason = string([]byte{0xff}) }},
		{"reason with NUL", func(input *ReceiptInput) { input.Reason = "stop\x00now" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()

			input := valid
			test.mutate(&input)
			if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("lease")}, nil
			}); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("out-of-bounds receipt error = %v", err)
			}
		})
	}

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, created, err := journal.CreateReceipt(context.Background(), valid, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("lease")}, nil
	}); err != nil || !created {
		t.Fatalf("boundary receipt created=%v error=%v", created, err)
	}
}

func TestJournalAtomicallyReservesOneActionPerDecisionRequest(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	for index := 1; index <= 2; index++ {
		go func(index int) {
			<-start
			requestID := fmt.Sprintf("decision-command-%d", index)
			_, _, err := journal.CreateReceipt(context.Background(), ReceiptInput{
				PrincipalID: "service-principal", PrincipalType: "service", RequestID: requestID, RequestSHA256: digestText(requestID),
				Action: "decision", RunID: "decision-run", AttemptID: "attempt-1", ExpectedState: "HUMAN_DECISION_REQUIRED", ExpectedRevision: digestText("revision"),
				DelegatedActor: &DelegatedActorV1{SubjectID: "human-1", SubjectType: "user"},
				Payload:        json.RawMessage(`{"decision_request_id":"origin-event","answer":"approve"}`), PolicyVersion: "ep006-governed-action-v1",
				AuthorityGrantSHA256: digestText("grant"), StateTransitionID: "origin-event",
			}, nil)
			results <- err
		}(index)
	}
	close(start)
	succeeded, reserved := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrDecisionExists):
			reserved++
		default:
			t.Fatalf("concurrent decision reservation error = %v", err)
		}
	}
	if succeeded != 1 || reserved != 1 {
		t.Fatalf("concurrent decision results success=%d reserved=%d", succeeded, reserved)
	}
}

func TestJournalDecisionReplayReappliesSecondaryUniquenessAfterCrash(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runID := "decision-replay-run"
	decisionRequestID := "decision-origin"
	decisionInput := func(requestID string) ReceiptInput {
		return ReceiptInput{
			PrincipalID: "service-principal", PrincipalType: "service", RequestID: requestID, RequestSHA256: digestText(requestID),
			Action: "decision", RunID: runID, AttemptID: "attempt-1", ExpectedState: "HUMAN_DECISION_REQUIRED", ExpectedRevision: digestText("revision"),
			DelegatedActor: &DelegatedActorV1{SubjectID: "human-1", SubjectType: "user"},
			Payload:        json.RawMessage(`{"decision_request_id":"` + decisionRequestID + `","answer":"approve"}`), PolicyVersion: "ep006-governed-action-v1",
			AuthorityGrantSHA256: digestText("grant"), StateTransitionID: decisionRequestID,
		}
	}

	// Request A reserves its immutable global identity, then loses the receipt
	// append. Restarting models the crash boundary after durable reservation.
	requestA := decisionInput("decision-command-a")
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	defaults := defaultJournalIO()
	failed := false
	journal.io.write = func(object string, file *os.File, data []byte) error {
		if object == "journal-segment" && !failed {
			failed = true
			return io.ErrShortWrite
		}
		return defaults.write(object, file, data)
	}
	if _, _, err := journal.CreateReceipt(context.Background(), requestA, nil); err == nil {
		t.Fatal("request A receipt append fault succeeded")
	}
	if !failed {
		t.Fatal("request A receipt append fault was not reached")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	// Request B can now durably own the same secondary decision identity because
	// request A has no materialized receipt in the run journal.
	journal, err = OpenWithClock(root, func() time.Time { return journalTestTime.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	requestB := decisionInput("decision-command-b")
	receiptB, created, err := journal.CreateReceipt(context.Background(), requestB, nil)
	if err != nil || !created {
		t.Fatalf("request B receipt=%+v created=%v err=%v", receiptB, created, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	journal, err = OpenWithClock(root, func() time.Time { return journalTestTime.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	segmentPath := filepath.Join(root, "actions", runID, "00000001.jsonl")
	segmentBefore, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatal(err)
	}
	lookupA := LookupKey(requestA.PrincipalID, requestA.RequestID)
	identityPathA := filepath.Join(root, "actions", requestIndexDirectory, lookupA[:2], lookupA+".json")
	identityBefore, err := os.ReadFile(identityPathA)
	if err != nil {
		t.Fatal(err)
	}
	replayA := ReceiptReplayInput{PrincipalID: requestA.PrincipalID, PrincipalType: requestA.PrincipalType, RequestID: requestA.RequestID,
		RequestSHA256: requestA.RequestSHA256, Action: requestA.Action, RunID: requestA.RunID, AttemptID: requestA.AttemptID}
	if _, err := journal.ReplayReceipt(context.Background(), replayA); !errors.Is(err, ErrDecisionExists) {
		t.Fatalf("request A ReplayReceipt error = %v", err)
	}
	if _, created, err := journal.CreateReceipt(context.Background(), requestA, func(uint64) (AdmissionBinding, error) {
		t.Fatal("conflicting frozen identity must not rebind")
		return AdmissionBinding{}, nil
	}); !errors.Is(err, ErrDecisionExists) || created {
		t.Fatalf("request A CreateReceipt created=%v error=%v", created, err)
	}
	segmentAfter, err := os.ReadFile(segmentPath)
	if err != nil || !bytes.Equal(segmentAfter, segmentBefore) {
		t.Fatalf("conflicting replay mutated journal: before=%d after=%d err=%v", len(segmentBefore), len(segmentAfter), err)
	}
	identityAfter, err := os.ReadFile(identityPathA)
	if err != nil || !bytes.Equal(identityAfter, identityBefore) {
		t.Fatalf("conflicting replay mutated request A identity: before=%d after=%d err=%v", len(identityBefore), len(identityAfter), err)
	}

	operationB, err := journal.Read(context.Background(), runID, receiptB.OperationID)
	if err != nil || operationB.Receipt.RequestID != requestB.RequestID || operationB.Status != StatusReceived {
		t.Fatalf("request B operation=%+v err=%v", operationB, err)
	}
	decisionOperation, err := journal.ReadByDecisionRequest(context.Background(), runID, decisionRequestID)
	if err != nil || decisionOperation.Receipt.OperationID != receiptB.OperationID {
		t.Fatalf("decision owner operation=%+v err=%v", decisionOperation, err)
	}
	if _, err := journal.ReadByRequest(context.Background(), runID, requestA.PrincipalID, requestA.RequestID); !errors.Is(err, ErrReceiptPending) {
		t.Fatalf("request A immutable identity state error = %v", err)
	}
	if _, err := journal.ReplayReceipt(context.Background(), replayA); !errors.Is(err, ErrDecisionExists) {
		t.Fatalf("repeated request A replay error = %v", err)
	}
}

func TestJournalSegmentRolloverLinksPriorFinalRecord(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	guard, err := journal.acquire(context.Background(), "rollover-run")
	if err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(root, "actions", "rollover-run")
	guard.close()
	data := buildNearlyFullSegment(t, "rollover-run")
	if len(data) >= MaxSegmentBytes || MaxSegmentBytes-len(data) > 600 {
		t.Fatalf("fixture segment size = %d", len(data))
	}
	if err := os.WriteFile(filepath.Join(runPath, "00000001.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	input := journalReceiptInput("rollover-request", digestText("rollover-request"))
	input.RunID = "rollover-run"
	receipt, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("lease")}, nil
	})
	if err != nil || !created || receipt.Epoch != 2 || receipt.PriorSegmentFinalRecordSHA256 == "" {
		t.Fatalf("rollover receipt = %+v, created=%v err=%v", receipt, created, err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if receipt.PriorSegmentFinalRecordSHA256 != digestText(string(lines[len(lines)-1])) {
		t.Fatal("rollover did not bind the prior segment final record")
	}
}

func TestJournalExhaustionAfterEightBoundedLinkedEpochs(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	guard, err := journal.acquire(context.Background(), "exhausted-run")
	if err != nil {
		t.Fatal(err)
	}
	guard.close()
	runPath := filepath.Join(root, "actions", "exhausted-run")
	nextSequence, prior := uint64(1), ""
	for epoch := uint64(1); epoch <= MaxJournalEpochs; epoch++ {
		data, next, finalDigest := buildNearlyFullEpoch(t, "exhausted-run", epoch, nextSequence, prior)
		if err := os.WriteFile(filepath.Join(runPath, segmentName(epoch)), data, 0o600); err != nil {
			t.Fatal(err)
		}
		nextSequence, prior = next, finalDigest
	}
	input := journalReceiptInputForRun("exhausted-run")
	input.RequestID, input.RequestSHA256 = "beyond-ceiling", digestText("beyond-ceiling")
	if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("lease")}, nil
	}); !errors.Is(err, ErrExhausted) {
		t.Fatalf("exhausted append error = %v", err)
	}
}

func TestJournalRejectsSymlinkAndHardLinkedSegment(t *testing.T) {
	for _, test := range []struct {
		name string
		make func(string, string) error
	}{
		{"symlink", func(target, path string) error { return os.Symlink(target, path) }},
		{"hard-link", func(target, path string) error { return os.Link(target, path) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			guard, err := journal.acquire(context.Background(), "unsafe-run")
			if err != nil {
				t.Fatal(err)
			}
			guard.close()
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := test.make(target, filepath.Join(root, "actions", "unsafe-run", "00000001.jsonl")); err != nil {
				t.Fatal(err)
			}
			if _, _, err := journal.CreateReceipt(context.Background(), journalReceiptInputForRun("unsafe-run"), nil); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("unsafe segment error = %v", err)
			}
		})
	}
}

func TestActionJournalLockHelper(t *testing.T) {
	root := os.Getenv("ABCP_ACTION_LOCK_HELPER")
	if root == "" {
		return
	}
	journal, err := Open(root)
	if err != nil {
		os.Exit(20)
	}
	guard, err := journal.acquire(context.Background(), "locked-run")
	if err != nil {
		os.Exit(21)
	}
	fmt.Println("locked")
	time.Sleep(10 * time.Second)
	guard.close()
	journal.Close()
}

func TestJournalCrossProcessLockWaitIsBounded(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestActionJournalLockHelper$")
	command.Env = append(os.Environ(), "ABCP_ACTION_LOCK_HELPER="+root)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	buffer := make([]byte, 7)
	if _, err := stdout.Read(buffer); err != nil || string(buffer) != "locked\n" {
		t.Fatalf("helper readiness = %q, err=%v", buffer, err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	started := time.Now()
	_, _, err = journal.CreateReceipt(context.Background(), journalReceiptInputForRun("locked-run"), nil)
	elapsed := time.Since(started)
	if !errors.Is(err, ErrBusy) || elapsed < 1900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("bounded lock result err=%v elapsed=%s", err, elapsed)
	}
}

func TestDecisionEffectAuthorityHelper(t *testing.T) {
	root := os.Getenv("ABCP_DECISION_EFFECT_HELPER")
	if root == "" {
		return
	}
	runID := os.Getenv("ABCP_DECISION_EFFECT_RUN")
	operationID := os.Getenv("ABCP_DECISION_EFFECT_OPERATION")
	journal, err := Open(root)
	if err != nil {
		os.Exit(30)
	}
	lease, _, err := journal.AcquireDecisionEffect(context.Background(), runID, operationID)
	if err != nil {
		os.Exit(31)
	}
	fmt.Println("held")
	command, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		os.Exit(32)
	}
	if command == "crash\n" {
		os.Exit(0)
	}
	if command != "close\n" {
		os.Exit(33)
	}
	if err := lease.Close(); errors.Is(err, ErrIntegrity) {
		fmt.Println("integrity")
		return
	} else if err != nil {
		os.Exit(34)
	}
	fmt.Println("closed")
}

func TestDecisionEffectAuthorityIsCrossProcessAndCrashReleasesIt(t *testing.T) {
	root, journal, receipt := createClaimedDecisionJournal(t, "effect-crash-run")
	defer journal.Close()
	command, stdin, output := startDecisionEffectHelper(t, root, receipt.RunID, receipt.OperationID)
	finished := false
	defer func() {
		if !finished {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	lease, _, err := journal.AcquireDecisionEffect(ctx, receipt.RunID, receipt.OperationID)
	cancel()
	if lease != nil {
		_ = lease.Close()
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("live effect contender error = %v", err)
	}
	operation, err := journal.Read(context.Background(), receipt.RunID, receipt.OperationID)
	if err != nil || operation.Status != StatusClaimed {
		t.Fatalf("live effect was prematurely reconciled: operation=%+v err=%v", operation, err)
	}
	if _, err := fmt.Fprintln(stdin, "crash"); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("crashed effect helper exit = %v", err)
	}
	finished = true
	lease, operation, err = journal.AcquireDecisionEffect(context.Background(), receipt.RunID, receipt.OperationID)
	if err != nil || operation.Status != StatusClaimed {
		t.Fatalf("crash-recovered effect authority: operation=%+v err=%v", operation, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if line, err := output.ReadString('\n'); err == nil || line != "" {
		t.Fatalf("unexpected helper output after crash = %q err=%v", line, err)
	}
}

func TestDecisionEffectAuthorityCannotBeReissuedAfterActionsReplacement(t *testing.T) {
	root, journal, receipt := createClaimedDecisionJournal(t, "effect-replacement-run")
	defer journal.Close()
	command, stdin, output := startDecisionEffectHelper(t, root, receipt.RunID, receipt.OperationID)
	finished := false
	defer func() {
		if !finished {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	actions := filepath.Join(root, "actions")
	if err := os.Rename(actions, actions+".retired"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(actions, 0o700); err != nil {
		t.Fatal(err)
	}
	if lease, _, err := journal.AcquireDecisionEffect(context.Background(), receipt.RunID, receipt.OperationID); !errors.Is(err, ErrIntegrity) {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatalf("existing process accepted replacement effect authority: %v", err)
	}
	if fresh, err := Open(root); !errors.Is(err, ErrIntegrity) {
		if fresh != nil {
			_ = fresh.Close()
		}
		t.Fatalf("fresh process accepted replacement effect authority: %v", err)
	}
	if _, err := fmt.Fprintln(stdin, "close"); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if line, err := output.ReadString('\n'); err != nil || line != "integrity\n" {
		t.Fatalf("replacement helper result = %q err=%v", line, err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("replacement helper exit = %v", err)
	}
	finished = true
}

func createClaimedDecisionJournal(t *testing.T, runID string) (string, *Journal, ActionReceiptV1) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatal(err)
	}
	input := journalReceiptInputForRun(runID)
	input.Action = "decision"
	input.ExpectedState = "HUMAN_DECISION_REQUIRED"
	input.DelegatedActor = &DelegatedActorV1{SubjectID: "human-1", SubjectType: "user"}
	input.Payload = json.RawMessage(`{"decision_request_id":"decision-origin-1","answer":"approve"}`)
	input.PolicyVersion = "ep006-governed-action-v1"
	input.AuthorityGrantSHA256 = digestText("grant")
	receipt, created, err := journal.CreateReceipt(context.Background(), input, nil)
	if err != nil || !created {
		_ = journal.Close()
		t.Fatalf("decision receipt = %+v created=%v err=%v", receipt, created, err)
	}
	if _, created, err := journal.CreateClaim(context.Background(), runID, receipt.OperationID, "", DecisionEventDomain); err != nil || !created {
		_ = journal.Close()
		t.Fatalf("decision claim created=%v err=%v", created, err)
	}
	return root, journal, receipt
}

func startDecisionEffectHelper(t *testing.T, root, runID, operationID string) (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestDecisionEffectAuthorityHelper$")
	command.Env = append(os.Environ(), "ABCP_DECISION_EFFECT_HELPER="+root, "ABCP_DECISION_EFFECT_RUN="+runID, "ABCP_DECISION_EFFECT_OPERATION="+operationID)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	output := bufio.NewReader(stdout)
	if line, err := output.ReadString('\n'); err != nil || line != "held\n" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("effect helper readiness = %q err=%v", line, err)
	}
	return command, stdin, output
}

func TestJournalInProcessFairnessGateHonorsContextAndReleases(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	held, err := journal.acquireActions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if contender, err := journal.acquireActions(ctx); contender != nil || !errors.Is(err, ErrBusy) || !errors.Is(err, context.DeadlineExceeded) {
		if contender != nil {
			_ = contender.close()
		}
		_ = held.close()
		t.Fatalf("bounded local wait contender=%v err=%v", contender, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		_ = held.close()
		t.Fatalf("bounded local wait took %s", elapsed)
	}
	if err := held.close(); err != nil {
		t.Fatal(err)
	}
	reacquired, err := journal.acquireActions(context.Background())
	if err != nil {
		t.Fatalf("released local gate was not reusable: %v", err)
	}
	if err := reacquired.close(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalRootGenerationAuthorityIsDurableAndNonReissuable(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	input := journalReceiptInputForRun("root-authority-run")
	if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("root-authority-lease")}, nil
	}); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	rootAuthority, found, err := fgetRootXattr(journal.rootFD, journalRootAuthorityXattr)
	if err != nil || !found {
		_ = journal.Close()
		t.Fatalf("journal root authority found=%v err=%v", found, err)
	}
	authority, canonical, established, err := decodeRootGenerationAuthority(rootAuthority, journalRootAuthorityKind, journal.rootDev, journal.rootIno)
	if err != nil || !established || authority.State != rootGenerationEstablished || len(authority.Objects) != 8 || !bytes.Equal(canonical, rootAuthority) {
		_ = journal.Close()
		t.Fatalf("journal root authority = %+v, established=%v err=%v", authority, established, err)
	}
	lookup := LookupKey(input.PrincipalID, input.RequestID)
	shardAuthorityName := journalShardAuthorityXattr(lookup[:2])
	shardAuthority, found, err := fgetRootXattr(journal.rootFD, shardAuthorityName)
	if err != nil || !found {
		_ = journal.Close()
		t.Fatalf("journal shard authority found=%v err=%v", found, err)
	}
	shard, _, established, err := decodeRootGenerationAuthority(shardAuthority, journalShardAuthorityKind, journal.rootDev, journal.rootIno)
	if err != nil || !established || shard.State != rootGenerationEstablished || len(shard.Objects) != 3 ||
		shard.IssuanceCount != 1 || !validDigest(shard.IssuanceFinalRecordSHA256) {
		_ = journal.Close()
		t.Fatalf("journal shard authority = %+v, established=%v err=%v", shard, established, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	observedRoot, _, err := fgetRootXattr(reopened.rootFD, journalRootAuthorityXattr)
	if err != nil || !bytes.Equal(observedRoot, rootAuthority) {
		_ = reopened.Close()
		t.Fatalf("journal root authority changed across reopen: err=%v", err)
	}
	observedShard, _, err := fgetRootXattr(reopened.rootFD, shardAuthorityName)
	if err != nil || !bytes.Equal(observedShard, shardAuthority) {
		_ = reopened.Close()
		t.Fatalf("journal shard authority changed across reopen: err=%v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	actions := filepath.Join(root, "actions")
	if err := os.Rename(actions, actions+"-original"); err != nil {
		t.Fatal(err)
	}
	actionsAnchor := filepath.Join(root, actionsIdentityName)
	if err := os.Rename(actionsAnchor, actionsAnchor+"-original"); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(root)
	if fresh != nil {
		_ = fresh.Close()
		t.Fatal("paired journal namespace loss issued a new generation")
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("paired journal namespace loss error = %v", err)
	}
	if _, err := os.Lstat(actions); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("paired journal namespace loss recreated actions: %v", err)
	}
}

func TestJournalRootGenerationAuthorityConcurrentFirstOpen(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	const contenders = 8
	start := make(chan struct{})
	results := make(chan error, contenders)
	for range contenders {
		go func() {
			<-start
			journal, err := Open(root)
			if err == nil {
				err = journal.Close()
			}
			results <- err
		}()
	}
	close(start)
	for range contenders {
		if err := <-results; err != nil {
			t.Fatalf("concurrent first Open failed: %v", err)
		}
	}
	journal, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	authority, found, err := fgetRootXattr(journal.rootFD, journalRootAuthorityXattr)
	if err != nil || !found || !bytes.Equal(authority, journal.rootAuthorityData) {
		t.Fatalf("concurrent journal authority found=%v err=%v", found, err)
	}
}

func TestActionJournalBootstrapCrashHelper(t *testing.T) {
	root := os.Getenv("ABCP_ACTION_BOOTSTRAP_CRASH_ROOT")
	if root == "" {
		return
	}
	target := os.Getenv("ABCP_ACTION_BOOTSTRAP_CRASH_IDENTITY")
	boundary := os.Getenv("ABCP_ACTION_BOOTSTRAP_CRASH_BOUNDARY")
	journalBootstrapBoundaryHook = func(identity, observed string) {
		if identity == target && observed == boundary {
			os.Exit(80)
		}
	}
	journal, err := Open(root)
	if err != nil {
		os.Exit(81)
	}
	_ = journal.Close()
	os.Exit(82)
}

func TestJournalInitialIdentityBootstrapRecoversEveryCrashBoundary(t *testing.T) {
	identities := []string{actionsIdentityName, lockIdentityName, requestIndexIdentityName, runGenerationIdentityName}
	boundaries := []string{
		"intent-visible", "intent-durable", "identity-created", "identity-bound-visible", "identity-bound-durable",
		"identity-partial", "identity-complete-visible", "identity-file-durable", "identity-stage-durable",
		"identity-published", "identity-publication-durable", "identity-committed-visible", "identity-committed-durable",
	}
	for _, identity := range identities {
		for _, boundary := range boundaries {
			t.Run(identity+"/"+boundary, func(t *testing.T) {
				root := t.TempDir()
				if err := os.Chmod(root, 0o700); err != nil {
					t.Fatal(err)
				}
				runJournalBootstrapCrashHelper(t, root, identity, boundary)
				authorizedInode := journalInitializingIdentityInode(t, root, identity)

				journal, err := Open(root)
				if err != nil {
					t.Fatalf("recover initial identity %s at %s: %v", identity, boundary, err)
				}
				firstAuthority := append([]byte(nil), journal.rootAuthorityData...)
				if err := journal.Close(); err != nil {
					t.Fatal(err)
				}
				identityPath := journalBootstrapIdentityPath(root, identity)
				info, err := os.Lstat(identityPath)
				if err != nil {
					t.Fatal(err)
				}
				stat, ok := info.Sys().(*syscall.Stat_t)
				if !ok || stat.Nlink != 1 || authorizedInode != 0 && stat.Ino != authorizedInode {
					t.Fatalf("recovered identity inode %s = %+v, authorized=%d", identity, stat, authorizedInode)
				}
				assertNoPreparedIdentityFiles(t, root)

				reopened, err := Open(root)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(reopened.rootAuthorityData, firstAuthority) {
					_ = reopened.Close()
					t.Fatal("repeated startup changed the ready journal generation")
				}
				if err := reopened.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestJournalInitializingIdentityBootstrapRejectsForeignReplacementAndMismatch(t *testing.T) {
	for _, attack := range []struct {
		name     string
		boundary string
		mutate   func(*testing.T, string)
	}{
		{"foreign-final", "intent-durable", func(t *testing.T, root string) {
			if err := os.WriteFile(journalBootstrapIdentityPath(root, actionsIdentityName), []byte("foreign"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"replaced-stage", "identity-bound-durable", func(t *testing.T, root string) {
			stage := journalBootstrapIdentityPath(root, actionsIdentityName) + ".prepared"
			if err := os.Rename(stage, stage+".detached"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stage, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"mismatched-prefix", "identity-partial", func(t *testing.T, root string) {
			stage := journalBootstrapIdentityPath(root, actionsIdentityName) + ".prepared"
			file, err := os.OpenFile(stage, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteAt([]byte("x"), 0); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(attack.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			runJournalBootstrapCrashHelper(t, root, actionsIdentityName, attack.boundary)
			attack.mutate(t, root)
			journal, err := Open(root)
			if journal != nil {
				_ = journal.Close()
				t.Fatal("tampered initializing journal generation opened")
			}
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("tampered initializing journal error = %v", err)
			}
		})
	}
}

func runJournalBootstrapCrashHelper(t *testing.T, root, identity, boundary string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestActionJournalBootstrapCrashHelper$")
	command.Env = append(os.Environ(),
		"ABCP_ACTION_BOOTSTRAP_CRASH_ROOT="+root,
		"ABCP_ACTION_BOOTSTRAP_CRASH_IDENTITY="+identity,
		"ABCP_ACTION_BOOTSTRAP_CRASH_BOUNDARY="+boundary,
	)
	output, err := command.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 80 {
		t.Fatalf("bootstrap crash helper %s/%s exit=%v output=%s", identity, boundary, err, output)
	}
}

func journalInitializingIdentityInode(t *testing.T, root, identity string) uint64 {
	t.Helper()
	fd, err := openAbsoluteDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil {
		t.Fatal("stat bootstrap root")
	}
	authority, _, found, err := readRootGenerationAuthority(fd, journalRootAuthorityXattr, journalRootAuthorityKind,
		uint64(stat.Dev), stat.Ino)
	if err != nil || !found {
		t.Fatalf("read initializing authority: found=%v err=%v", found, err)
	}
	if authority.PreparedIdentity != nil && authority.PreparedIdentity.IdentityName == identity {
		return authority.PreparedIdentity.Objects[authority.PreparedIdentity.IdentityIndex].Inode
	}
	for _, object := range authority.Objects {
		if object.Name == identity {
			return object.Inode
		}
	}
	return 0
}

func journalBootstrapIdentityPath(root, identity string) string {
	if identity == actionsIdentityName {
		return filepath.Join(root, identity)
	}
	return filepath.Join(root, "actions", identity)
}

func assertNoPreparedIdentityFiles(t *testing.T, root string) {
	t.Helper()
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(info.Name(), ".identity.json.prepared") || strings.HasSuffix(info.Name(), resourceSlotIdentityName+".prepared") {
			t.Fatalf("prepared identity survived recovery: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type journalPairedGenerationAttack struct {
	name string
}

func TestJournalCrossProcessPairedGenerationReplacementPreservesRequestHistory(t *testing.T) {
	for _, attack := range []journalPairedGenerationAttack{
		{name: "actions-and-anchor"},
		{name: "request-index-and-anchor"},
		{name: "active-shard-and-anchor"},
		{name: "lock-and-anchor"},
		{name: "lock-identity"},
		{name: "run-history-and-anchor"},
	} {
		t.Run(attack.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			rootInfo, err := os.Lstat(root)
			if err != nil {
				t.Fatal(err)
			}
			rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatal("service root has no stat identity")
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if journal != nil {
					_ = journal.Close()
				}
			}()
			input := journalReceiptInputForRun("paired-generation-history-run")
			receipt, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("paired-generation-history-lease")}, nil
			})
			if err != nil || !created {
				t.Fatalf("initial history receipt=%+v created=%v err=%v", receipt, created, err)
			}
			lookup := LookupKey(input.PrincipalID, input.RequestID)
			identityPath := filepath.Join(root, "actions", requestIndexDirectory, lookup[:2], lookup+".json")
			identityBefore, err := os.ReadFile(identityPath)
			if err != nil {
				t.Fatal(err)
			}
			segmentPath := filepath.Join(root, "actions", input.RunID, "00000001.jsonl")
			segmentBefore, err := os.ReadFile(segmentPath)
			if err != nil {
				t.Fatal(err)
			}
			rootAuthority := append([]byte(nil), journal.rootAuthorityData...)
			shardAuthorityName := journalShardAuthorityXattr(lookup[:2])
			shardAuthority, found, err := fgetRootXattr(journal.rootFD, shardAuthorityName)
			if err != nil || !found {
				t.Fatalf("initial shard authority found=%v err=%v", found, err)
			}

			restore, verifyReplacement := installJournalPairedGenerationAttack(t, root, lookup[:2], attack)
			verifyReplacement()
			if journal.actions == nil || journal.requestIndex == nil || journal.lockFile == nil || journal.shards[int(mustDecodeShardByte(t, lookup[:2]))] == nil {
				t.Fatal("process A lost its pinned original journal generation")
			}

			// Process B must reject even a locally coherent child generation;
			// repeated failed opens also prove descriptor cleanup in that process.
			runJournalOpenIntegrityHelper(t, root)
			verifyReplacement()
			observedRoot, found, err := fgetRootXattr(journal.rootFD, journalRootAuthorityXattr)
			if err != nil || !found || !bytes.Equal(observedRoot, rootAuthority) {
				t.Fatalf("paired attack changed journal root authority: found=%v err=%v", found, err)
			}
			observedShard, found, err := fgetRootXattr(journal.rootFD, shardAuthorityName)
			if err != nil || !found || !bytes.Equal(observedShard, shardAuthority) {
				t.Fatalf("paired attack changed shard authority: found=%v err=%v", found, err)
			}
			rootAfter, err := os.Lstat(root)
			if err != nil {
				t.Fatal(err)
			}
			rootAfterStat, ok := rootAfter.Sys().(*syscall.Stat_t)
			if !ok || rootAfterStat.Dev != rootStat.Dev || rootAfterStat.Ino != rootStat.Ino {
				t.Fatalf("trusted service root changed: before=%+v after=%+v", rootStat, rootAfterStat)
			}

			restore()
			operation, err := journal.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID)
			if err != nil || !reflect.DeepEqual(operation.Receipt, receipt) {
				t.Fatalf("process A lost durable request history: operation=%+v err=%v", operation, err)
			}
			identityAfter, err := os.ReadFile(identityPath)
			if err != nil || !bytes.Equal(identityAfter, identityBefore) {
				t.Fatalf("request identity changed: before=%d after=%d err=%v", len(identityBefore), len(identityAfter), err)
			}
			segmentAfter, err := os.ReadFile(segmentPath)
			if err != nil || !bytes.Equal(segmentAfter, segmentBefore) {
				t.Fatalf("journal history changed: before=%d after=%d err=%v", len(segmentBefore), len(segmentAfter), err)
			}

			conflict := input
			conflict.RunID = "paired-generation-reset-attempt"
			conflict.RequestSHA256 = digestText("paired-generation-reset-intent")
			if _, created, err := journal.CreateReceipt(context.Background(), conflict, func(uint64) (AdmissionBinding, error) {
				t.Fatal("reset attempt reached admission binding")
				return AdmissionBinding{}, nil
			}); !errors.Is(err, ErrConflict) || created {
				t.Fatalf("paired replacement reset global request identity: created=%v err=%v", created, err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			journal = nil

			reopened, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			operation, err = reopened.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID)
			if err != nil || !reflect.DeepEqual(operation.Receipt, receipt) {
				_ = reopened.Close()
				t.Fatalf("fresh process view lost durable request history: operation=%+v err=%v", operation, err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestActionJournalOpenIntegrityHelper(t *testing.T) {
	root := os.Getenv("ABCP_ACTION_OPEN_INTEGRITY_HELPER")
	if root == "" {
		return
	}
	assertRejectedOpen := func() {
		t.Helper()
		journal, err := Open(root)
		if journal != nil {
			_ = journal.Close()
			t.Fatal("paired replacement journal unexpectedly opened")
		}
		if !errors.Is(err, ErrIntegrity) {
			t.Fatalf("paired replacement journal open error = %v", err)
		}
	}
	assertRejectedOpen()
	before := countJournalHelperDescriptors(t)
	for attempt := 0; attempt < 8; attempt++ {
		assertRejectedOpen()
	}
	if after := countJournalHelperDescriptors(t); after != before {
		t.Fatalf("failed journal opens leaked descriptors: before=%d after=%d", before, after)
	}
}

func TestActionJournalNamespacePinHelper(t *testing.T) {
	root := os.Getenv("ABCP_ACTION_NAMESPACE_HELPER")
	if root == "" {
		return
	}
	journal, err := Open(root)
	if err != nil {
		os.Exit(30)
	}
	input := journalReceiptInputForRun("namespace-original-run")
	if _, err := journal.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID); err != nil {
		os.Exit(31)
	}
	guard, err := journal.acquireActions(context.Background())
	if err != nil {
		os.Exit(32)
	}
	fmt.Println("held")
	var signal string
	if _, err := fmt.Fscanln(os.Stdin, &signal); err != nil || signal != "continue" {
		os.Exit(33)
	}
	if err := guard.close(); !errors.Is(err, ErrIntegrity) {
		os.Exit(34)
	}
	fmt.Println("integrity")
	if err := journal.Close(); err != nil {
		os.Exit(35)
	}
}

func TestActionJournalRunNamespaceHelper(t *testing.T) {
	root := os.Getenv("ABCP_ACTION_RUN_NAMESPACE_HELPER")
	if root == "" {
		return
	}
	runID := os.Getenv("ABCP_ACTION_RUN_NAMESPACE_RUN")
	operationID := os.Getenv("ABCP_ACTION_RUN_NAMESPACE_OPERATION")
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime.Add(time.Minute) })
	if err != nil {
		os.Exit(40)
	}
	guard, err := journal.acquireRun(context.Background(), runID, false)
	if err != nil {
		os.Exit(41)
	}
	state, err := guard.scan()
	if err != nil || state.operations[operationID] == nil || state.operations[operationID].Status != StatusClaimed {
		os.Exit(42)
	}
	fmt.Println("held")
	var signal string
	if _, err := fmt.Fscanln(os.Stdin, &signal); err != nil || signal != "append" {
		os.Exit(43)
	}
	outcome := ActionOutcomeV1{Kind: "ActionOutcomeV1", SchemaVersion: JournalSchemaVersion,
		OperationID: operationID, Status: StatusApplied, RecordedAt: canonicalTime(journalTestTime.Add(time.Minute)),
		AuthoritativeEventIDs: []string{state.operations[operationID].Claim.ControllerEventID, "detached-outcome-event"}}
	appendErr := guard.append(state, &outcome)
	closeErr := guard.close()
	if !errors.Is(appendErr, ErrIntegrity) || !errors.Is(closeErr, ErrIntegrity) {
		os.Exit(44)
	}
	fmt.Println("integrity")
	if err := journal.Close(); err != nil {
		os.Exit(45)
	}
}

func TestActionJournalRunIssuanceCrashHelper(t *testing.T) {
	root := os.Getenv("ABCP_ACTION_RUN_ISSUANCE_CRASH_ROOT")
	if root == "" {
		return
	}
	boundary := os.Getenv("ABCP_ACTION_RUN_ISSUANCE_CRASH_BOUNDARY")
	runID := os.Getenv("ABCP_ACTION_RUN_ISSUANCE_CRASH_RUN")
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime.Add(time.Hour) })
	if err != nil {
		os.Exit(60)
	}
	defaults := defaultJournalIO()
	journal.io.syncDir = func(object string, fd int) error {
		before := boundary == "authority-before-sync" && object == "run-generation-authority"
		if before {
			os.Exit(70)
		}
		if err := defaults.syncDir(object, fd); err != nil {
			return err
		}
		if (boundary == "unbound-stage" && object == "directory") ||
			(boundary == "prepared-authority" && object == "run-generation-prepare-authority") ||
			(boundary == "published-directory" && object == "run-generation-directory") ||
			(boundary == "authority-after-sync" && object == "run-generation-authority") {
			os.Exit(70)
		}
		return nil
	}
	journal.io.write = func(object string, file *os.File, data []byte) error {
		if boundary == "partial-history" && object == "run-generation-history" {
			if err := defaults.write(object, file, data[:len(data)/2]); err != nil {
				return err
			}
			if err := defaults.syncFile(object, file); err != nil {
				return err
			}
			os.Exit(70)
		}
		return defaults.write(object, file, data)
	}
	journal.io.syncFile = func(object string, file *os.File) error {
		if err := defaults.syncFile(object, file); err != nil {
			return err
		}
		if boundary == "complete-history" && object == "run-generation-history" {
			os.Exit(70)
		}
		return nil
	}
	input := journalReceiptInputForRun(runID)
	input.RequestID = "request-" + boundary
	input.RequestSHA256 = digestText(input.RequestID)
	if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("crash-issuance-lease")}, nil
	}); err != nil {
		os.Exit(61)
	}
	os.Exit(62)
}

func TestJournalRunGenerationCrashRecoveryAtEveryIssuanceBoundary(t *testing.T) {
	for _, boundary := range []string{
		"unbound-stage",
		"prepared-authority",
		"published-directory",
		"partial-history",
		"complete-history",
		"authority-before-sync",
		"authority-after-sync",
	} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
			if err != nil {
				t.Fatal(err)
			}
			prior := journalReceiptInputForRun("run-issuance-committed-prefix")
			prior.Action = "decision"
			prior.ExpectedState = "HUMAN_DECISION_REQUIRED"
			prior.DelegatedActor = &DelegatedActorV1{SubjectID: "human-1", SubjectType: "user"}
			prior.Payload = json.RawMessage(`{"decision_request_id":"run-issuance-decision","answer":"approve"}`)
			prior.PolicyVersion = "ep006-governed-action-v1"
			prior.AuthorityGrantSHA256 = digestText("run-issuance-grant")
			priorReceipt, created, err := journal.CreateReceipt(context.Background(), prior, nil)
			if err != nil || !created {
				_ = journal.Close()
				t.Fatalf("prior receipt=%+v created=%v err=%v", priorReceipt, created, err)
			}
			priorClaim, created, err := journal.CreateClaim(context.Background(), prior.RunID, priorReceipt.OperationID, "", DecisionEventDomain)
			if err != nil || !created {
				_ = journal.Close()
				t.Fatalf("prior claim=%+v created=%v err=%v", priorClaim, created, err)
			}
			if _, err := journal.AppendOutcome(context.Background(), prior.RunID, priorReceipt.OperationID, StatusApplied,
				[]string{priorClaim.ControllerEventID, "run-issuance-outcome"}, ""); err != nil {
				_ = journal.Close()
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}

			runID := "run-issuance-crash-" + boundary
			runActionJournalIssuanceCrashHelper(t, root, boundary, runID)
			actions := filepath.Join(root, "actions")
			stagePath := filepath.Join(actions, runGenerationPreparedName)
			finalPath := filepath.Join(actions, storageComponent(runID))
			preparedPath := stagePath
			if boundary != "unbound-stage" && boundary != "prepared-authority" {
				preparedPath = finalPath
			}
			preparedInfo, err := os.Stat(preparedPath)
			if err != nil {
				t.Fatalf("prepared inode at %s: %v", preparedPath, err)
			}
			preparedStat, ok := preparedInfo.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatal("prepared run has no stat identity")
			}
			rootFD, err := openAbsoluteDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			var rootStat syscall.Stat_t
			if err := syscall.Fstat(rootFD, &rootStat); err != nil {
				_ = syscall.Close(rootFD)
				t.Fatal(err)
			}
			preparedAuthority, _, found, authorityErr := readRootGenerationAuthority(rootFD, journalRunAuthorityXattr,
				journalRunAuthorityKind, uint64(rootStat.Dev), rootStat.Ino)
			_ = syscall.Close(rootFD)
			if authorityErr != nil || !found {
				t.Fatalf("prepared authority found=%v authority=%+v err=%v", found, preparedAuthority, authorityErr)
			}

			restarted, err := OpenWithClock(root, func() time.Time { return journalTestTime.Add(2 * time.Hour) })
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if _, err := os.Stat(stagePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("prepared staging name survived recovery: %v", err)
			}
			input := journalReceiptInputForRun(runID)
			input.RequestID = "request-" + boundary
			input.RequestSHA256 = digestText(input.RequestID)
			binderCalls := 0
			receipt, created, err := restarted.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{OwnerLeaseID: digestText("run-issuance-recovered-lease")}, nil
			})
			if err != nil || !created || binderCalls != 1 {
				t.Fatalf("recovered receipt=%+v created=%v binderCalls=%d err=%v", receipt, created, binderCalls, err)
			}
			finalInfo, err := os.Stat(finalPath)
			if err != nil {
				t.Fatal(err)
			}
			finalStat, ok := finalInfo.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatal("recovered run has no stat identity")
			}
			if boundary != "unbound-stage" && (finalStat.Dev != preparedStat.Dev || finalStat.Ino != preparedStat.Ino) {
				t.Fatalf("recovery reissued prepared inode: prepared=%d:%d final=%d:%d",
					preparedStat.Dev, preparedStat.Ino, finalStat.Dev, finalStat.Ino)
			}
			if replayed, created, err := restarted.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				binderCalls++
				return AdmissionBinding{}, errors.New("recovered request must not rebind")
			}); err != nil || created || !reflect.DeepEqual(replayed, receipt) || binderCalls != 1 {
				t.Fatalf("recovered replay=%+v created=%v binderCalls=%d err=%v", replayed, created, binderCalls, err)
			}
			priorOperation, err := restarted.Read(context.Background(), prior.RunID, priorReceipt.OperationID)
			if err != nil || priorOperation.Status != StatusApplied || len(priorOperation.Outcomes) != 1 {
				t.Fatalf("committed outcome=%+v err=%v", priorOperation, err)
			}
			duplicate := prior
			duplicate.RequestID = "run-issuance-duplicate-" + boundary
			duplicate.RequestSHA256 = digestText(duplicate.RequestID)
			if _, created, err := restarted.CreateReceipt(context.Background(), duplicate, nil); !errors.Is(err, ErrDecisionExists) || created {
				t.Fatalf("decision uniqueness created=%v err=%v", created, err)
			}
			authorityData, found, err := fgetRootXattr(restarted.rootFD, journalRunAuthorityXattr)
			if err != nil || !found {
				t.Fatalf("run authority found=%v err=%v", found, err)
			}
			authority, _, established, err := decodeRootGenerationAuthority(authorityData, journalRunAuthorityKind,
				restarted.rootDev, restarted.rootIno)
			if err != nil || !established || authority.IssuanceCount != 2 || authority.PreparedRunGeneration != nil {
				t.Fatalf("recovered authority=%+v established=%v err=%v", authority, established, err)
			}
			history, err := os.ReadFile(filepath.Join(actions, runGenerationHistoryName))
			if err != nil || len(bytes.Split(bytes.TrimSuffix(history, []byte("\n")), []byte("\n"))) != 2 {
				t.Fatalf("recovered history bytes=%d err=%v", len(history), err)
			}
		})
	}
}

func TestJournalPreparedRunGenerationReplacementFailsClosed(t *testing.T) {
	for _, boundary := range []string{"prepared-authority", "published-directory"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			runID := "run-issuance-replacement-" + boundary
			runActionJournalIssuanceCrashHelper(t, root, boundary, runID)
			preparedPath := filepath.Join(root, "actions", runGenerationPreparedName)
			if boundary == "published-directory" {
				preparedPath = filepath.Join(root, "actions", storageComponent(runID))
			}
			detached := preparedPath + ".detached"
			if err := os.Rename(preparedPath, detached); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(preparedPath, 0o700); err != nil {
				t.Fatal(err)
			}
			fresh, openErr := Open(root)
			if fresh != nil {
				_ = fresh.Close()
				t.Fatal("fresh open adopted a replacement prepared inode")
			}
			if !errors.Is(openErr, ErrIntegrity) {
				t.Fatalf("replacement prepared inode error=%v", openErr)
			}
			if _, err := os.Stat(detached); err != nil {
				t.Fatalf("exact detached prepared inode was modified: %v", err)
			}
		})
	}
}

func runActionJournalIssuanceCrashHelper(t *testing.T, root, boundary, runID string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestActionJournalRunIssuanceCrashHelper$")
	command.Env = append(os.Environ(),
		"ABCP_ACTION_RUN_ISSUANCE_CRASH_ROOT="+root,
		"ABCP_ACTION_RUN_ISSUANCE_CRASH_BOUNDARY="+boundary,
		"ABCP_ACTION_RUN_ISSUANCE_CRASH_RUN="+runID)
	output, err := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 70 {
		t.Fatalf("issuance crash boundary %s exit=%v output=%s", boundary, err, output)
	}
}

func TestJournalRunGenerationFreshOpenRejectsLossAndContentCoherentReplacement(t *testing.T) {
	for _, attack := range []string{"loss", "content-coherent-replacement"} {
		t.Run(attack, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
			if err != nil {
				t.Fatal(err)
			}
			input := journalReceiptInputForRun("durable-run-generation")
			receipt, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("durable-run-generation-lease")}, nil
			})
			if err != nil || !created {
				t.Fatalf("receipt=%+v created=%v err=%v", receipt, created, err)
			}
			claim, created, err := journal.CreateClaim(context.Background(), input.RunID, receipt.OperationID,
				receipt.OwnerLeaseID, CancelEventDomain)
			if err != nil || !created {
				t.Fatalf("claim=%+v created=%v err=%v", claim, created, err)
			}
			if _, err := journal.AppendOutcome(context.Background(), input.RunID, receipt.OperationID, StatusApplied,
				[]string{claim.ControllerEventID, "durable-outcome-event"}, ""); err != nil {
				t.Fatal(err)
			}
			authorityBefore, found, err := fgetRootXattr(journal.rootFD, journalRunAuthorityXattr)
			if err != nil || !found {
				t.Fatalf("run authority found=%v err=%v", found, err)
			}
			authority, _, established, err := decodeRootGenerationAuthority(authorityBefore, journalRunAuthorityKind,
				journal.rootDev, journal.rootIno)
			if err != nil || !established || authority.IssuanceCount != 1 || !validDigest(authority.IssuanceFinalRecordSHA256) {
				t.Fatalf("run authority=%+v established=%v err=%v", authority, established, err)
			}
			historyBefore, err := os.ReadFile(filepath.Join(root, "actions", runGenerationHistoryName))
			if err != nil {
				t.Fatal(err)
			}
			runPath := filepath.Join(root, "actions", storageComponent(input.RunID))
			segmentBefore, err := os.ReadFile(filepath.Join(runPath, "00000001.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			detached := runPath + ".detached"
			if err := os.Rename(runPath, detached); err != nil {
				t.Fatal(err)
			}
			if attack == "content-coherent-replacement" {
				if err := os.Mkdir(runPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(runPath, "00000001.jsonl"), segmentBefore, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fresh, openErr := Open(root)
			if fresh != nil {
				_ = fresh.Close()
				t.Fatal("fresh process accepted a reissued run namespace")
			}
			if !errors.Is(openErr, ErrIntegrity) {
				t.Fatalf("fresh replacement open error=%v", openErr)
			}
			rootFD, err := openAbsoluteDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			authorityAfter, found, authorityErr := fgetRootXattr(rootFD, journalRunAuthorityXattr)
			_ = syscall.Close(rootFD)
			if authorityErr != nil || !found || !bytes.Equal(authorityAfter, authorityBefore) {
				t.Fatalf("failed open changed run authority: found=%v err=%v", found, authorityErr)
			}
			historyAfter, err := os.ReadFile(filepath.Join(root, "actions", runGenerationHistoryName))
			if err != nil || !bytes.Equal(historyAfter, historyBefore) {
				t.Fatalf("failed open changed run history: before=%d after=%d err=%v", len(historyBefore), len(historyAfter), err)
			}
			detachedSegment, err := os.ReadFile(filepath.Join(detached, "00000001.jsonl"))
			if err != nil || !bytes.Equal(detachedSegment, segmentBefore) {
				t.Fatalf("detached operation history changed: before=%d after=%d err=%v", len(segmentBefore), len(detachedSegment), err)
			}
		})
	}
}

func TestJournalCrossProcessRunReplacementCannotRedirectOutcomeOrDecisionUniqueness(t *testing.T) {
	root, journal, receipt := createClaimedDecisionJournal(t, "cross-process-run-generation")
	defer journal.Close()
	runPath := filepath.Join(root, "actions", storageComponent(receipt.RunID))
	segmentBefore, err := os.ReadFile(filepath.Join(runPath, "00000001.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestActionJournalRunNamespaceHelper$")
	command.Env = append(os.Environ(), "ABCP_ACTION_RUN_NAMESPACE_HELPER="+root,
		"ABCP_ACTION_RUN_NAMESPACE_RUN="+receipt.RunID, "ABCP_ACTION_RUN_NAMESPACE_OPERATION="+receipt.OperationID)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	output := bufio.NewReader(stdout)
	if line, err := output.ReadString('\n'); err != nil || line != "held\n" {
		t.Fatalf("run helper readiness=%q err=%v", line, err)
	}
	detached := runPath + ".detached"
	if err := os.Rename(runPath, detached); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runPath, "00000001.jsonl"), segmentBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	freshWhileHeld, openErr := Open(root)
	if freshWhileHeld != nil {
		_ = freshWhileHeld.Close()
		t.Fatal("fresh process accepted replacement while the original guard was live")
	}
	if !errors.Is(openErr, ErrIntegrity) {
		t.Fatalf("fresh live-replacement open error=%v", openErr)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err = journal.Read(waitContext, receipt.RunID, receipt.OperationID)
	cancel()
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("replacement contender bypassed original authority: %v", err)
	}
	if _, err := fmt.Fprintln(stdin, "append"); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if line, err := output.ReadString('\n'); err != nil || line != "integrity\n" {
		t.Fatalf("run helper result=%q err=%v", line, err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("run helper exit=%v", err)
	}
	finished = true
	detachedAfter, err := os.ReadFile(filepath.Join(detached, "00000001.jsonl"))
	if err != nil || !bytes.Equal(detachedAfter, segmentBefore) {
		t.Fatalf("detached guard wrote an outcome: before=%d after=%d err=%v", len(segmentBefore), len(detachedAfter), err)
	}
	replacementAfter, err := os.ReadFile(filepath.Join(runPath, "00000001.jsonl"))
	if err != nil || !bytes.Equal(replacementAfter, segmentBefore) {
		t.Fatalf("detached guard redirected an outcome: before=%d after=%d err=%v", len(segmentBefore), len(replacementAfter), err)
	}
	duplicate := journalReceiptInputForRun(receipt.RunID)
	duplicate.RequestID = "replacement-decision-request"
	duplicate.RequestSHA256 = digestText("replacement-decision-intent")
	duplicate.Action = "decision"
	duplicate.ExpectedState = "HUMAN_DECISION_REQUIRED"
	duplicate.DelegatedActor = &DelegatedActorV1{SubjectID: "human-2", SubjectType: "user"}
	duplicate.Payload = json.RawMessage(`{"decision_request_id":"decision-origin-1","answer":"reject"}`)
	duplicate.PolicyVersion = "ep006-governed-action-v1"
	duplicate.AuthorityGrantSHA256 = digestText("replacement-grant")
	binderCalls := 0
	if _, created, err := journal.CreateReceipt(context.Background(), duplicate, func(uint64) (AdmissionBinding, error) {
		binderCalls++
		return AdmissionBinding{}, nil
	}); !errors.Is(err, ErrIntegrity) || created {
		t.Fatalf("replacement decision uniqueness created=%v err=%v", created, err)
	}
	if binderCalls != 0 {
		t.Fatalf("replacement decision reached binding %d times", binderCalls)
	}
	fresh, openErr := Open(root)
	if fresh != nil {
		_ = fresh.Close()
		t.Fatal("fresh process accepted redirected decision history")
	}
	if !errors.Is(openErr, ErrIntegrity) {
		t.Fatalf("fresh redirected decision open error=%v", openErr)
	}
}

func TestJournalCrossProcessNamespaceReplacementFailsClosed(t *testing.T) {
	for _, replacement := range []string{"actions", "lock", "request-index", "active-shard", "run-history"} {
		t.Run(replacement, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			journal, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			input := journalReceiptInputForRun("namespace-original-run")
			if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
				return AdmissionBinding{OwnerLeaseID: digestText("namespace-original-lease")}, nil
			}); err != nil {
				t.Fatal(err)
			}

			command := exec.Command(os.Args[0], "-test.run=^TestActionJournalNamespacePinHelper$")
			command.Env = append(os.Environ(), "ABCP_ACTION_NAMESPACE_HELPER="+root)
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			finished := false
			defer func() {
				if !finished {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			output := bufio.NewReader(stdout)
			if line, err := output.ReadString('\n'); err != nil || line != "held\n" {
				t.Fatalf("namespace helper readiness = %q, err=%v", line, err)
			}

			actions := filepath.Join(root, "actions")
			indexRoot := filepath.Join(actions, requestIndexDirectory)
			lookup := LookupKey(input.PrincipalID, input.RequestID)
			switch replacement {
			case "actions":
				if err := os.Rename(actions, actions+".replaced"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(actions, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(actions, ".lock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(actions, requestIndexDirectory), 0o700); err != nil {
					t.Fatal(err)
				}
			case "lock":
				lockPath := filepath.Join(actions, ".lock")
				if err := os.Rename(lockPath, lockPath+".replaced"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "request-index":
				if err := os.Rename(indexRoot, indexRoot+".replaced"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(indexRoot, 0o700); err != nil {
					t.Fatal(err)
				}
			case "active-shard":
				shardPath := filepath.Join(indexRoot, lookup[:2])
				if err := os.Rename(shardPath, shardPath+".replaced"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(shardPath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "run-history":
				historyPath := filepath.Join(actions, runGenerationHistoryName)
				if err := os.Rename(historyPath, historyPath+".replaced"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(historyPath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			// The helper retains the original root-anchored authority. A local
			// contender must wait on that authority instead of locking any
			// replacement inode.
			waitContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_, err = journal.ReadByRequest(waitContext, input.RunID, input.PrincipalID, input.RequestID)
			cancel()
			if !errors.Is(err, ErrBusy) {
				t.Fatalf("replacement contender error = %v", err)
			}
			if _, err := fmt.Fprintln(stdin, "continue"); err != nil {
				t.Fatal(err)
			}
			if err := stdin.Close(); err != nil {
				t.Fatal(err)
			}
			if line, err := output.ReadString('\n'); err != nil || line != "integrity\n" {
				t.Fatalf("namespace helper result = %q, err=%v", line, err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("namespace helper exit = %v", err)
			}
			finished = true

			if _, err := journal.ReadByRequest(context.Background(), input.RunID, input.PrincipalID, input.RequestID); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("pinned journal replacement error = %v", err)
			}
			fresh, openErr := Open(root)
			if !errors.Is(openErr, ErrIntegrity) {
				if fresh != nil {
					_ = fresh.Close()
				}
				t.Fatalf("fresh journal replacement open error = %v", openErr)
			}
		})
	}
}

func installJournalPairedGenerationAttack(t *testing.T, root, shard string, attack journalPairedGenerationAttack) (func(), func()) {
	t.Helper()
	backup := filepath.Join(root, ".journal-paired-generation-backup")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	actions := filepath.Join(root, "actions")
	index := filepath.Join(actions, requestIndexDirectory)
	var object, anchor, name string
	directory, replaceObject := false, true
	switch attack.name {
	case "actions-and-anchor":
		object, anchor, name, directory = actions, filepath.Join(root, actionsIdentityName), "actions", true
	case "request-index-and-anchor":
		object, anchor, name, directory = index, filepath.Join(actions, requestIndexIdentityName), requestIndexDirectory, true
	case "active-shard-and-anchor":
		object, anchor, name, directory = filepath.Join(index, shard), filepath.Join(index, "."+shard+".identity.json"), shard, true
	case "lock-and-anchor":
		object, anchor, name = filepath.Join(actions, ".lock"), filepath.Join(actions, lockIdentityName), ".lock"
	case "lock-identity":
		object, anchor, name, replaceObject = filepath.Join(actions, ".lock"), filepath.Join(actions, lockIdentityName), ".lock", false
	case "run-history-and-anchor":
		object, anchor, name = filepath.Join(actions, runGenerationHistoryName), filepath.Join(actions, runGenerationIdentityName), runGenerationHistoryName
	default:
		t.Fatalf("unknown journal paired-generation attack %q", attack.name)
	}
	backupObject := filepath.Join(backup, "object")
	backupAnchor := filepath.Join(backup, "anchor")
	if replaceObject {
		if err := os.Rename(object, backupObject); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(anchor, backupAnchor); err != nil {
		t.Fatal(err)
	}

	if attack.name == "actions-and-anchor" {
		createReplacementJournalActions(t, root)
	} else {
		if replaceObject {
			if directory {
				if err := os.Mkdir(object, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(object, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		writeReplacementJournalNamespaceAnchor(t, object, anchor, name, directory)
	}

	verify := func() {
		switch attack.name {
		case "actions-and-anchor":
			assertReplacementJournalNamespace(t, actions, filepath.Join(root, actionsIdentityName), "actions", true)
			assertReplacementJournalNamespace(t, filepath.Join(actions, ".lock"), filepath.Join(actions, lockIdentityName), ".lock", false)
			assertReplacementJournalNamespace(t, filepath.Join(actions, requestIndexDirectory), filepath.Join(actions, requestIndexIdentityName), requestIndexDirectory, true)
			assertReplacementJournalNamespace(t, filepath.Join(actions, runGenerationHistoryName), filepath.Join(actions, runGenerationIdentityName), runGenerationHistoryName, false)
			entries, err := os.ReadDir(filepath.Join(actions, requestIndexDirectory))
			if err != nil || len(entries) != 0 {
				t.Fatalf("replacement actions established request identities: entries=%d err=%v", len(entries), err)
			}
		case "request-index-and-anchor", "active-shard-and-anchor":
			assertReplacementJournalNamespace(t, object, anchor, name, true)
			entries, err := os.ReadDir(object)
			if err != nil || len(entries) != 0 {
				t.Fatalf("replacement %s established request identities: entries=%d err=%v", name, len(entries), err)
			}
		case "lock-and-anchor", "lock-identity", "run-history-and-anchor":
			assertReplacementJournalNamespace(t, object, anchor, name, false)
		}
	}
	restore := func() {
		if replaceObject {
			if directory {
				if err := os.RemoveAll(object); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(object); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(anchor); err != nil {
			t.Fatal(err)
		}
		if replaceObject {
			if err := os.Rename(backupObject, object); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Rename(backupAnchor, anchor); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(backup); err != nil {
			t.Fatal(err)
		}
	}
	return restore, verify
}

func createReplacementJournalActions(t *testing.T, root string) {
	t.Helper()
	actions := filepath.Join(root, "actions")
	if err := os.Mkdir(actions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeReplacementJournalNamespaceAnchor(t, actions, filepath.Join(root, actionsIdentityName), "actions", true)
	lock := filepath.Join(actions, ".lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeReplacementJournalNamespaceAnchor(t, lock, filepath.Join(actions, lockIdentityName), ".lock", false)
	index := filepath.Join(actions, requestIndexDirectory)
	if err := os.Mkdir(index, 0o700); err != nil {
		t.Fatal(err)
	}
	writeReplacementJournalNamespaceAnchor(t, index, filepath.Join(actions, requestIndexIdentityName), requestIndexDirectory, true)
	history := filepath.Join(actions, runGenerationHistoryName)
	if err := os.WriteFile(history, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeReplacementJournalNamespaceAnchor(t, history, filepath.Join(actions, runGenerationIdentityName), runGenerationHistoryName, false)
}

func writeReplacementJournalNamespaceAnchor(t *testing.T, object, anchor, name string, directory bool) {
	t.Helper()
	info, err := os.Lstat(object)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("replacement journal object %s has no stat identity", object)
	}
	objectType := "file"
	if directory {
		objectType = "directory"
	}
	identity := namespaceIdentityV1{Kind: "ActionNamespaceIdentityV1", SchemaVersion: 1, Name: name,
		ObjectType: objectType, Device: uint64(stat.Dev), Inode: stat.Ino}
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(anchor, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertReplacementJournalNamespace(t *testing.T, object, anchor, name string, directory bool) {
	t.Helper()
	info, err := os.Lstat(object)
	if err != nil {
		t.Fatal(err)
	}
	if directory {
		if !info.IsDir() || info.Mode().Perm() != 0o700 || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("unsafe replacement journal directory %s: info=%v", object, info)
		}
	} else if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("unsafe replacement journal file %s: info=%v", object, info)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !directory && stat.Nlink != 1 {
		t.Fatalf("unsafe replacement journal identity %s: stat=%+v", object, stat)
	}
	data, err := os.ReadFile(anchor)
	if err != nil {
		t.Fatal(err)
	}
	var identity namespaceIdentityV1
	if json.Unmarshal(data, &identity) != nil {
		t.Fatalf("invalid replacement journal anchor %s", anchor)
	}
	canonical, marshalErr := json.Marshal(identity)
	objectType := "file"
	if directory {
		objectType = "directory"
	}
	if marshalErr != nil || !bytes.Equal(canonical, data) || identity.Kind != "ActionNamespaceIdentityV1" || identity.SchemaVersion != 1 ||
		identity.Name != name || identity.ObjectType != objectType || identity.Device != uint64(stat.Dev) || identity.Inode != stat.Ino {
		t.Fatalf("replacement journal anchor %s = %+v, marshalErr=%v", anchor, identity, marshalErr)
	}
	anchorInfo, err := os.Lstat(anchor)
	if err != nil || !anchorInfo.Mode().IsRegular() || anchorInfo.Mode().Perm() != 0o600 {
		t.Fatalf("unsafe replacement journal anchor %s: info=%v err=%v", anchor, anchorInfo, err)
	}
	anchorStat, ok := anchorInfo.Sys().(*syscall.Stat_t)
	if !ok || anchorStat.Nlink != 1 {
		t.Fatalf("unsafe replacement journal anchor identity %s: stat=%+v", anchor, anchorStat)
	}
}

func runJournalOpenIntegrityHelper(t *testing.T, root string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestActionJournalOpenIntegrityHelper$")
	command.Env = append(os.Environ(), "ABCP_ACTION_OPEN_INTEGRITY_HELPER="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("paired replacement journal helper failed: %v\n%s", err, output)
	}
}

func countJournalHelperDescriptors(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func mustDecodeShardByte(t *testing.T, shard string) byte {
	t.Helper()
	decoded, err := hex.DecodeString(shard)
	if err != nil || len(decoded) != 1 {
		t.Fatalf("invalid shard %q: %v", shard, err)
	}
	return decoded[0]
}

func journalReceiptInput(requestID, requestDigest string) ReceiptInput {
	input := journalReceiptInputForRun("journal-run")
	input.RequestID, input.RequestSHA256 = requestID, requestDigest
	return input
}

func journalReceiptInputForRun(runID string) ReceiptInput {
	return ReceiptInput{PrincipalID: "service-principal", PrincipalType: "service", RequestID: "request-1", RequestSHA256: digestText("request"),
		Action: "cancel", RunID: runID, AttemptID: "attempt-1", ExpectedState: "IMPLEMENTING", ExpectedRevision: digestText("revision"),
		Payload: json.RawMessage(`{}`), StateTransitionID: "transition-event-1"}
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func buildNearlyFullSegment(t *testing.T, runID string) []byte {
	t.Helper()
	data, _, _ := buildNearlyFullEpoch(t, runID, 1, 1, "")
	return data
}

func buildNearlyFullEpoch(t *testing.T, runID string, epoch, firstSequence uint64, prior string) ([]byte, uint64, string) {
	t.Helper()
	var result []byte
	sequence := firstSequence
	first := true
	for {
		record := fixtureReceipt(runID, sequence, strings.Repeat("x", 15_000))
		record.Epoch = epoch
		if first {
			record.PriorSegmentFinalRecordSHA256 = prior
		}
		line, _ := json.Marshal(record)
		line = append(line, '\n')
		if MaxSegmentBytes-len(result) <= 17_000 {
			break
		}
		result = append(result, line...)
		sequence++
		first = false
	}
	base := fixtureReceipt(runID, sequence, "")
	base.Epoch = epoch
	if first {
		base.PriorSegmentFinalRecordSHA256 = prior
	}
	baseLine, _ := json.Marshal(base)
	remaining := MaxSegmentBytes - 1 - len(result)
	padding := remaining - len(baseLine) - 1
	if padding < 0 || padding > 16_000 {
		t.Fatalf("rollover padding = %d", padding)
	}
	base.Payload = json.RawMessage(`{"decision_request_id":"decision-origin-` + fmt.Sprint(sequence) + `","answer":"` + strings.Repeat("y", padding) + `"}`)
	line, _ := json.Marshal(base)
	result = append(result, line...)
	result = append(result, '\n')
	return result, sequence + 1, digestText(string(line))
}

func fixtureReceipt(runID string, sequence uint64, padding string) ActionReceiptV1 {
	requestID := fmt.Sprintf("request-%d", sequence)
	return ActionReceiptV1{Kind: "ActionReceiptV1", SchemaVersion: 1, Epoch: 1, Sequence: sequence,
		LookupKeySHA256: LookupKey("service-principal", requestID), OperationID: fmt.Sprintf("operation-%d", sequence),
		PrincipalID: "service-principal", PrincipalType: "service", RequestID: requestID, RequestSHA256: digestText(requestID), Action: "decision",
		RunID: runID, AttemptID: "attempt-1", ExpectedState: "IMPLEMENTING", ExpectedRevision: digestText("revision"),
		DelegatedActor: &DelegatedActorV1{SubjectID: "human-1", SubjectType: "user"},
		Payload:        json.RawMessage(`{"decision_request_id":"decision-origin-` + fmt.Sprint(sequence) + `","answer":"` + padding + `"}`), PolicyVersion: "ep006-governed-action-v1", AuthorityGrantSHA256: digestText("grant"),
		AdmittedStateTransitionID: "transition-event-1",
		ReceivedAt:                canonicalTime(journalTestTime)}
}
