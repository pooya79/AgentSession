BINARY := bin/agentsession
PACKAGE := ./cmd/agentsession
DATA_DIR ?= $(if $(XDG_DATA_HOME),$(XDG_DATA_HOME)/agentsession,$(HOME)/.local/share/agentsession)
DATABASE := $(DATA_DIR)/agentsession.db
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
	rm -f -- "$(DATABASE)" "$(DATABASE)-wal" "$(DATABASE)-shm"

clean:
	rm -rf bin
