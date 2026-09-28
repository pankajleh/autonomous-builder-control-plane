//go:build linux

package actioncontrol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Request-index shards used to keep their generation authority on the service-root inode. ext4 gives one inode
// about 4 KiB of extended attributes, so the third or fourth shard could not be recorded and the half-created shard
// then failed every later open. Each established shard now keeps its authority on its own directory.

func openCapacityJournal(t *testing.T, root string) *Journal {
	t.Helper()
	journal, err := OpenWithClock(root, func() time.Time { return journalTestTime })
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	return journal
}

func capacityRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// distinctShardInputs returns receipt inputs whose requests land in count different request-index shards.
func distinctShardInputs(count int) []ReceiptInput {
	inputs := make([]ReceiptInput, 0, count)
	seen := make(map[string]bool, count)
	for candidate := 0; len(inputs) < count; candidate++ {
		input := journalReceiptInputForRun(fmt.Sprintf("capacity-run-%d", len(inputs)))
		input.RequestID = fmt.Sprintf("capacity-request-%d", candidate)
		input.RequestSHA256 = digestText(input.RequestID)
		shard := LookupKey(input.PrincipalID, input.RequestID)[:2]
		if seen[shard] {
			continue
		}
		seen[shard] = true
		inputs = append(inputs, input)
	}
	return inputs
}

func createCapacityReceipt(t *testing.T, journal *Journal, input ReceiptInput) ActionReceiptV1 {
	t.Helper()
	receipt, created, err := journal.CreateReceipt(context.Background(), input, func(uint64) (AdmissionBinding, error) {
		return AdmissionBinding{OwnerLeaseID: digestText("lease-" + input.RequestID)}, nil
	})
	if err != nil || !created {
		t.Fatalf("receipt for shard %s: created=%v err=%v", LookupKey(input.PrincipalID, input.RequestID)[:2], created, err)
	}
	return receipt
}

