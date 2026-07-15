BINARY := bin/agentsession
PACKAGE := ./cmd/agentsession
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: generate fmt build test vet check run web clean

generate:
	go tool templ generate

fmt:
	go tool templ fmt .
	go tool templ generate
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

build: generate
	mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(PACKAGE)

test: generate
	go test ./...

vet: generate
	go vet ./...

check: generate
	git diff --exit-code -- '*.templ' '*_templ.go'
	go vet ./...
	go test ./...

run: generate
	go run $(PACKAGE)

web: generate
	go run $(PACKAGE) web

clean:
	rm -rf bin
