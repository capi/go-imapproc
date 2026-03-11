BIN_DIR   := bin
CMDS      := $(notdir $(wildcard cmd/*))
BINS      := $(addprefix $(BIN_DIR)/,$(CMDS))

REGISTRY   := ghcr.io/capi
IMAGE_NAME := $(REGISTRY)/go-imapproc

.PHONY: all build check test vet lint fmt clean tidy docker-slim docker-full docker-all docker-publish

all: build

## check: build, vet, and test — must pass before committing
check: build vet test

build: $(BINS)

$(BIN_DIR)/%: cmd/%
	@mkdir -p $(BIN_DIR)
	go build -o $@ ./$<

## test: run all tests
test:
	go test ./...

## vet: run go vet
vet:
	go vet ./...

## fmt: format source code
fmt:
	gofmt -l -w .

## tidy: tidy and verify go modules
tidy:
	go mod tidy
	go mod verify

## clean: remove build artifacts
clean:
	rm -rf $(BIN_DIR)

## docker-slim: build minimal Docker image (go-imapproc:latest-slim)
docker-slim:
	docker build -t $(IMAGE_NAME):latest-slim -f Dockerfile .

## docker-full: build full Docker image with Python3/Node/gws (go-imapproc:latest-full)
docker-full:
	docker build -t $(IMAGE_NAME):latest-full -f Dockerfile.full .

## docker-all: build both slim and full Docker images
docker-all: docker-slim docker-full

## docker-publish: push both Docker images to registry
docker-publish:
	docker push $(IMAGE_NAME):latest-slim
	docker push $(IMAGE_NAME):latest-full