func rootXattrNames(t *testing.T, root string) []string {
	t.Helper()
	size, err := syscall.Listxattr(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, size)
	size, err = syscall.Listxattr(root, buffer)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, name := range strings.Split(string(buffer[:size]), "\x00") {
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func shardPath(root, shard string) string {
	return filepath.Join(root, "actions", requestIndexDirectory, shard)
}

func readPathXattr(t *testing.T, path, name string) ([]byte, bool) {
	t.Helper()
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	data, found, err := fgetRootXattr(fd, name)
	if err != nil {
		t.Fatalf("read %s on %s: %v", name, path, err)
	}
	return data, found
}

func writePathXattr(t *testing.T, path, name string, data []byte) {
	t.Helper()
	if err := syscall.Setxattr(path, name, data, 0); err != nil {
		t.Fatalf("write %s on %s: %v", name, path, err)
	}
}

func removePathXattr(t *testing.T, path, name string) {
	t.Helper()
	if err := syscall.Removexattr(path, name); err != nil {
		t.Fatalf("remove %s on %s: %v", name, path, err)
	}
}

func TestJournalRequestShardsKeepTheServiceRootWithinItsXattrSpace(t *testing.T) {
	root := capacityRoot(t)
	journal := openCapacityJournal(t, root)
	inputs := distinctShardInputs(32)
	receipts := make([]ActionReceiptV1, len(inputs))
	for index, input := range inputs {
		receipts[index] = createCapacityReceipt(t, journal, input)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range rootXattrNames(t, root) {
		if strings.Contains(name, "request-shard") {
			t.Fatalf("an established shard left its authority on the service root: %s", name)
		}
	}
	reopened := openCapacityJournal(t, root)
	defer reopened.Close()
	for index, input := range inputs {
		replayed, created, err := reopened.CreateReceipt(context.Background(), input, nil)
		if err != nil || created || !reflect.DeepEqual(replayed, receipts[index]) {
			t.Fatalf("replay %d: created=%v err=%v", index, created, err)
		}
		shard := LookupKey(input.PrincipalID, input.RequestID)[:2]
		data, found := readPathXattr(t, shardPath(root, shard), requestShardAuthorityXattr)
		authority, _, established, err := decodeRootGenerationAuthority(data, journalShardAuthorityKind, reopened.rootDev, reopened.rootIno)
		if !found || err != nil || !established || authority.IssuanceCount != 1 {
			t.Fatalf("shard %s authority found=%v established=%v count=%d err=%v", shard, found, established, authority.IssuanceCount, err)
		}
	}
}

// legacyShardLayout rewrites established shards into the layout written before this change: the authority on the
// service root and no registry.
func legacyShardLayout(t *testing.T, root string, shards []string) {
	t.Helper()
	for _, shard := range shards {
		data, found := readPathXattr(t, shardPath(root, shard), requestShardAuthorityXattr)
		if !found {
			t.Fatalf("shard %s has no authority", shard)
		}
		writePathXattr(t, root, journalShardAuthorityXattr(shard), data)
		removePathXattr(t, shardPath(root, shard), requestShardAuthorityXattr)
	}
	removePathXattr(t, filepath.Join(root, "actions", requestIndexDirectory), requestShardRegistryXattr)
}

func shardsOf(inputs []ReceiptInput) []string {
	shards := make([]string, len(inputs))
	for index, input := range inputs {
		shards[index] = LookupKey(input.PrincipalID, input.RequestID)[:2]
	}
	return shards
}

func TestJournalMovesRootShardAuthoritiesOntoTheirShards(t *testing.T) {
	root := capacityRoot(t)
	journal := openCapacityJournal(t, root)
	inputs := distinctShardInputs(2)
	receipts := []ActionReceiptV1{createCapacityReceipt(t, journal, inputs[0]), createCapacityReceipt(t, journal, inputs[1])}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	shards := shardsOf(inputs)
	legacyShardLayout(t, root, shards)

	reopened := openCapacityJournal(t, root)
	defer reopened.Close()
	for _, name := range rootXattrNames(t, root) {
		if strings.Contains(name, "request-shard") {
			t.Fatalf("migration left %s on the service root", name)
		}
	}
	for index, input := range inputs {
		if _, found := readPathXattr(t, shardPath(root, shards[index]), requestShardAuthorityXattr); !found {
			t.Fatalf("shard %s authority was not moved", shards[index])
		}
		replayed, created, err := reopened.CreateReceipt(context.Background(), input, nil)
		if err != nil || created || !reflect.DeepEqual(replayed, receipts[index]) {
			t.Fatalf("replay after migration %d: created=%v err=%v", index, created, err)
		}
	}
	registry, found := readPathXattr(t, filepath.Join(root, "actions", requestIndexDirectory), requestShardRegistryXattr)
	if !found || !strings.Contains(string(registry), `"kind":"ActionJournalRequestShardRegistryV1"`) {
		t.Fatalf("registry found=%v data=%s", found, registry)
	}
	// The moved shard keeps issuing from its own record.
	next := journalReceiptInputForRun("capacity-run-next")
	for candidate := 0; ; candidate++ {
		next.RequestID = fmt.Sprintf("capacity-next-%d", candidate)
		next.RequestSHA256 = digestText(next.RequestID)
		if LookupKey(next.PrincipalID, next.RequestID)[:2] == shards[0] {
			break
		}
	}
	createCapacityReceipt(t, reopened, next)
	data, _ := readPathXattr(t, shardPath(root, shards[0]), requestShardAuthorityXattr)
	authority, _, _, err := decodeRootGenerationAuthority(data, journalShardAuthorityKind, reopened.rootDev, reopened.rootIno)
	if err != nil || authority.IssuanceCount != 2 {
		t.Fatalf("moved shard authority count=%d err=%v", authority.IssuanceCount, err)
	}
}

func TestJournalFinishesAMoveInterruptedAfterTheShardRecordWasWritten(t *testing.T) {
	root := capacityRoot(t)
	journal := openCapacityJournal(t, root)
	inputs := distinctShardInputs(1)
	receipt := createCapacityReceipt(t, journal, inputs[0])
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	shard := shardsOf(inputs)[0]
	data, _ := readPathXattr(t, shardPath(root, shard), requestShardAuthorityXattr)
	// Crash after the shard record was written, before the registry and the root record were updated.
	writePathXattr(t, root, journalShardAuthorityXattr(shard), data)
	removePathXattr(t, filepath.Join(root, "actions", requestIndexDirectory), requestShardRegistryXattr)

	reopened := openCapacityJournal(t, root)
	defer reopened.Close()
	if _, found := readPathXattr(t, root, journalShardAuthorityXattr(shard)); found {
		t.Fatal("the root record was not removed")
	}
	replayed, created, err := reopened.CreateReceipt(context.Background(), inputs[0], nil)
	if err != nil || created || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("replay: created=%v err=%v", created, err)
	}
}

func TestJournalCompletesAnInterruptedShardCreationAtOpen(t *testing.T) {
	root := capacityRoot(t)
	journal := openCapacityJournal(t, root)
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	// The state a failed cancel left on the live service: an initializing root record and an empty shard directory.
	inputs := distinctShardInputs(1)
	shard := shardsOf(inputs)[0]
	rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(rootFD, &stat); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createInitializingRootGeneration(rootFD, journalShardAuthorityXattr(shard), journalShardAuthorityKind, uint64(stat.Dev), stat.Ino); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Close(rootFD)
	if err := os.Mkdir(shardPath(root, shard), 0o700); err != nil {
		t.Fatal(err)
	}

	reopened := openCapacityJournal(t, root)
	defer reopened.Close()
	if _, found := readPathXattr(t, root, journalShardAuthorityXattr(shard)); found {
		t.Fatal("the interrupted creation left its root record")
	}
	createCapacityReceipt(t, reopened, inputs[0])
}

func TestJournalRejectsShardsThatDisagreeWithTheRegistry(t *testing.T) {
	cases := map[string]func(t *testing.T, root, shard string){
		"registered shard without its record": func(t *testing.T, root, shard string) {
			removePathXattr(t, shardPath(root, shard), requestShardAuthorityXattr)
		},
		"shard present but not registered": func(t *testing.T, root, shard string) {
			removePathXattr(t, filepath.Join(root, "actions", requestIndexDirectory), requestShardRegistryXattr)
		},
		"root and shard records disagree": func(t *testing.T, root, shard string) {
			data, _ := readPathXattr(t, shardPath(root, shard), requestShardAuthorityXattr)
			changed := strings.Replace(string(data), `"issuance_count":1`, `"issuance_count":2`, 1)
			writePathXattr(t, root, journalShardAuthorityXattr(shard), []byte(changed))
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			root := capacityRoot(t)
			journal := openCapacityJournal(t, root)
			inputs := distinctShardInputs(1)
			createCapacityReceipt(t, journal, inputs[0])
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			tamper(t, root, shardsOf(inputs)[0])
			if reopened, err := OpenWithClock(root, func() time.Time { return journalTestTime }); !errors.Is(err, ErrIntegrity) {
				if reopened != nil {
					_ = reopened.Close()
				}
				t.Fatalf("open after tamper err=%v", err)
			}
		})
	}
}

// shardAuthorityFD returns the descriptor a live journal pinned for an established shard, so a test reads the
// authority of the generation that journal trusts even after the path was replaced underneath it.
func shardAuthorityFD(t *testing.T, journal *Journal, shard string) int {
	t.Helper()
	index, err := requestShardIndex(shard)
	if err != nil {
		t.Fatal(err)
	}
	journal.shardMu.Lock()
	defer journal.shardMu.Unlock()
	pin := journal.shards[index]
	if pin == nil {
		t.Fatalf("journal has no pinned shard %s", shard)
	}
	return pin.fd
}

// readShardAuthorityAt reads the authority a shard directory carries, by path.
func readShardAuthorityAt(root, shard string) ([]byte, bool, error) {
	fd, err := syscall.Open(shardPath(root, shard), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false, err
	}
	defer syscall.Close(fd)
	return fgetRootXattr(fd, requestShardAuthorityXattr)
}
