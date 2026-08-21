# apm2go build targets.
#
# The binary embeds the web UI and both agent jars, so `make build` depends on
# the web and jar targets. Linux binaries are built inside a container because
# the DuckDB driver needs cgo and the host is usually not Linux.

SHELL := /bin/bash
.DEFAULT_GOAL := help

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

MODULE  := github.com/apm2go/apm2go
LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.BuildDate=$(BUILD_DATE)

OTEL_AGENT_VERSION := 2.30.0
OTEL_AGENT_URL := https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/download/v$(OTEL_AGENT_VERSION)/opentelemetry-javaagent.jar

ASSETS_DIR     := internal/assets/files
BOOTSTRAP_JAR  := $(ASSETS_DIR)/apm2go-bootstrap.jar
OTEL_JAR       := $(ASSETS_DIR)/opentelemetry-javaagent.jar
UI_DIST        := internal/api/dist

# apm2go-attach-helper is Linux- and architecture-specific, like OBI below, for
# the same reason: it is a real Linux binary embedded via a //go:build linux
# file, so a macOS `make build` never looks at it. Unlike OBI it is built from
# source already in this repo, not downloaded, and unlike every other embedded
# artifact it must be compiled with CGO_ENABLED=0 specifically — see its own
# package doc for why that is not an optimisation but the entire point of it
# being a separate binary at all.
ATTACHHELPER_DIR := internal/attachhelper/files
ATTACHHELPER_BIN := $(ATTACHHELPER_DIR)/apm2go-attach-helper

# OBI is Linux- and architecture-specific, unlike the Java agent jars, so this
# target downloads for the host's own GOARCH. build/package.sh downloads it
# again per target architecture immediately before that architecture's
# container build, overwriting this file each time.
OBI_VERSION := 0.11.0
EBPF_DIR    := internal/ebpf/files
EBPF_BIN    := $(EBPF_DIR)/obi
OBI_URL      = https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation/releases/download/v$(OBI_VERSION)/obi-v$(OBI_VERSION)-linux-$(shell go env GOARCH).tar.gz

BIN_DIR := dist

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- assets

$(OTEL_JAR): ## Download the bundled OpenTelemetry Java agent
	@mkdir -p $(ASSETS_DIR)
	curl -sSLf -o $@ $(OTEL_AGENT_URL)

.PHONY: bootstrap-jar
bootstrap-jar: $(BOOTSTRAP_JAR) ## Compile the apm2go bootstrap agent jar

$(BOOTSTRAP_JAR): agent/bootstrap/src/io/apm2go/bootstrap/BootstrapAgent.java agent/bootstrap/manifest.txt
	@mkdir -p agent/bootstrap/build/classes $(ASSETS_DIR)
	javac --release 8 -Xlint:-options -d agent/bootstrap/build/classes $<
	jar cfm $@ agent/bootstrap/manifest.txt -C agent/bootstrap/build/classes .

$(EBPF_BIN):
	@mkdir -p $(EBPF_DIR)
	curl -sSLf -o /tmp/obi.tar.gz "$(OBI_URL)"
	tar -xzf /tmp/obi.tar.gz -O obi > $@
	chmod +x $@
	rm -f /tmp/obi.tar.gz

.PHONY: ebpf
ebpf: $(EBPF_BIN) ## Download the bundled OBI (eBPF instrumentation) binary for the host architecture

$(ATTACHHELPER_BIN): internal/attach/*.go cmd/apm2go-attach-helper/main.go
	@mkdir -p $(ATTACHHELPER_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=$(shell go env GOARCH) \
		go build -trimpath -ldflags '-s -w' -o $@ ./cmd/apm2go-attach-helper

.PHONY: attachhelper
attachhelper: $(ATTACHHELPER_BIN) ## Build the cgo-free attach helper for the host architecture

.PHONY: assets
assets: $(BOOTSTRAP_JAR) $(OTEL_JAR) ## Prepare the Java embedded assets
# OBI and the attach helper are not included here: both are Linux-only and
# irrelevant to a macOS `make build`, where the //go:build linux embeds never
# compile either in. `build-linux` builds both per architecture inside
# build/package.sh; `make ebpf` and `make attachhelper` are the standalone way
# to get them for a Linux host.

# ---------------------------------------------------------------- web ui

.PHONY: web
web: ## Build the web UI into the directory the binary embeds
	cd web && npm ci && npm run build

$(UI_DIST):
	@$(MAKE) web

# ---------------------------------------------------------------- build

.PHONY: build
build: assets web ## Build apm2go for the host platform
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/apm2go ./cmd/apm2go

.PHONY: build-linux
build-linux: assets web ## Build linux amd64 and arm64 binaries in a container
	ARCHES="amd64 arm64" VERSION=$(VERSION) COMMIT=$(COMMIT) BUILD_DATE=$(BUILD_DATE) ./build/package.sh

# ---------------------------------------------------------------- test

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: lint
lint: ## Vet and format-check the tree
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...

.PHONY: e2e
e2e: ## Verify discovery, attach and ingest against a real JVM
	./build/e2e.sh

.PHONY: e2e-container
e2e-container: image ## Verify the same across a container boundary
	./build/e2e-container.sh

.PHONY: e2e-multilang
e2e-multilang: ## Verify eBPF instrumentation of Node.js, Python and Go against the Java chain
	./build/e2e-multilang.sh

.PHONY: e2e-multicontainer
e2e-multicontainer: image ## Verify discovery with apm2go and every service in separate containers
	./build/e2e-multicontainer.sh

.PHONY: e2e-all
e2e-all: e2e e2e-container e2e-multilang e2e-multicontainer ## Run every acceptance test

# ---------------------------------------------------------------- package

.PHONY: package
package: build-linux ## Build linux binaries plus RPM and DEB packages
	@echo "Packages are in $(BIN_DIR)/"

.PHONY: image
image: ## Build the apm2go container image for the host architecture
	docker build -f build/Dockerfile.apm2go \
		--build-arg TARGETARCH=$(shell go env GOARCH) \
		-t apm2go:$(VERSION) -t apm2go:latest .

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN_DIR) agent/bootstrap/build $(UI_DIST)
