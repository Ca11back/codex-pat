GO ?= go
PYTHON ?= python3
VERSION ?= 0.1.5
DIST_DIR ?= dist
GOCACHE ?= $(CURDIR)/.cache/go-build
GOMODCACHE ?= $(CURDIR)/.cache/go-mod
GOOS ?= $(shell $(GO) env GOOS)
GOARCH ?= $(shell $(GO) env GOARCH)

ifeq ($(GOOS),linux)
LIB_EXT := .so
else ifeq ($(GOOS),darwin)
LIB_EXT := .dylib
else ifeq ($(GOOS),windows)
LIB_EXT := .dll
else
$(error unsupported release GOOS $(GOOS))
endif

ARTIFACT := $(DIST_DIR)/codex-pat-v$(VERSION)$(LIB_EXT)
ifeq ($(GOOS),windows)
BUILD_ARTIFACT := $(DIST_DIR)/codex_pat.dll
else
BUILD_ARTIFACT := $(ARTIFACT)
endif
RELEASE_DIR ?= $(DIST_DIR)/release
LDFLAGS := -s -w -X main.version=$(VERSION)
GO_RUN := GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) $(GO)

.PHONY: fmt test release-test vet verify build abi package verify-release manylinux integration load-smoke check clean

fmt:
	$(GO_RUN) fmt ./...

test:
	$(GO_RUN) test ./...

release-test:
	$(PYTHON) -m unittest scripts/release_test.py

vet:
	$(GO_RUN) vet ./...

verify:
	$(GO_RUN) mod verify

build:
	$(PYTHON) scripts/release.py validate-target --goos $(GOOS) --goarch $(GOARCH)
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=1 $(GO_RUN) build -trimpath -buildvcs=false -buildmode=c-shared -ldflags '$(LDFLAGS)' -o $(BUILD_ARTIFACT) ./cmd/codex-pat
ifeq ($(GOOS),windows)
	mv $(BUILD_ARTIFACT) $(ARTIFACT)
endif
	chmod 0755 $(ARTIFACT)

abi: build
	$(PYTHON) scripts/release.py verify-library --library $(ARTIFACT) --goos $(GOOS) --goarch $(GOARCH)

package: abi
	$(PYTHON) scripts/release.py package --version $(VERSION) --goos $(GOOS) --goarch $(GOARCH) --library $(ARTIFACT) --output $(RELEASE_DIR)

verify-release:
	$(PYTHON) scripts/release.py verify-release --version $(VERSION) --directory $(RELEASE_DIR)

manylinux:
	VERSION=$(VERSION) GOARCH=$(GOARCH) OUTPUT=$(ARTIFACT) bash scripts/build-manylinux.sh

integration: build
	CODEX_PAT_PLUGIN=$(ARTIFACT) CODEX_PAT_VERSION=$(VERSION) $(GO_RUN) test -tags=integration ./integration -count=1

load-smoke: abi
	test -n "$(CPA_BIN)"
	$(GO_RUN) run ./cmd/cpa-load-smoke -cpa "$(CPA_BIN)" -plugin "$(ARTIFACT)" -version "$(VERSION)"

check: fmt test release-test vet verify abi

clean:
	rm -rf $(DIST_DIR)
