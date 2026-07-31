.PHONY: all build test check fmt vet clean install

GO      ?= go
BIN     := bin/gyro
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

all: build test

build:
	$(GO) build $(LDFLAGS) -o $(BIN) .

test:
	$(GO) test ./...

# Parse every reference program. Fails if any of them stops parsing cleanly.
check: build
	@for file in examples/*.gyro; do \
		./$(BIN) check $$file || exit 1; \
	done

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

install:
	$(GO) install $(LDFLAGS) .

clean:
	rm -rf bin
	$(GO) clean
