# ABCP Ralphex Execution Integration P01 Evidence

## Runtime identity

- Repo B base: `ff2202884cad24ac1b4ced70c8100d0ebc392827`
- Ralphex executable: `/home/devagent/ralphex-behavior-lab/tools/ralphex/.bin/ralphex`
- Ralphex executable SHA-256: `9ad47b083aaaf34eed4b35ca82f3c92d1a3b7641921be0ea606cbd1cd1c3adac`
- Ralphex detached source HEAD: `319e30618352a1b43e4be1b8a894c6c05e6d5fa8`

## Profile change

The private Repo B admission manifest template now pins the approved Ralphex executable, executable digest, and source revision. The public admission profile identifier, Repo C contract, repository settings, executor policy, worktree policy, acceptance policy, and `internal/run` to `internal/ralphex` path are unchanged. The independent local smoke acceptance command remains `/usr/bin/true`; it is not the Ralphex execution command.

## Regression coverage

`TestAdmissionMaterializesV2InputsAndReplaysOrConflicts` uses a distinct executable as the controller-owned Ralphex runtime and verifies that admission materialization preserves its path, digest, source revision, mode, and time bounds exactly instead of substituting the ABCP launcher or acceptance executable.

## Gates

- `sha256sum /home/devagent/ralphex-behavior-lab/tools/ralphex/.bin/ralphex`: PASS, exact approved executable digest.
- `git -C /home/devagent/ralphex-behavior-lab/tools/ralphex rev-parse HEAD`: PASS, exact approved detached source revision.
- Bounded `abcp serve` startup with the real private profile and a fresh service root: PASS; the controller remained healthy until the expected timeout shutdown and emitted no error output.
- `go test -count=1 ./internal/runadmission -run 'TestAdmissionMaterializesV2InputsAndReplaysOrConflicts' -v`: PASS.
- `go test -count=1 ./internal/runadmission ./internal/run ./internal/ralphex`: PASS.
- `go test -count=1 ./...`: PASS.
- `go vet ./...`: PASS.

The focused and full gates confirm that no public DTO/API, Repo C source, admission contract, runner implementation, or Ralphex adapter implementation changed.
