FROM mcr.microsoft.com/devcontainers/go:2-1.25-trixie

# Install Python testing dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3-pytest \
    && rm -rf /var/lib/apt/lists/*

ENV HOME=/home/vscode
USER vscode
RUN /bin/bash -lc "curl -fsSL https://opencode.ai/install | bash"
