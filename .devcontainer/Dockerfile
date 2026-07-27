FROM mcr.microsoft.com/devcontainers/base:2-trixie

# Install Go 1.25.12 (pinned for reproducibility)
ENV GO_VERSION=1.25.12 \
    GO_TAR=go1.25.12.linux-amd64.tar.gz \
    GO_SHA256=234828b7a89e0e303d2556310ee549fbcf253d28de937bac3da13d6294262ac1

RUN wget -q "https://go.dev/dl/${GO_TAR}" && \
    echo "${GO_SHA256}  ${GO_TAR}" | sha256sum -c - && \
    tar -C /usr/local -xzf "${GO_TAR}" && \
    rm "${GO_TAR}"

ENV PATH=/usr/local/go/bin:$PATH

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
