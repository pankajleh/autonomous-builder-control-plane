package activity

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

const providerTimeout = 5 * time.Second

type session struct {
	ID           string          `json:"id"`
	State        string          `json:"state"`
	Dir          string          `json:"dir"`
	DirPath      string          `json:"dirPath,omitempty"`
	DisplayTitle string          `json:"displayTitle,omitempty"`
	Project      string          `json:"project,omitempty"`
	Repository   string          `json:"repository,omitempty"`
	AdmissionID  string          `json:"admissionId,omitempty"`
	PlanPath     string          `json:"planPath,omitempty"`
	Branch       string          `json:"branch,omitempty"`
	Mode         string          `json:"mode,omitempty"`
	RunParams    string          `json:"runParams,omitempty"`
	StartTime    string          `json:"startTime"`
	LastModified string          `json:"lastModified"`
	DiffStats    json.RawMessage `json:"diffStats,omitempty"`
}

type sidecar struct {
	url          string
	port         int
	client       *http.Client
	command      *exec.Cmd
	runtimeDir   string
	runtimeLease *os.File
	done         chan struct{}
	cancel       context.CancelFunc
}

func providerClient() *http.Client {
	return &http.Client{Timeout: providerTimeout, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: providerTimeout, MaxResponseHeaderBytes: 16 << 10}, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrIntegrity }}
}

// Keep attestation work bounded even if a pinned capability probe times out.
// The verifier retains its own duplicate descriptor until it exits, so a
// cancelled caller cannot redirect the probe through a reused file descriptor.
var attestationSlots = make(chan struct{}, MaxStreams)

func attest(ctx context.Context, binary *os.File, scope Scope) error {
	select {
	case attestationSlots <- struct{}{}:
	default:
		return ErrExhausted
	}
	pinned, err := duplicateFile(binary)
	if err != nil {
		<-attestationSlots
		return ErrUnavailable
	}
	done := make(chan error, 1)
	go func() {
		defer func() { pinned.Close(); <-attestationSlots }()
		err := ralphex.VerifyGovernedExecutionCapabilityV1(fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), pinned.Fd()), scope.Ralphex.BinarySHA256, scope.Ralphex.SourceSHA)
		if err == nil {
			hasher := sha256.New()
			_, err = io.Copy(hasher, io.LimitReader(pinned, 1<<30))
			if err == nil && hex.EncodeToString(hasher.Sum(nil)) != scope.Ralphex.BinarySHA256 {
				err = ErrIntegrity
			}
		}
		done <- err
	}()
	select {
	case <-ctx.Done():
		return ErrUnavailable
	case err := <-done:
		if err != nil {
			return ErrUnavailable
		}
		return nil
	}
}

func startSidecar(ctx context.Context, scope Scope, root string) (*sidecar, error) {
	startup, startCancel := context.WithTimeout(ctx, providerTimeout)
	defer startCancel()
	if startup.Err() != nil {
		return nil, ErrUnavailable
	}
	binary, err := openRegular(scope.Ralphex.BinaryPath, os.O_RDONLY, false)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer binary.Close()
	if err = attest(startup, binary, scope); err != nil {
		return nil, err
	}
	if startup.Err() != nil {
		return nil, ErrUnavailable
	}
	// The provider installs configuration before entering watch-only mode.
	// Give it a private home and cwd outside the admitted repository so neither
	// default installation nor local project configuration can affect the run.
	runtimeDir, lease, err := newSidecarRuntime(startup, root)
	if err != nil {
		return nil, ErrUnavailable
	}
	keepRuntime := false
	defer func() {
		if !keepRuntime {
			os.RemoveAll(runtimeDir)
			lease.Close()
		}
	}()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, ErrUnavailable
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	lifetime, cancel := context.WithCancel(ctx)
	command := exec.CommandContext(lifetime, scope.Ralphex.BinaryPath, "--serve", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--watch", scope.Repository)
	command.Env = append(gitexec.Environment(), "HOME="+runtimeDir)
	command.Dir = runtimeDir
	pinExecutable(command, binary)
	// Both controller and child retain the runtime lease. Reconciliation must
	// not remove runtime state while either process is still alive.
	command.ExtraFiles = append(command.ExtraFiles, lease)
	done := make(chan struct{})
	if err = startContained(command, done); err != nil {
		cancel()
		return nil, ErrUnavailable
	}
	sc := &sidecar{url: "http://127.0.0.1:" + strconv.Itoa(port), port: port, client: providerClient(), command: command, runtimeDir: runtimeDir, runtimeLease: lease, done: done, cancel: cancel}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-startup.Done():
			sc.close()
			return nil, ErrUnavailable
		case <-sc.done:
			cancel()
			return nil, ErrUnavailable
		case <-ticker.C:
			if _, err = sc.sessions(startup); err == nil {
				keepRuntime = true
				return sc, nil
			}
		}
	}
}
func (s *sidecar) close() {
	s.cancel()
	<-s.done
	s.client.CloseIdleConnections()
	os.RemoveAll(s.runtimeDir)
	s.runtimeLease.Close()
}

