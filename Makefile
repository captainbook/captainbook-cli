BINARY := ceebee
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X github.com/captainbook/captainbook-cli/cmd.Version=$(VERSION)"
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

.PHONY: build test lint clean build-all codegen codegen-check

build:
	go build $(LDFLAGS) -o $(BINARY) .

test:
	go test ./... -v -count=1

lint:
	go vet ./...

clean:
	rm -f $(BINARY) $(BINARY)-*

# Regenerate the Inventory CLI v1 client from api/inventory/cli-v1.yaml.
# Tool version is pinned in go.mod via tools.go.
codegen:
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen \
		-config internal/inventory/gen/cfg.yaml \
		api/inventory/cli-v1.yaml

# CI gate: regenerate, then assert no drift across the entire gen tree
# (including new untracked files codegen might add).
codegen-check: codegen
	@git diff --exit-code -- internal/inventory/gen/ \
		|| (echo "ERROR: codegen output drifted. Run 'make codegen' and commit."; exit 1)
	@untracked=$$(git ls-files --others --exclude-standard internal/inventory/gen/); \
		if [ -n "$$untracked" ]; then \
			echo "ERROR: codegen emitted untracked files:"; echo "$$untracked"; exit 1; \
		fi

build-all:
	@for platform in $(PLATFORMS); do \
		os=$$(echo $$platform | cut -d/ -f1); \
		arch=$$(echo $$platform | cut -d/ -f2); \
		output=$(BINARY)-$$os-$$arch; \
		echo "Building $$output..."; \
		GOOS=$$os GOARCH=$$arch go build $(LDFLAGS) -o $$output .; \
	done
