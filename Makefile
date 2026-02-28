# See https://tech.davis-hansson.com/p/make/
SHELL := bash
.DELETE_ON_ERROR:
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := all
MAKEFLAGS += --warn-undefined-variables
MAKEFLAGS += --no-builtin-rules
MAKEFLAGS += --no-print-directory
BIN := .tmp/bin
export PATH := $(abspath $(BIN)):$(PATH)
export GOBIN := $(abspath $(BIN))
LICENSE_TYPE := proprietary
COPYRIGHT_HOLDER := Peter Edge
COPYRIGHT_YEARS := 2026
LICENSE_IGNORE := --ignore testdata/

UNAME_OS := $(shell uname -s)
UNAME_ARCH := $(shell uname -m)
BUF_VERSION := v1.66.0
GO_MOD_GOTOOLCHAIN := go1.26.0
GOLANGCI_LINT_VERSION := v2.10.1
ifeq ($(UNAME_OS),Darwin)
GOLANGCI_LINT_OS := darwin
else ifeq ($(UNAME_OS),Linux)
GOLANGCI_LINT_OS := linux
endif
ifeq ($(UNAME_ARCH),x86_64)
GOLANGCI_LINT_ARCH := amd64
else ifeq ($(UNAME_ARCH),arm64)
GOLANGCI_LINT_ARCH := arm64
else ifeq ($(UNAME_ARCH),aarch64)
GOLANGCI_LINT_ARCH := arm64
else
GOLANGCI_LINT_ARCH := $(UNAME_ARCH)
endif

GO_GET_PKGS ?=

.PHONY: help
help: ## Describe useful make targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "%-30s %s\n", $$1, $$2}'

.PHONY: all
all: ## lint and test (default)
	$(MAKE) lint
	$(MAKE) test

.PHONY: clean
clean: ## Delete intermediate build artifacts
	git clean -Xdf

.PHONY: build
build: ## Build all packages
	go build ./...

.PHONY: test
test: ## Run unit tests
	go test -vet=off -race ./...

.PHONY: lint
lint: $(BIN)/golangci-lint ## Lint
	@$(MAKE) checknodiffgenerated
	go vet ./...
	golangci-lint run --modules-download-mode=readonly --timeout=3m0s
	buf lint

.PHONY: lintfix
lintfix: $(BIN)/golangci-lint ## Automatically fix some lint errors
	golangci-lint run --fix --modules-download-mode=readonly --timeout=3m0s

.PHONY: install
install: ## Install all binaries
	go install ./...

.PHONY: generate
generate: $(BIN)/buf $(BIN)/protoc-gen-go $(BIN)/license-header ## Regenerate code and licenses
	buf generate
	license-header --license-type "$(LICENSE_TYPE)" --copyright-holder "$(COPYRIGHT_HOLDER)" --year-range "$(COPYRIGHT_YEARS)" $(LICENSE_IGNORE)
	@echo gofmt -s -w GO_FILES
	@gofmt -s -w $(shell find . -name '*.go')
	buf format -w

.PHONY: checknodiffgenerated
checknodiffgenerated:
	@ if [[ -d .git || -f .git ]]; then \
			$(MAKE) __checknodiffgeneratedinternal; \
		else \
			echo "skipping make checknodiffgenerated due to no .git repository" >&2; \
		fi

.PHONY: upgrade
upgrade: ## Upgrade dependencies
	go mod edit -toolchain=$(GO_MOD_GOTOOLCHAIN)
	go get -u -t ./... $(GO_GET_PKGS)
	go mod tidy -v

$(BIN)/buf: Makefile
	@mkdir -p $(BIN)
	go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)

$(BIN)/license-header: Makefile
	@mkdir -p $(BIN)
	go install github.com/bufbuild/buf/private/pkg/licenseheader/cmd/license-header@$(BUF_VERSION)

$(BIN)/golangci-lint: Makefile
	$(eval DIR=$(abspath $(BIN)))
	@mkdir -p $(DIR)
	$(eval GOLANGCI_LINT_TMP := $(shell mktemp -d))
	cd $(GOLANGCI_LINT_TMP); \
		curl -fsSL -o $(GOLANGCI_LINT_TMP)/golangci-lint.tar.gz \
			https://github.com/golangci/golangci-lint/releases/download/$(GOLANGCI_LINT_VERSION)/golangci-lint-$(subst v,,$(GOLANGCI_LINT_VERSION))-$(GOLANGCI_LINT_OS)-$(GOLANGCI_LINT_ARCH).tar.gz && \
		tar zxf $(GOLANGCI_LINT_TMP)/golangci-lint.tar.gz --strip-components 1 && \
		cp golangci-lint $(DIR)/golangci-lint
	@rm -rf $(GOLANGCI_LINT_TMP)

.PHONY: $(BIN)/protoc-gen-go
$(BIN)/protoc-gen-go:
	@mkdir -p $(BIN)
	go install google.golang.org/protobuf/cmd/protoc-gen-go

.PHONY: __checknodiffgeneratedinternal
__checknodiffgeneratedinternal:
	@echo bash etc/script/checknodiffgenerated.bash make generate
	@bash etc/script/checknodiffgenerated.bash $(MAKE) generate
