.PHONY: all build test bench check fmt vet clean install grammar

GO      ?= go
BIN     := bin/gyro
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

all: build test

build:
	$(GO) build $(LDFLAGS) -o $(BIN) .

test:
	$(GO) test ./...

# Measure the backend against the C each case was ported from. BENCH
# filters by case name, REPEAT says how many runs each binary gets
# (fastest counts).
BENCH  ?=
REPEAT ?= 5

bench:
	$(GO) test ./tests/bench -run TestBench -count 1 -timeout 30m -v \
		-gyro.bench=true -gyro.bench.repeat=$(REPEAT) -gyro.bench.filter=$(BENCH)

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

# Regenerate and test both tree-sitter grammars. Needs `npm install` in
# each grammar directory once; the generated parser.c is committed.
grammar:
	cd tree-sitter-gyro && npx tree-sitter generate && npx tree-sitter test
	cd tree-sitter-metals && npx tree-sitter generate && npx tree-sitter test

clean:
	rm -rf bin
	$(GO) clean
