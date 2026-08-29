# apm2go build targets.
#
# There are three deliverables and one target each: a .deb, an .rpm, and a
# container image. Nothing builds the other two — `make rpm` produces an RPM and
# leaves dist/ otherwise untouched.
#
# Every target runs inside Docker, so Docker is the only thing this host needs.
# The web UI, both agent jars, the eBPF binary and the attach helper are all
# built or fetched inside build/Dockerfile.build; no Node.js and no JDK here.

SHELL := /bin/bash
.DEFAULT_GOAL := help

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# The packages are x86-64 only, and deliberately not a knob. apm2go is installed
# on servers, and those are amd64; an arm64 RPM would be a second artefact to
# build, publish and answer questions about for no one who exists.
PKG_ARCH := amd64

# The container image is the one deliverable that is built for either
# architecture, since it is also what people try apm2go out with — often on an
# arm64 laptop. Defaults to this machine's, so `make image` is runnable straight
# away; `make image ARCH=amd64` builds the one a server wants.
ARCH ?= $(shell uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')

# nfpm rejects a leading "v" and anything that is not semver-ish, so a git
# describe such as "v0.2.0-3-gabc1234" is normalised here rather than failing
# minutes into the build.
PKG_VERSION := $(shell echo "$(VERSION)" | sed 's/^v//; s/-dirty$$//; s/-g[0-9a-f]*$$//; s/-/./g')

DIST := dist

# Builds the Linux binary for $(1) and leaves it at dist/apm2go_$(1). Every
# deliverable is made of one of these.
#
# Not expressed as a rule for that file, so that editing a source file is not
# mistaken for having nothing to do. BuildKit's own cache is what makes a no-op
# rebuild cheap here, and it, unlike a timestamp, actually knows what went into
# the binary.
define build_binary
	@mkdir -p $(DIST)
	docker build --platform linux/$(1) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-f build/Dockerfile.build --target export \
		--output type=local,dest=$(DIST)/export_$(1) .
	mv $(DIST)/export_$(1)/apm2go $(DIST)/apm2go_$(1)
	@rmdir $(DIST)/export_$(1)
endef

# Produces the package in format $(1) without touching the other. nfpm is used
# rather than rpmbuild or dpkg-deb so packages can be built on any host,
# including macOS, with no distribution tooling installed.
define package
	@sed -e 's|$${ARCH}|$(PKG_ARCH)|g' -e 's|$${VERSION}|$(PKG_VERSION)|g' \
		packaging/nfpm.yaml > $(DIST)/nfpm.yaml
	docker run --rm -v "$(CURDIR):/work" -w /work goreleaser/nfpm:latest package \
		--config $(DIST)/nfpm.yaml --packager $(1) --target /work/$(DIST)/
	@rm -f $(DIST)/nfpm.yaml
endef

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "  Packages are always linux/$(PKG_ARCH)."
	@echo "  The image is linux/$(ARCH); override with ARCH=amd64 or ARCH=arm64."

# ---------------------------------------------------------------- deliverables

.PHONY: deb
deb: ## Build the x86-64 .deb package for Debian and Ubuntu
	$(call build_binary,$(PKG_ARCH))
	$(call package,deb)
	@ls -1sh $(DIST)/*.deb

.PHONY: rpm
rpm: ## Build the x86-64 .rpm package for RHEL, Rocky, Alma and Fedora
	$(call build_binary,$(PKG_ARCH))
	$(call package,rpm)
	@ls -1sh $(DIST)/*.rpm

.PHONY: image
image: ## Build the apm2go container image for ARCH (amd64 or arm64)
	$(call build_binary,$(ARCH))
	docker build --platform linux/$(ARCH) -f build/Dockerfile.apm2go \
		--build-arg TARGETARCH=$(ARCH) \
		-t apm2go:$(PKG_VERSION) -t apm2go:latest .

# ---------------------------------------------------------------- development

# Two steps, because CAP_SYS_PTRACE is not in Docker's default set and cannot be
# granted during a build: internal/attach's privilege-drop test verifies nothing
# without it. Tests run on this machine's architecture, to stay out of emulation.
.PHONY: test
test: ## Run gofmt, go vet and the unit tests
	docker build --platform linux/$(ARCH) \
		-f build/Dockerfile.build --target testenv -t apm2go-test:$(ARCH) .
	docker run --rm --cap-add SYS_PTRACE apm2go-test:$(ARCH)

.PHONY: clean
clean: ## Remove build output
	rm -rf $(DIST)
