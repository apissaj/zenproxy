BINARY=zenproxy
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo v0.1.0)
LDFLAGS=-s -w -X main.version=$(VERSION)

.PHONY: build run test vet clean release-snapshot

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

run:
	go run .

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY) $(BINARY).exe

release-snapshot:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_linux_amd64 .
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_linux_arm64 .
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_darwin_amd64 .
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_darwin_arm64 .
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_windows_amd64.exe .
	GOOS=windows GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)_windows_arm64.exe .
	@echo "Release artifacts in dist/"
