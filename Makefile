# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright 2026 Red Hat, Inc.

BINARY_NAME := rhds-mcp
# grw build-binaries v1.4 runs plain `make <platform>` without VERSION, so CI
# binaries report the default until Phase F passes VERSION=<tag> or derives it
# from the release tag.
VERSION ?= 0.0.1
DIST_DIR := dist/bin
GO_PACKAGE := ./cmd/rhds-mcp
GO_LDFLAGS := -s -w -X main.serverVersion=$(VERSION)
GO_BUILD_FLAGS := -trimpath -ldflags="$(GO_LDFLAGS)"

# CI sets WINDOWS_IMAGE via go-release-workflows; override for local dev.
WINDOWS_IMAGE ?= grw-windows-cross

.DEFAULT_GOAL := build

.PHONY: data data-update data-force data-determinism build size-check license-check linux-x64 linux-arm64 darwin-x64 darwin-arm64 win32-x64 win32-arm64 clean

# Generated data is not committed. `make data` reads the committed lock and
# regenerates only when internal/data/dist/index.json is missing, stale, or was
# produced by an older generator version. Network access happens only inside
# tools/gen-data, never at runtime.
data:
	go generate ./internal/data

# Resolve current npm and GitHub inputs and rewrite the lock. Run this when a
# new RHDS release should be bundled, then commit the lock.
data-update:
	go run ./tools/gen-data -update -lock internal/data/sources.lock.json

# Regenerate unconditionally (used by the determinism check).
data-force:
	go run ./tools/gen-data -force -lock internal/data/sources.lock.json -out internal/data/dist

# Two forced runs with the same lock must be byte-identical (NFR-05).
data-determinism: data
	@tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	cp -a internal/data/dist "$$tmp/first"; \
	$(MAKE) -s data-force >/dev/null; \
	diff -r "$$tmp/first" internal/data/dist

build: data
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 go build $(GO_BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME) $(GO_PACKAGE)

# NFR-06: stripped binary 40 MB or less, npm tarball 16 MB or less.
size-check: build
	./hack/check-size.sh $(DIST_DIR)/$(BINARY_NAME)

# Allowlist from docs/PLAN.md section 9.3: GPL-3.0, MIT, Apache-2.0, BSD.
LICENSE_ALLOWLIST := MIT,Apache-2.0,BSD-2-Clause,BSD-3-Clause,GPL-3.0
license-check:
	go run github.com/google/go-licenses@v1.6.0 check ./... \
		--allowed_licenses=$(LICENSE_ALLOWLIST)

linux-x64: data
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
		go build $(GO_BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-linux-x64 $(GO_PACKAGE)

linux-arm64: data
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=aarch64-linux-gnu-gcc \
		go build $(GO_BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-linux-arm64 $(GO_PACKAGE)

darwin-x64: data
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
		CC="clang -arch x86_64" \
		CGO_CFLAGS="-arch x86_64" CGO_LDFLAGS="-arch x86_64" \
		go build $(GO_BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-x64 $(GO_PACKAGE)

darwin-arm64: data
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		CC="clang -arch arm64" \
		CGO_CFLAGS="-arch arm64" CGO_LDFLAGS="-arch arm64" \
		go build $(GO_BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64 $(GO_PACKAGE)

# The grw Windows container builds "." at the repository root. This module's
# main package lives under cmd/, so override the container command while keeping
# the toolchain, GOOS, and compiler selection from its Containerfile.
win32-x64: data
	@mkdir -p $(DIST_DIR)
	podman run --rm -v "$(CURDIR):/app:Z" -w /app \
		-e GOARCH=amd64 -e BINARY_NAME=$(BINARY_NAME) -e GO_PACKAGE=$(GO_PACKAGE) \
		$(WINDOWS_IMAGE) \
		sh -c 'export GOOS=windows CC=x86_64-w64-mingw32-gcc; go build $(GO_BUILD_FLAGS) -o "$(DIST_DIR)/$${BINARY_NAME}-windows-$${GOARCH}.exe" $${GO_PACKAGE}'
	@mv $(DIST_DIR)/$(BINARY_NAME)-windows-amd64.exe $(DIST_DIR)/$(BINARY_NAME)-win32-x64.exe

win32-arm64: data
	@mkdir -p $(DIST_DIR)
	podman run --rm -v "$(CURDIR):/app:Z" -w /app \
		-e GOARCH=arm64 -e BINARY_NAME=$(BINARY_NAME) -e GO_PACKAGE=$(GO_PACKAGE) \
		$(WINDOWS_IMAGE) \
		sh -c 'export GOOS=windows CC=aarch64-w64-mingw32-clang; go build $(GO_BUILD_FLAGS) -o "$(DIST_DIR)/$${BINARY_NAME}-windows-$${GOARCH}.exe" $${GO_PACKAGE}'
	@mv $(DIST_DIR)/$(BINARY_NAME)-windows-arm64.exe $(DIST_DIR)/$(BINARY_NAME)-win32-arm64.exe

clean:
	rm -rf dist
