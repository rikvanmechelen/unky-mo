BINARY := mo
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

# Prefer GOBIN; fall back to GOPATH/bin. `go env` always returns a sensible
# default so this works without any user-specific config.
GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

.PHONY: build install clean test test-race test-expectfail test-integration mocks mocks-check

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/mo

install: build
	mkdir -p $(GOBIN)
	cp $(BINARY) $(GOBIN)/$(BINARY)
	@echo "Installed $(BINARY) to $(GOBIN)/$(BINARY)"
	@case ":$$PATH:" in \
		*":$(GOBIN):"*) ;; \
		*) echo "Note: $(GOBIN) is not on your PATH. Add it to run '$(BINARY)' from anywhere." ;; \
	esac

clean:
	rm -f $(BINARY)

test:
	go test ./...

test-race:
	go test -race ./...

test-expectfail:
	@go test -tags expectfail ./... || true

# Integration tests that spin up a real (isolated) tmux server and exercise
# the claude session-detection + tmux-pane attribution code end-to-end.
# Requires tmux.
test-integration:
	go test -tags integration ./internal/tmux/... ./internal/integration/... ./internal/tui/sidebar/...

# Regenerate all gomock mocks. Requires mockgen — `go install go.uber.org/mock/mockgen@latest`.
mocks:
	@command -v mockgen >/dev/null 2>&1 || { echo "mockgen not found — run: go install go.uber.org/mock/mockgen@latest"; exit 1; }
	go generate ./...

# CI check: mocks are up-to-date.
mocks-check: mocks
	@git diff --exit-code -- '*mock_*.go' || { echo "mocks are stale — run 'make mocks' and commit the result"; exit 1; }