// Each request opens a new connection. The port alone is not identity: another
// process can bind it after the pinned child exits. Check both before and after
// reading responses, and again at the batch's persistence boundary.
func (s *sidecar) verifyOwner() error {
	if s.command == nil || s.command.Process == nil || s.done == nil || s.port < 1 {
		return ErrUnavailable
	}
	select {
	case <-s.done:
		return ErrUnavailable
	default:
	}
	if !ownsListener(s.command.Process.Pid, s.port) {
		return ErrUnavailable
	}
	select {
	case <-s.done:
		return ErrUnavailable
	default:
		return nil
	}
}

func (s *sidecar) sessions(ctx context.Context) ([]session, error) {
	if err := s.verifyOwner(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/api/sessions", nil)
	if err != nil {
		return nil, ErrIntegrity
	}
	response, err := s.client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, ErrIntegrity
	}
	var list []session
	if strictjson.Decode(data, &list) != nil || list == nil || len(list) > 10000 {
		return nil, ErrIntegrity
	}
	if err = s.verifyOwner(); err != nil {
		return nil, err
	}
	return list, nil
}

func correlate(scope Scope, list []session) (session, error) {
	var found session
	count := 0
	for _, candidate := range list {
		if candidate.AdmissionID != scope.RunID {
			if candidate.Branch == scope.Branch {
				return session{}, ErrIntegrity
			}
			continue
		}
		if candidate.Repository != scope.RepositoryIdentity || candidate.Branch != scope.Branch || candidate.Project != "" && candidate.Project != scope.Project || candidate.StartTime == "" || candidate.ID == "" || len(candidate.ID) > MaxSourceID {
			return session{}, ErrIntegrity
		}
		if started, err := time.Parse(time.RFC3339Nano, candidate.StartTime); err != nil || started.IsZero() {
			return session{}, ErrIntegrity
		}
		expected := filepath.Join(scope.Repository, ".ralphex", "progress")
		if candidate.DirPath != expected {
			return session{}, ErrIntegrity
		}
		dir, err := openDirectory(expected)
		if err != nil {
			return session{}, ErrIntegrity
		}
		dir.Close()
		found = candidate
		count++
	}
	if count == 0 {
		return session{}, ErrUnavailable
	}
	if count != 1 {
		return session{}, ErrIntegrity
	}
	return found, nil
}

func sessionID(path string) string {
	name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "progress-"), ".txt")
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(filepath.Clean(path)))
	return fmt.Sprintf("%s-%016x", name, hash.Sum64())
}

