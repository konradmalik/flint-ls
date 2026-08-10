BIN := flint-ls
VERSION := $$(make -s show-version)
CURRENT_REVISION := $(shell git rev-parse --short HEAD)
BUILD_LDFLAGS := "-s -w -X main.revision=$(CURRENT_REVISION)"
# release tooling lives in its own module, so it is built here rather than being
# a dependency of the server itself
TOOLS_BIN := $(CURDIR)/bin
export GO111MODULE=on

.PHONY: all
all: clean build

.PHONY: build
build:
	go build -ldflags=$(BUILD_LDFLAGS) -o $(BIN) .

.PHONY: install
install:
	go install -ldflags=$(BUILD_LDFLAGS) .

.PHONY: show-version
show-version: $(TOOLS_BIN)/gobump
	@$(TOOLS_BIN)/gobump show -r .

.PHONY: cross
cross: $(TOOLS_BIN)/goxz
	$(TOOLS_BIN)/goxz -n $(BIN) -pv=v$(VERSION) -build-ldflags=$(BUILD_LDFLAGS) .

.PHONY: test
test: build
	go test -v ./...

EFMLS_CONFIGS_DIR ?= core/testdata/efmls-configs

# the corpus of linter and formatter configs that the compatibility tests check
# flint-ls against. Not in git, so a clean checkout -- which is every CI run -- gets
# whatever upstream ships that day, while a working copy keeps the clone it already
# has. `rm -rf $(EFMLS_CONFIGS_DIR)` is how you refresh one by hand.
#
# Cloned aside and moved into place so that an interrupted clone leaves no directory
# for the next run to mistake for a finished one.
$(EFMLS_CONFIGS_DIR):
	rm -rf "$@.partial"
	git clone --quiet --depth 1 --single-branch \
		https://github.com/creativenull/efmls-configs-nvim "$@.partial"
	mv "$@.partial" "$@"

# the compatibility tests, insisting on a corpus rather than skipping when there is
# none. Needs an nvim on PATH, because the configs are lua that decides what to return
# when it is loaded; `nix develop .#ci` has one for machines without.
#
# -count=1 because the corpus is read by nvim rather than by the test, so go's test
# cache cannot see it change and would answer a refreshed corpus from a stale pass.
.PHONY: test-efmls
test-efmls: $(EFMLS_CONFIGS_DIR)
	EFMLS_REQUIRE_CORPUS=1 go test ./core -run TestEfmls -v -count=1

# everything, for when you want the compatibility tests included rather than skipped.
# `test` on its own stays offline and needs no nvim, which is what CI's os matrix runs.
#
# The corpus comes first so that the run inside `test` finds one, instead of reporting
# a skip for tests that `test-efmls` goes on to run anyway.
.PHONY: test-all
test-all: $(EFMLS_CONFIGS_DIR) test test-efmls

.PHONY: lint
lint:
	go mod tidy
	go mod vendor
	go -C tools mod tidy
	golangci-lint run ./...

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: clean
clean:
	rm -rf $(BIN) goxz $(TOOLS_BIN)
	go clean

.PHONY: bump
bump: $(TOOLS_BIN)/gobump
ifneq ($(shell git status --porcelain),)
	$(error git workspace is dirty)
endif
ifneq ($(shell git rev-parse --abbrev-ref HEAD),main)
	$(error current branch is not main)
endif
	@$(TOOLS_BIN)/gobump up -w .
	git commit -am "bump up version to $(VERSION)"
	git tag "v$(VERSION)"
	git push origin main
	git push origin "refs/tags/v$(VERSION)"

.PHONY: upload
upload: $(TOOLS_BIN)/ghr
	$(TOOLS_BIN)/ghr "v$(VERSION)" goxz

.PHONY: tools
tools: $(TOOLS_BIN)/goxz $(TOOLS_BIN)/ghr $(TOOLS_BIN)/gobump

$(TOOLS_BIN)/goxz: tools/go.mod tools/go.sum
	go build -C tools -o $@ github.com/Songmu/goxz/cmd/goxz

$(TOOLS_BIN)/ghr: tools/go.mod tools/go.sum
	go build -C tools -o $@ github.com/tcnksm/ghr

$(TOOLS_BIN)/gobump: tools/go.mod tools/go.sum
	go build -C tools -o $@ github.com/x-motemen/gobump/cmd/gobump
