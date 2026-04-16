BINARY := mo
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build install clean

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/mo

install: build
	cp $(BINARY) $(GOPATH)/bin/$(BINARY) 2>/dev/null || cp $(BINARY) ~/go/bin/$(BINARY)

clean:
	rm -f $(BINARY)
