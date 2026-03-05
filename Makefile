BIN_DIR   := bin
CMDS      := $(notdir $(wildcard cmd/*))
BINS      := $(addprefix $(BIN_DIR)/,$(CMDS))

.PHONY: all build check test vet lint fmt clean tidy

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
