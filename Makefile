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
#
# The vendored spec is byte-identical to the server repo's copy, which means it
# uses OpenAPI 3.1 constructs oapi-codegen can't parse: nullable $refs spelled
# `oneOf: [$ref, {type: "null"}]`, nullable type arrays (`type: [array, null]`),
# and a parameter union whose array branch has inline enum items, which makes
# codegen derive one name for both the branch and its item type and emit a
# self-referential alias. tools/speccompat rewrites the first two in place and
# hoists the third into components.schemas, all in a temp copy; the vendored
# file is never edited.
#
# The temp copy lives in an mktemp -d directory rather than a derived filename:
# `$$(mktemp -t foo).yaml` would write to a path mktemp never created (leaking
# the real one) and `-t` means different things on BSD and GNU. A unique dir
# plus a fixed name inside it is atomic, cleans up fully, and keeps the .yaml
# extension oapi-codegen selects its parser from.
codegen:
	@tmpd=$$(mktemp -d) || exit 1; \
		go run ./tools/speccompat api/inventory/cli-v1.yaml $$tmpd/cli-v1.yaml && \
		go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen \
			-config internal/inventory/gen/cfg.yaml \
			$$tmpd/cli-v1.yaml; \
		status=$$?; rm -rf $$tmpd; exit $$status

# CI gate: regenerate, assert the result COMPILES, then assert no drift across
# the entire gen tree (including new untracked files codegen might add).
#
# The build comes first on purpose. `make codegen` exits 0 even when it emits Go
# that cannot compile — that is how a `oneOf` whose array branch carried inline
# enum items shipped a self-referential type alias past this gate, surfacing
# later as an unrelated-looking failure in the test job. Compiling here names the
# real problem at the step that owns it, and a non-compiling tree is a worse
# failure than a drifted one, so it is worth reporting first.
codegen-check: codegen
	@go build ./... \
		|| (echo "ERROR: regenerated code does not compile. A spec construct oapi-codegen mishandles needs a rule in tools/speccompat (never edit the vendored spec)."; exit 1)
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
