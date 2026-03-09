FROM mcr.microsoft.com/devcontainers/go:2-1.25-trixie

# Install Python testing dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3-pytest \
    && rm -rf /var/lib/apt/lists/*

ENV HOME=/home/vscode
USER vscode
# Note: gws is pinned to 0.9.0 due to a bug in later versions
# (see https://github.com/googleworkspace/cli/issues/344).
# New versions will be evaluated and updated as fixes are released.
RUN /bin/bash -lc "npm install -g @googleworkspace/cli@0.9.0" && \
    /bin/bash -lc "curl -fsSL https://opencode.ai/install | bash"