// Only file identity/append continuity is inspected here; provider events are
// obtained exclusively through the sidecar's SSE machine contract.
func progressProof(scope Scope, selected session, previous *providerProof) (providerProof, error) {
	dirPath := filepath.Join(scope.Repository, ".ralphex", "progress")
	dir, err := openDirectory(dirPath)
	if err != nil {
		return providerProof{}, ErrIntegrity
	}
	entries, err := dir.ReadDir(10001)
	dir.Close()
	if err != nil && err != io.EOF || len(entries) > 10000 {
		return providerProof{}, ErrIntegrity
	}
	path := ""
	for _, entry := range entries {
		candidate := filepath.Join(dirPath, entry.Name())
		if sessionID(candidate) == selected.ID {
			if path != "" {
				return providerProof{}, ErrIntegrity
			}
			path = candidate
		}
	}
	if path == "" {
		return providerProof{}, ErrIntegrity
	}
	f, err := openRegular(path, os.O_RDONLY, false)
	if err != nil {
		return providerProof{}, ErrIntegrity
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > MaxBytes {
		return providerProof{}, ErrIntegrity
	}
	p := providerProof{Session: sourceID(selected.ID), Generation: identity(selected.ID, selected.StartTime, selected.PlanPath, scope.AuthorityDigest), FileIdentity: generationFileID(f), Size: info.Size()}
	if p.FileIdentity == "" {
		return providerProof{}, ErrIntegrity
	}
	if previous != nil {
		if p.Session != previous.Session || p.Generation != previous.Generation || p.FileIdentity != previous.FileIdentity || p.Size < previous.Size {
			return providerProof{}, ErrIntegrity
		}
		hash := sha256.New()
		if _, err = io.CopyN(hash, f, previous.Size); err != nil || hex.EncodeToString(hash.Sum(nil)) != previous.PrefixDigest {
			return providerProof{}, ErrIntegrity
		}
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return providerProof{}, ErrIntegrity
		}
	}
	hash := sha256.New()
	if _, err = io.CopyN(hash, f, p.Size); err != nil {
		return providerProof{}, ErrIntegrity
	}
	p.PrefixDigest = hex.EncodeToString(hash.Sum(nil))
	return p, nil
}

type providerMessage struct {
	id      uint64
	payload ProviderEvent
}

// Bounded five-second reads allow metadata/generation revalidation between
// batches and avoid trusting a long-lived provider connection after rotation.
func (s *sidecar) batch(ctx context.Context, id string, last uint64) ([]providerMessage, error) {
	if err := s.verifyOwner(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/events?session="+url.QueryEscape(id), nil)
	if err != nil {
		return nil, ErrIntegrity
	}
	if last != math.MaxUint64 {
		req.Header.Set("Last-Event-ID", strconv.FormatUint(last, 10))
	}
	response, err := s.client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return nil, ErrUnavailable
	}
	messages, err := parseSSE(response.Body)
	if err != nil {
		return nil, err
	}
	if err = s.verifyOwner(); err != nil {
		return nil, err
	}
	return messages, nil
}

func parseSSE(reader io.Reader) ([]providerMessage, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 4<<20))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	result := make([]providerMessage, 0)
	id, data := "", ""
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data != "" {
				number, err := strconv.ParseUint(id, 10, 64)
				if err != nil || number == math.MaxUint64 || strconv.FormatUint(number, 10) != id {
					return nil, ErrIntegrity
				}
				var p ProviderEvent
				if strictjson.Decode([]byte(data), &p) != nil {
					return nil, ErrIntegrity
				}
				result = append(result, providerMessage{number, p})
				if len(result) >= MaxPageSize {
					return result, nil
				}
			}
			id, data = "", ""
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			if id != "" || len(value) > MaxSourceID {
				return nil, ErrIntegrity
			}
			id = value
		case "data":
			if data != "" {
				data += "\n"
			}
			data += value
			if len(data) > 64<<10 {
				return nil, ErrIntegrity
			}
		}
	}
	if err := scanner.Err(); err != nil {
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			return nil, ErrUnavailable
		}
	}
	return result, nil
}
