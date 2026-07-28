BINARY := bin/agentsession
PACKAGE := ./cmd/agentsession
# Optional equivalent of the application's --data-dir override for maintenance.
DATA_DIR ?=
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: help generate fmt build test vet check run web clean remove-db

help:
	@printf '%s\n' \
		'Usage: make <target>' \
		'' \
		'Targets:' \
		'  help      Show this help message' \
		'  generate  Generate Go code from templ components' \
		'  fmt       Format templ and Go sources' \
		'  build     Build bin/agentsession' \
		'  test      Run all tests' \
		'  vet       Run Go static analysis' \
		'  check     Verify generated code, vet, and tests' \
		'  run       Run the terminal interface' \
		'  web       Run the web interface on 127.0.0.1:8080' \
		'  remove-db Remove the local AgentSession database' \
		'  clean     Remove build artifacts'

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

remove-db:
	go run $(PACKAGE) $(if $(strip $(DATA_DIR)),--data-dir "$(DATA_DIR)",) database remove

clean:
	rm -rf bin
