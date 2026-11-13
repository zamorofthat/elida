# Stock Reconciliation Design

## Intent

The nightly job in this project double counts a record when the warehouse feed repeats it. The design below removes the double count without changing the feed format.

## Goals

- A repeated record changes the count once.
- The job stays idempotent across restarts.
- The code style of `internal/stock` is kept: one function per rule, table tests.

## Non-goals

- Changing the feed format.
- Reworking the file structure of `cmd/orchard`.

## Constraints

The parser in `internal/feed/parse.go` is shared with the import tool, so its signature stays as it is. Lint and format checks in the Makefile already cover the new package. This repo keeps migrations numbered; the design adds none.

## Requirements

The reconciliation reads each record's identifier, keeps the set it has seen during the run, and applies a record only when its identifier is new. The set is rebuilt from the store at start-up, which is what keeps a restarted run from applying a record twice. The project already stores identifiers, so no schema change is needed.

## Open questions

Whether the seen-set is bounded by a time window is left to the implementation. The feed has never exceeded a few thousand records a night.
