BINARY := mo
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

# Prefer GOBIN; fall back to GOPATH/bin. `go env` always returns a sensible
# default so this works without any user-specific config.
GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

.PHONY: build install clean

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/mo

install: build
	mkdir -p $(GOBIN)
	cp $(BINARY) $(GOBIN)/$(BINARY)

clean:
	rm -f $(BINARY)
