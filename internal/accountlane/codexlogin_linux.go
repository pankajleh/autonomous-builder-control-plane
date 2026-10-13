//go:build linux

package accountlane

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// DeviceLoginTimeout bounds a Codex device sign-in; its code lasts 15 minutes.
const DeviceLoginTimeout = 15 * time.Minute

// deviceLoginStartTimeout bounds the wait for the link and the code.
const deviceLoginStartTimeout = 30 * time.Second

var (
	ansiPattern       = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	deviceURLPattern  = regexp.MustCompile(`https://[A-Za-z0-9.-]+\.(openai|chatgpt)\.com/[A-Za-z0-9/._-]*`)
	deviceCodePattern = regexp.MustCompile(`^[A-Z0-9]{4,6}-[A-Z0-9]{4,6}$`)
)

// deviceEnvironment is all a device sign-in sees of the controller's
// environment: no key, no other sign-in.
var deviceEnvironment = []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"}

// CodexDeviceLogin starts `codex login --device-auth` (found on the
// controller's PATH) in its own process group, with CODEX_HOME set to home.
// It answers once Codex has printed its link and its one-time code; the
// process then waits for the administrator for at most DeviceLoginTimeout.
func CodexDeviceLogin(ctx context.Context, home string) (DeviceLogin, error) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		return DeviceLogin{}, errors.New("codex is not on the controller's PATH")
	}
	runCtx, cancel := context.WithTimeout(ctx, DeviceLoginTimeout)
	command := exec.CommandContext(runCtx, codex, "login", "--device-auth")
	command.Env = []string{"CODEX_HOME=" + home}
	for _, key := range deviceEnvironment {
		if value, ok := os.LookupEnv(key); ok {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.Stdin = nil
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return syscall.Kill(-command.Process.Pid, syscall.SIGTERM) }
	command.WaitDelay = 5 * time.Second
	reader, writer, err := os.Pipe()
	if err != nil {
		cancel()
		return DeviceLogin{}, err
	}
	command.Stdout, command.Stderr = writer, writer
	if err := command.Start(); err != nil {
		cancel()
		reader.Close()
		writer.Close()
		return DeviceLogin{}, errors.New("codex device sign-in did not start")
	}
	writer.Close()
	done := make(chan struct{})
	go func() {
		command.Wait()
		cancel()
		close(done)
	}()
	found := make(chan [2]string, 1)
	go func() {
		url, code := readDevice(reader)
		found <- [2]string{url, code}
		io.Copy(io.Discard, reader)
		reader.Close()
	}()
	stop := func() { cancel() }
	select {
	case pair := <-found:
		if pair[0] == "" || pair[1] == "" {
			stop()
			return DeviceLogin{}, errors.New("codex printed no device link and code")
		}
		return DeviceLogin{VerificationURL: pair[0], UserCode: pair[1], ExpiresAt: time.Now().Add(DeviceLoginTimeout), Done: done, Stop: stop}, nil
	case <-time.After(deviceLoginStartTimeout):
		stop()
		return DeviceLogin{}, errors.New("codex printed no device link and code in time")
	case <-done:
		return DeviceLogin{}, errors.New("codex device sign-in ended early")
	}
}

// readDevice reads Codex's output until it has seen the link and the code,
// answering empty strings when the output ends first.
func readDevice(reader io.Reader) (string, string) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 64<<10))
	url, code := "", ""
	for scanner.Scan() {
		line := strings.TrimSpace(ansiPattern.ReplaceAllString(scanner.Text(), ""))
		if url == "" {
			url = deviceURLPattern.FindString(line)
		}
		if code == "" && deviceCodePattern.MatchString(line) {
			code = line
		}
		if url != "" && code != "" {
			return url, code
		}
	}
	return "", ""
}
