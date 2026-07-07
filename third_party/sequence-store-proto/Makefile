TOOL := go tool -modfile=tools/go.mod

.PHONY: generate lint breaking test vuln all

all: generate lint test

generate:
	$(TOOL) buf generate

# Formatting is checked on hand-written packages only; *.pb.go is
# machine-produced.
lint:
	$(TOOL) buf lint
	go vet ./...
	@out="$$($(TOOL) gofumpt -l commitment devstore cmd)" || exit 1; \
	if [ -n "$$out" ]; then echo "gofumpt needed:"; echo "$$out"; exit 1; fi

# Compares the proto against the default branch; run before tagging.
breaking:
	$(TOOL) buf breaking --against '.git#branch=main'

test:
	go test -race ./...

vuln:
	$(TOOL) govulncheck ./...
