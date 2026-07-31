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
