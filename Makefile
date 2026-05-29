DIST := dist
EXECUTABLE := gitea-runner
DIST_DIRS := $(DIST)/binaries $(DIST)/release
GO ?= go
SHASUM ?= shasum -a 256
HAS_GO = $(shell hash $(GO) > /dev/null 2>&1 && echo "GO" || echo "NOGO" )
XGO_PACKAGE ?= src.techknowlogick.com/xgo@latest
XGO_VERSION := go-1.26.x
GXZ_PACKAGE ?= github.com/ulikunitz/xz/cmd/gxz@v0.5.10

LINUX_ARCHS ?= linux/amd64,linux/arm64
DARWIN_ARCHS ?= darwin-12/amd64,darwin-12/arm64
WINDOWS_ARCHS ?= windows/amd64
GOFILES := $(shell find . -type f -name "*.go" -o -name "go.mod" ! -name "generated.*")

DOCKER_IMAGE ?= gitea/runner
DOCKER_TAG ?= nightly
DOCKER_REF := $(DOCKER_IMAGE):$(DOCKER_TAG)
DOCKER_ROOTLESS_REF := $(DOCKER_IMAGE):$(DOCKER_TAG)-dind-rootless

GOLANGCI_LINT_PACKAGE ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
GOVULNCHECK_PACKAGE ?= golang.org/x/vuln/cmd/govulncheck@v1.3.0

STATIC ?=
EXTLDFLAGS ?=
ifneq ($(STATIC),)
	EXTLDFLAGS = -extldflags "-static"
endif

ifeq ($(HAS_GO), GO)
	GOPATH ?= $(shell $(GO) env GOPATH)
	export PATH := $(GOPATH)/bin:$(PATH)

	CGO_EXTRA_CFLAGS := -DSQLITE_MAX_VARIABLE_NUMBER=32766
	CGO_CFLAGS ?= $(shell $(GO) env CGO_CFLAGS) $(CGO_EXTRA_CFLAGS)
endif

ifeq ($(OS), Windows_NT)
	GOFLAGS := -v -buildmode=exe
	EXECUTABLE ?= $(EXECUTABLE).exe
else ifeq ($(OS), Windows)
	GOFLAGS := -v -buildmode=exe
	EXECUTABLE ?= $(EXECUTABLE).exe
else
	GOFLAGS := -v
	EXECUTABLE ?= $(EXECUTABLE)
endif

STORED_VERSION_FILE := VERSION

ifneq ($(DRONE_TAG),)
	VERSION ?= $(subst v,,$(DRONE_TAG))
	RELASE_VERSION ?= $(VERSION)
else
	ifneq ($(DRONE_BRANCH),)
		VERSION ?= $(subst release/v,,$(DRONE_BRANCH))
	else
		VERSION ?= main
	endif

	STORED_VERSION=$(shell cat $(STORED_VERSION_FILE) 2>/dev/null)
	ifneq ($(STORED_VERSION),)
		RELASE_VERSION ?= $(STORED_VERSION)
	else
		RELASE_VERSION ?= $(shell git describe --tags --always | sed 's/-/+/' | sed 's/^v//')
	endif
endif

TAGS ?=
LDFLAGS ?= -X "gitea.com/gitea/runner/internal/pkg/ver.version=v$(RELASE_VERSION)"

.PHONY: all
all: build

.PHONY: help
help: Makefile ## print Makefile help information.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m[TARGETS] default target: build\033[0m\n\n\033[35mTargets:\033[0m\n"} /^[0-9A-Za-z._-]+:.*?##/ { printf "  \033[36m%-45s\033[0m %s\n", $$1, $$2 }' Makefile

.PHONY: fmt
fmt: ## format the Go code
	$(GO) run $(GOLANGCI_LINT_PACKAGE) fmt

.PHONY: go-check
go-check:
	$(eval MIN_GO_VERSION_STR := $(shell grep -Eo '^go\s+[0-9]+\.[0-9]+' go.mod | cut -d' ' -f2))
	$(eval MIN_GO_VERSION := $(shell printf "%03d%03d" $(shell echo '$(MIN_GO_VERSION_STR)' | tr '.' ' ')))
	$(eval GO_VERSION := $(shell printf "%03d%03d" $(shell $(GO) version | grep -Eo '[0-9]+\.[0-9]+' | tr '.' ' ');))
	@if [ "$(GO_VERSION)" -lt "$(MIN_GO_VERSION)" ]; then \
		echo "Gitea Runner requires Go $(MIN_GO_VERSION_STR) or greater to build. You can get it at https://go.dev/dl/"; \
		exit 1; \
	fi

.PHONY: fmt-check
fmt-check: fmt
	@diff=$$(git diff --color=always); \
	if [ -n "$$diff" ]; then \
		echo "Please run 'make fmt' and commit the result:"; \
		printf "%s" "$${diff}"; \
		exit 1; \
	fi

.PHONY: deps-tools
deps-tools: ## install tool dependencies
	$(GO) install $(GOLANGCI_LINT_PACKAGE) & \
	$(GO) install $(GXZ_PACKAGE) & \
	$(GO) install $(XGO_PACKAGE) & \
	$(GO) install $(GOVULNCHECK_PACKAGE) & \
	wait

.PHONY: lint
lint: lint-go ## lint everything

