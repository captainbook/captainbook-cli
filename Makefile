BINARY := ceebee
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X github.com/captainbook/captainbook-cli/cmd.Version=$(VERSION)"
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

.PHONY: build test lint clean build-all

build:
	go build $(LDFLAGS) -o $(BINARY) .

test:
	go test ./... -v -count=1

lint:
	go vet ./...

clean:
	rm -f $(BINARY) $(BINARY)-*

build-all:
	@for platform in $(PLATFORMS); do \
		os=$$(echo $$platform | cut -d/ -f1); \
		arch=$$(echo $$platform | cut -d/ -f2); \
		output=$(BINARY)-$$os-$$arch; \
		echo "Building $$output..."; \
		GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o $$output .; \
	done
