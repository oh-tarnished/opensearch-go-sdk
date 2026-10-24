# GOWORK=off keeps a go.work in a parent directory from changing what builds.
export GOWORK := off

SPEC := spec/opensearch-openapi.yaml

.PHONY: all generate check-spec update-spec vet test test-integration check-generated

all: generate vet test

## generate: check the spec pin, prepare the spec and regenerate opensearchapi
generate: check-spec
	go generate ./...

## check-spec: verify the pinned spec matches spec/VERSION
check-spec:
	@want=$$(sed -n 's/^SHA256=//p' spec/VERSION); \
	got=$$(shasum -a 256 $(SPEC) | cut -d' ' -f1); \
	if [ "$$want" != "$$got" ]; then echo "$(SPEC): sha256 $$got, spec/VERSION pins $$want"; exit 1; fi

## update-spec VERSION=vX.Y.Z: download a spec release and pin it
update-spec:
	@test -n "$(VERSION)" || { echo "usage: make update-spec VERSION=vX.Y.Z"; exit 1; }
	curl -fsSL -o $(SPEC) https://github.com/opensearch-project/opensearch-api-specification/releases/download/$(VERSION)/opensearch-openapi.yaml
	curl -fsSL -o spec/LICENSE.txt https://github.com/opensearch-project/opensearch-api-specification/releases/download/$(VERSION)/LICENSE.txt
	sum=$$(shasum -a 256 $(SPEC) | cut -d' ' -f1); \
	sed -i.bak -e "s|^VERSION=.*|VERSION=$(VERSION)|" \
		-e "s|^URL=.*|URL=https://github.com/opensearch-project/opensearch-api-specification/releases/download/$(VERSION)/opensearch-openapi.yaml|" \
		-e "s|^SHA256=.*|SHA256=$$sum|" spec/VERSION && rm spec/VERSION.bak
	$(MAKE) generate

## check-generated: fail when regenerating changes anything
check-generated: generate
	git diff --exit-code -- opensearchapi go.mod go.sum
	@untracked=$$(git ls-files --others --exclude-standard opensearchapi); \
	if [ -n "$$untracked" ]; then echo "untracked generated files: $$untracked"; exit 1; fi

vet:
	go vet ./...
	@out=$$(gofmt -l opensearch tools opensearchapi); if [ -n "$$out" ]; then echo "gofmt: $$out"; exit 1; fi

test:
	go test -race ./...

## test-integration: run every test against OPENSEARCH_URL (default a local container on 19200)
test-integration:
	OPENSEARCH_URL=$${OPENSEARCH_URL:-http://localhost:19200} go test -race -count=1 -timeout 15m ./...
