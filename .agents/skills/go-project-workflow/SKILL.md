---
name: go-project-workflow
description: Build, test, and quality checks for Go projects with Makefile
license: MIT
compatibility: opencode
metadata:
  language: go
  tool: make
---

## Key principles

### Always use Makefile targets
Don't use `go build` directly. Use `make build` or `make check`.

If you absolutely must use `go build`, use this pattern:
```
go build -o bin/BINARY_NAME ./cmd/BINARY_NAME
```

### Pre-commit workflow
Before committing any changes:
1. Run `make check` (builds, vets, and tests in one command)
2. Run `make fmt` and `make tidy` to clean up code
3. Verify all checks passed before committing

### Multiple binaries
This project may have multiple binaries in separate `cmd/*/` directories. `make build` builds them all. Check the `Makefile` for available targets.

## Available targets

- `make build` — Build all binaries in `bin/`
- `make check` — Build, vet, and test (run before committing)
- `make test` — Run `go test ./...`
- `make vet` — Run `go vet ./...`
- `make fmt` — Format source code with `gofmt`
- `make tidy` — Tidy and verify Go modules
- `make clean` — Remove build artifacts

## Troubleshooting

**Build fails with "command not found"?**
Check that the binary exists in `cmd/`. The Makefile builds one binary per command directory.

**Tests fail after code changes?**
Run `make fmt` and `make tidy` first, then `make check` again.

**Want to debug a specific binary?**
Build it individually: `go build -o bin/BINARY_NAME ./cmd/BINARY_NAME` and then debug with your preferred tool.
