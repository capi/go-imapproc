- You run in a devcontainer. Ask the user to install additional tools you need.
- Avoid adding new dependencies without permission.

- Use Makefile targets to build/verify/etc. Tidy code and use formatter.
- When building directly using "go" command, make sure that binaries are built in the bin/ directory.

- Prefix git commits with your agent's name, e.g. "OpenCode: The change". Use short commit messages. Explain details in body.
- Always run "make check", run all linters, formatters, checks, and tidy before comitting.
