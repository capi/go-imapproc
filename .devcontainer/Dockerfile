FROM mcr.microsoft.com/devcontainers/go:2-1.25-trixie

# Install Python testing dependencies and Node.js
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3-pytest \
    nodejs \
    npm \
    && rm -rf /var/lib/apt/lists/*

# Note: gws is pinned to 0.9.0 due to a bug in later versions
# (see https://github.com/googleworkspace/cli/issues/344).
# New versions will be evaluated and updated as fixes are released.
RUN npm install -g @googleworkspace/cli@0.9.0

ENV HOME=/home/vscode
USER vscode
RUN /bin/bash -lc "curl -fsSL https://opencode.ai/install | bash"
