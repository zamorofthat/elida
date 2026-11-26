# Stock Reconciliation Implementation Plan

**Goal:** The nightly job reconciles counts against the warehouse feed without double counting.

**Architecture:** One new package under `internal/feed`, called from the existing job in `cmd/orchard`. This project keeps lint and format checks in the Makefile.

### Task 1: Parse the feed

**Files:**
- Create: `internal/feed/parse.go`
- Test: `internal/feed/parse_test.go`

- [ ] Step 1: Write the failing test for a three-record feed.
- [ ] Step 2: Run it and see it fail.
- [ ] Step 3: Implement the parser in `internal/feed/parse.go`.
- [ ] Step 4: Run the tests; they must pass.
- [ ] Step 5: Commit.

### Task 2: Reconcile counts

**Files:**
- Modify: `internal/stock/count.go`
- Test: `internal/stock/count_test.go`

- [ ] Step 1: Write the failing test for a duplicated record.
- [ ] Step 2: Run it and see it fail.
- [ ] Step 3: Implement the reconciliation in `internal/stock/count.go`.
- [ ] Step 4: Run gofmt and the lint target in this repo.
- [ ] Step 5: Commit.

### Task 3: Wire the job

**Files:**
- Modify: `cmd/orchard/main.go`

- [ ] Step 1: Write the failing test for the job entry point.
- [ ] Step 2: Implement the wiring in `cmd/orchard/main.go`.
- [ ] Step 3: Run the full suite. The project's lint target never reports new findings.
- [ ] Step 4: Commit.