.PHONY: lint-go
lint-go: ## lint go files
	$(GO) run $(GOLANGCI_LINT_PACKAGE) run

.PHONY: lint-go-fix
lint-go-fix: ## lint go files and fix issues
	$(GO) run $(GOLANGCI_LINT_PACKAGE) run --fix

.PHONY: lint-pr-title
lint-pr-title: ## lint PR title against Conventional Commits (set PR_TITLE=...)
	@node ./tools/lint-pr-title.ts

.PHONY: security-check
security-check: deps-tools
	GOEXPERIMENT= $(GO) run $(GOVULNCHECK_PACKAGE) -show color ./... || true

.PHONY: tidy
tidy: ## run go mod tidy
	$(GO) mod tidy

.PHONY: tidy-check
tidy-check: tidy
	@diff=$$(git diff --color=always -- go.mod go.sum); \
	if [ -n "$$diff" ]; then \
		echo "Please run 'make tidy' and commit the result:"; \
		printf "%s" "$${diff}"; \
		exit 1; \
	fi

.PHONY: test
test: fmt-check security-check ## test everything (integration tests self-skip without docker/network)
	@$(GO) test -race -timeout 20m -v -cover -coverprofile coverage.txt ./... && echo "\n==>\033[32m Ok\033[m\n" || exit 1

.PHONY: test-dind
test-dind: ## run the daemon-facing tests against the built dind image (TARGET=dind|dind-rootless)
	@./scripts/test-dind.sh $(TARGET)

.PHONY: install
install: $(GOFILES) ## install the runner binary via `go install`
	$(GO) install -v -tags '$(TAGS)' -ldflags '-s -w $(EXTLDFLAGS) $(LDFLAGS)'

.PHONY: build
build: go-check $(EXECUTABLE) ## build the runner binary

$(EXECUTABLE): $(GOFILES)
	$(GO) build -v -tags '$(TAGS)' -ldflags '-s -w $(EXTLDFLAGS) $(LDFLAGS)' -o $@

.PHONY: deps-backend
deps-backend: ## install backend dependencies
	$(GO) mod download

.PHONY: release
release: release-windows release-linux release-darwin release-copy release-compress release-check ## build release artifacts

$(DIST_DIRS):
	mkdir -p $(DIST_DIRS)

.PHONY: release-windows
release-windows: | $(DIST_DIRS)
	CGO_CFLAGS="$(CGO_CFLAGS)" $(GO) run $(XGO_PACKAGE) -go $(XGO_VERSION) -buildmode exe -dest $(DIST)/binaries -tags 'netgo osusergo $(TAGS)' -ldflags '-s -w -linkmode external -extldflags "-static" $(LDFLAGS)' -targets '$(WINDOWS_ARCHS)' -out $(EXECUTABLE)-$(VERSION) .
ifeq ($(CI),true)
	cp -r /build/* $(DIST)/binaries/
endif

.PHONY: release-linux
release-linux: | $(DIST_DIRS)
	CGO_CFLAGS="$(CGO_CFLAGS)" $(GO) run $(XGO_PACKAGE) -go $(XGO_VERSION) -dest $(DIST)/binaries -tags 'netgo osusergo $(TAGS)' -ldflags '-s -w -linkmode external -extldflags "-static" $(LDFLAGS)' -targets '$(LINUX_ARCHS)' -out $(EXECUTABLE)-$(VERSION) .
ifeq ($(CI),true)
	cp -r /build/* $(DIST)/binaries/
endif

.PHONY: release-darwin
release-darwin: | $(DIST_DIRS)
	CGO_CFLAGS="$(CGO_CFLAGS)" $(GO) run $(XGO_PACKAGE) -go $(XGO_VERSION) -dest $(DIST)/binaries -tags 'netgo osusergo $(TAGS)' -ldflags '-s -w $(LDFLAGS)' -targets '$(DARWIN_ARCHS)' -out $(EXECUTABLE)-$(VERSION) .
ifeq ($(CI),true)
	cp -r /build/* $(DIST)/binaries/
endif

.PHONY: release-copy
release-copy: | $(DIST_DIRS)
	cd $(DIST); for file in `find . -type f -name "*"`; do cp $${file} ./release/; done;

.PHONY: release-check
release-check: | $(DIST_DIRS)
	cd $(DIST)/release/; for file in `find . -type f -name "*"`; do echo "checksumming $${file}" && $(SHASUM) `echo $${file} | sed 's/^..//'` > $${file}.sha256; done;

.PHONY: release-compress
release-compress: | $(DIST_DIRS)
	cd $(DIST)/release/; for file in `find . -type f -name "*"`; do echo "compressing $${file}" && $(GO) run $(GXZ_PACKAGE) -k -9 $${file}; done;

.PHONY: docker
docker: ## build the docker image
	if ! docker buildx version >/dev/null 2>&1; then \
		ARG_DISABLE_CONTENT_TRUST=--disable-content-trust=false; \
	fi; \
	docker build $${ARG_DISABLE_CONTENT_TRUST} -t $(DOCKER_REF) .

.PHONY: clean
clean: ## delete binary and coverage files
	$(GO) clean -x -i ./...
	rm -rf coverage.txt $(EXECUTABLE) $(DIST)

.PHONY: version
version: ## print the version
	@echo $(VERSION)
