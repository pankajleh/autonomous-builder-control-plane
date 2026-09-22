# G0-B Development Admission

### Task 1: Implement the frozen development-admission seam

- [ ] Add the strict schema-v1 development request DTO/validation and separate `POST /v1/development-runs` route using only the frozen fields and existing authentication/error-envelope conventions.
- [ ] Reuse existing admission durability, semantic replay/conflict, controller-owned profile binding, repository-base fencing, launch/run registration and Ralphex/acceptance machinery without changing product `POST /v1/runs` request fields or semantics.
- [ ] Materialize development authority with exactly one executable `### Task 1: Implement the frozen development slice` heading, collision-safe inert capsule markdown, and development-only labels; reject caller injection of private execution policy.
- [ ] Add focused development-admission and product-admission compatibility regression coverage, including strict decoding, capsule digest, replay/conflict, base/profile mismatch, injection rejection, binding corruption and ambiguous launch behavior where represented by existing machinery.
- [ ] Run focused tests, full `go test -count=1 ./...`, full `go test -count=1 -race ./...`, `go vet ./...`, `git diff --check`, exact path inventory and leave a committed clean candidate.
- [ ] Do not merge, publish, activate the new development executor, change Repo A/Ralphex/provider policy, or widen the frozen path ceiling.

When complete, emit `<<<RALPHEX:ALL_TASKS_DONE>>>`.
