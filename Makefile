BINARY := mo
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

# Prefer GOBIN; fall back to GOPATH/bin. `go env` always returns a sensible
# default so this works without any user-specific config.
GOBIN := $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

.PHONY: build install clean test test-race test-expectfail test-integration mocks mocks-check codemirror fmt fmt-check

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/mo

install: build
	mkdir -p $(GOBIN)
	@# Copy then rename: Linux refuses to overwrite a running binary in place
	@# ("Text file busy"), but a rename swaps the inode out from under it.
	cp $(BINARY) $(GOBIN)/$(BINARY).new && mv -f $(GOBIN)/$(BINARY).new $(GOBIN)/$(BINARY)
	@echo "Installed $(BINARY) to $(GOBIN)/$(BINARY)"
	@# Also install to the PATH location if it differs from GOBIN (e.g. stale ~/go/bin/mo).
	@PATH_MO=$$(command -v $(BINARY) 2>/dev/null); \
	if [ -n "$$PATH_MO" ] && [ "$$(cd "$$(dirname "$$PATH_MO")" && pwd)" != "$$(cd "$(GOBIN)" && pwd)" ]; then \
		cp $(BINARY) "$$PATH_MO.new" && mv -f "$$PATH_MO.new" "$$PATH_MO"; \
		echo "Also updated $$PATH_MO"; \
	fi
	@case ":$$PATH:" in \
		*":$(GOBIN):"*) ;; \
		*) echo "Note: $(GOBIN) is not on your PATH. Add it to run '$(BINARY)' from anywhere." ;; \
	esac

clean:
	rm -f $(BINARY)

fmt:
	gofmt -w $$(git ls-files '*.go')

# CI check: every Go file is gofmt'd.
fmt-check:
	@out=$$(gofmt -l $$(git ls-files '*.go')); if [ -n "$$out" ]; then echo "not gofmt'd (run 'make fmt'):"; echo "$$out"; exit 1; fi

# Formatting first, so an unformatted file fails locally as it would in CI.
test: fmt-check
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

# Regenerate all gomock mocks. go:generate invokes mockgen by name, so the
# pinned version (the go.uber.org/mock in go.mod) is installed into .bin/ and
# put first on PATH: an unpinned `mockgen@latest` would one day write
# different mocks and fail mocks-check for no reason.
MOCKGEN_VERSION := $(shell go list -m -f '{{.Version}}' go.uber.org/mock)
MOCKGEN := $(CURDIR)/.bin/mockgen-$(MOCKGEN_VERSION)

$(MOCKGEN):
	mkdir -p $(CURDIR)/.bin/tmp
	GOBIN=$(CURDIR)/.bin/tmp go install go.uber.org/mock/mockgen@$(MOCKGEN_VERSION)
	mv $(CURDIR)/.bin/tmp/mockgen $(MOCKGEN) && rmdir $(CURDIR)/.bin/tmp
	ln -sf $(notdir $(MOCKGEN)) $(CURDIR)/.bin/mockgen

mocks: $(MOCKGEN)
	PATH="$(CURDIR)/.bin:$$PATH" go generate ./...

# CI check: mocks are up-to-date.
mocks-check: mocks
	@git diff --exit-code -- '*mock_*.go' || { echo "mocks are stale — run 'make mocks' and commit the result"; exit 1; }

# Rebuild the vendored CodeMirror bundle (internal/web/static/vendor/codemirror.js)
# from tools/codemirror. Needs Node; only for bumping versions or exports —
# the bundle is committed, so build/install never need Node.
codemirror:
	cd tools/codemirror && npm ci --no-audit --no-fund && npm run build
