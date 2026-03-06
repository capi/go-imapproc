- You run in a devcontainer. Ask the user to install additional tools you need.
- Avoid adding new dependencies without permission.

- Use Makefile targets to build/verify/etc. Tidy code and use formatter.
- Always use `make build` (or `make check`) instead of `go build` directly. If you must use `go build`, use the pattern: `go build -o bin/BINARY_NAME ./cmd/BINARY_NAME`

- Prefix git commits with your agent's name, e.g. "OpenCode: The change". Use short commit messages. Explain details in body.
- Always run "make check", run all linters, formatters, checks, and tidy before comitting.
