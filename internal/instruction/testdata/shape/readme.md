# Orchard

Orchard is a small inventory service with an HTTP API and a nightly reconciliation job.

## Installation

    go install example.org/orchard/cmd/orchard@latest

## Usage

Start the server with a configuration file:

    orchard --config orchard.yaml

The server listens on port 8080. The reconciliation job runs at 02:00 local time and writes a summary line to the log.

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `listen` | `:8080` | address the API binds to |
| `feed_url` | none | where the warehouse feed is fetched from |

## Development

This project uses Go modules. The Makefile has build, test, lint and format targets. Source lives under `cmd/` and `internal/`; tests sit beside the code in `_test.go` files. The code style follows gofmt, and the lint configuration is in the repo root.

## License

Apache 2.0.
