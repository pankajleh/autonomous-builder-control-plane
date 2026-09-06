package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

const version = "0.1.0-dev"

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "version":
		if len(args) != 1 {
			usage(stderr)
			return 2
		}
		fmt.Fprintln(stdout, version)
		return 0
	case "validate-transition":
		if len(args) != 3 {
			fmt.Fprintln(stderr, "usage: abcp validate-transition <from> <to>")
			return 2
		}
		if err := domain.ValidateTransition(domain.State(args[1]), domain.State(args[2])); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "VALID")
		return 0
	case "run":
		return runCommand(args[1:], stdout, stderr)
	default:
		usage(stderr)
		return 2
	}
}

func runCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "path to the governed authority manifest")
	ledgerPath := flags.String("ledger", "", "path to the append-only JSONL ledger")
	evidenceRoot := flags.String("evidence-root", "", "root directory for immutable run evidence")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *manifestPath == "" || *ledgerPath == "" || *evidenceRoot == "" {
		fmt.Fprintln(stderr, "usage: abcp run --manifest <path> --ledger <path> --evidence-root <path>")
		return 2
	}

	manifest, err := loadManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	governed, err := authority.New(manifest)
	if err != nil {
		fmt.Fprintf(stderr, "validate authority: %v\n", err)
		return 1
	}
	artifacts, err := evidence.NewStore(*evidenceRoot, governed.RunID())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	canonicalLedger, err := canonicalLedgerDestination(*ledgerPath, artifacts.RunDir())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	events, err := ledger.NewJSONLLedger(canonicalLedger)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runner, err := runctl.New(governed, events, artifacts, supervisor.New())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := runner.Run(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		if result.State != "" {
			fmt.Fprintln(stderr, result.State)
		}
		return 1
	}
	if !result.Accepted() {
		fmt.Fprintln(stderr, result.State)
		if result.FailureReason != "" {
			fmt.Fprintln(stderr, result.FailureReason)
		}
		return 1
	}
	fmt.Fprintln(stdout, result.State)
	return 0
}

func canonicalLedgerDestination(path, evidenceRunDir string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve ledger path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return "", fmt.Errorf("create ledger directory: %w", err)
	}
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", fmt.Errorf("canonicalize ledger directory: %w", err)
	}
	canonical := filepath.Join(canonicalParent, filepath.Base(absolute))
	if info, statErr := os.Lstat(absolute); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		canonical, err = filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", fmt.Errorf("canonicalize ledger symlink: %w", err)
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect ledger path: %w", statErr)
	}
	evidenceRunDir, err = filepath.EvalSymlinks(evidenceRunDir)
	if err != nil {
		return "", fmt.Errorf("canonicalize evidence run directory: %w", err)
	}
	relative, err := filepath.Rel(evidenceRunDir, canonical)
	if err != nil {
		return "", fmt.Errorf("compare ledger and evidence paths: %w", err)
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("ledger path must be outside the run evidence directory")
	}
	return canonical, nil
}

func loadManifest(path string) (authority.Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return authority.Manifest{}, fmt.Errorf("open manifest: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest authority.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return authority.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return authority.Manifest{}, fmt.Errorf("decode manifest: multiple JSON values")
		}
		return authority.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return manifest, nil
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: abcp <version|validate-transition|run>")
}
