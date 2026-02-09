SHELL := /bin/sh

BIN_DIR := bin
BINARY := $(BIN_DIR)/runeshell

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X 'main.version=$(VERSION)' -X 'main.commit=$(COMMIT)' -X 'main.date=$(DATE)'

.PHONY: build install clean test go-test web-test unit-test integration-test e2e-test coverage ci-test fmt

build:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/runeshell

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/runeshell

go-test:
	GOCACHE=$${GOCACHE:-/tmp/gocache} go test ./...

web-test:
	node --test web/app.test.js

unit-test:
	GOCACHE=$${GOCACHE:-/tmp/gocache} go test ./cmd/... ./internal/...

integration-test:
	GOCACHE=$${GOCACHE:-/tmp/gocache} go test ./integration/...

e2e-test:
	npm run test:e2e

coverage:
	./scripts/check-coverage.sh

ci-test: unit-test integration-test web-test coverage

test: go-test web-test

fmt:
	gofmt -w cmd internal

clean:
	rm -rf $(BIN_DIR)
