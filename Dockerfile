# syntax=docker/dockerfile:1

# Stage 1: Build
FROM golang:1.25-trixie AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN make check && mkdir -p /out && cp bin/imapproc /out/imapproc

# Stage 2: Minimal runtime image
FROM debian:trixie-slim

LABEL org.opencontainers.image.title="imapproc" \
      org.opencontainers.image.description="Lightweight Go daemon that monitors IMAP mailbox and processes unread emails with external scripts" \
      org.opencontainers.image.url="https://github.com/capi/go-imapproc" \
      org.opencontainers.image.source="https://github.com/capi/go-imapproc"

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1000 imapproc \
    && useradd -u 1000 -g 1000 -d /home/imapproc -s /bin/sh -m imapproc

COPY --from=builder /out/imapproc /usr/local/bin/imapproc

USER imapproc
ENTRYPOINT ["/usr/local/bin/imapproc"]
