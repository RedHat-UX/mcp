# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright 2026 Red Hat, Inc.

BINARY_NAME := rhds-mcp
VERSION ?= 0.0.1
DIST_DIR := dist/bin
GO_PACKAGE := ./cmd/rhds-mcp
GO_LDFLAGS := -s -w -X main.serverVersion=$(VERSION)
GO_BUILD_FLAGS := -trimpath -ldflags="$(GO_LDFLAGS)"

# CI sets WINDOWS_IMAGE via go-release-workflows; override for local dev.
WINDOWS_IMAGE ?= grw-windows-cross

.DEFAULT_GOAL := build

.PHONY: data data-update build linux-x64 linux-arm64 darwin-x64 darwin-arm64 win32-x64 win32-arm64 clean

# Generated data is optional until tools/gen-data lands in Phase C. On a clean
# checkout there is nothing to do, so `make data` succeeds without network. The
# scoped no-op check that compares internal/data/dist/index.json with
# sources.lock.json (PLAN section 4.3) arrives with the generator in Phase C.
data:
	@if [ -d tools/gen-data ] && [ -d internal/data ]; then \
		go generate ./internal/data; \
	else \
		echo "make data: tools/gen-data is not present yet; skipping generated data"; \
	fi

data-update:
	@echo "make data-update: not implemented yet; this will refresh internal/data/sources.lock.json from npm and GitHub (Phase C)" >&2
	@exit 1

build: data
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 go build $(GO_BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME) $(GO_PACKAGE)

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
