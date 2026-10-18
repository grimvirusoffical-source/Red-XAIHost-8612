# RedX host build. Requires Go 1.22+.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
TAGS ?=

.PHONY: all build test vet dist clean smoke
all: vet test build

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -tags "$(TAGS)" -ldflags "-s -w -X redx/host.Version=$(VERSION)" -o bin/redxd ./cmd/redxd
	CGO_ENABLED=0 go build -trimpath -tags "$(TAGS)" -ldflags "-s -w -X redx/host.Version=$(VERSION)" -o bin/redx ./cmd/redx

vet:
	go vet ./host/... ./cmd/...

test:
	go test -race -count=1 ./host/... ./cmd/...

# all platforms: linux amd64/arm64/armv7, darwin amd64/arm64, windows amd64/arm64
dist:
	VERSION=$(VERSION) TAGS="$(TAGS)" scripts/build.sh

smoke: build
	scripts/smoke.sh bin

clean:
	rm -rf bin dist
