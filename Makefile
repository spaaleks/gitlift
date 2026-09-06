BINARY  := gitlift
STATICCHECK ?= honnef.co/go/tools/cmd/staticcheck@v0.6.1
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: run build snapshot release install test lint fmt tools clean

run:
	VERSION=$(VERSION) bin/run.sh

build:
	VERSION=$(VERSION) bin/build-binary.sh

snapshot:
	VERSION=$(VERSION) bin/build-binary.sh --snapshot

release:
	VERSION=$(VERSION) bin/build-binary.sh --release

install:
	CGO_ENABLED=0 go install \
		-ldflags "-s -w -X github.com/spaaleks/gitlift.Version=$(VERSION)" \
		./cmd/$(BINARY)

test:
	go test ./...

GOFILES := $(shell find . -type f -name '*.go' -not -path './.cache/*' -not -path './dist/*')

lint:
	go vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	else \
		echo "staticcheck not installed, skipping (make tools)"; \
	fi
	@unformatted=$$(gofmt -l $(GOFILES)); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi

fmt:
	gofmt -w $(GOFILES)

tools:
	go install $(STATICCHECK)

clean:
	rm -rf dist
