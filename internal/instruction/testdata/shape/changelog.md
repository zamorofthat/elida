# Changelog

## 1.4.0

### Features

- reconcile repeated feed records once instead of twice
- add a `--dry-run` flag to the reconciliation job
- report the feed's record count in the summary line

### Bug Fixes

- the parser in `internal/feed/parse.go` no longer drops the last record of a feed without a trailing newline
- lint target in the Makefile now covers `cmd/`
- format check no longer fails on generated files in this repo

## 1.3.2

### Bug Fixes

- the server always closed the store twice on shutdown; it now closes it once
- unused configuration key `feed_timeout` removed from the example file
- the project's Docker image no longer ships test fixtures under `test/`

## 1.3.1

### Bug Fixes

- code style: import order fixed in `internal/stock/count.go`
- the job should not have logged the feed URL; it does not any more
