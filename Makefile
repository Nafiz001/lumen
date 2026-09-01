# lumen -- a private search engine

BINARY := lumen
PKG    := ./cmd/lumen

.PHONY: all build test race bench lint fmt vet cover clean install demo help

all: lint test build

build:
	go build -o $(BINARY) $(PKG)

install:
	go install $(PKG)

test:
	go test ./...

# The concurrency tests only mean something under the race detector; the
# server answers queries from many goroutines against one index.
race:
	go test -race ./...

bench:
	go test -run XXX -bench . -benchmem ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	@echo "detail: go tool cover -html=coverage.out"

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

# gofmt -l prints files needing formatting; failing on non-empty output is what
# makes it a check rather than a suggestion.
lint: vet
	@test -z "$$(gofmt -l cmd internal)" || \
		(echo "gofmt needed:"; gofmt -l cmd internal; exit 1)

# Index this repository and serve it, which is the shortest honest demo: the
# corpus is the code being described.
demo: build
	./$(BINARY) index . -index /tmp/lumen-demo -quiet
	./$(BINARY) serve -index /tmp/lumen-demo

clean:
	rm -f $(BINARY) $(BINARY).exe coverage.out
	rm -rf lumen-index

help:
	@echo "build    build $(BINARY)"
	@echo "test     run the test suite"
	@echo "race     run it under the race detector"
	@echo "bench    run the benchmarks"
	@echo "lint     gofmt check and go vet"
	@echo "cover    test coverage summary"
	@echo "demo     index this repo and serve it"
