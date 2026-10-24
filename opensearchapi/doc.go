// Package opensearchapi is generated from the OpenSearch API Specification
// pinned in spec/ by oapi-codegen. Don't edit the *.gen.go files: change
// spec/specprep.yaml, spec/overlay.yaml or the oapi-codegen configs here and
// run `go generate ./...` (or `make generate`).
//
// Client returns *http.Response; ClientWithResponses decodes typed
// responses. Every operation has a ...WithBody variant that takes any
// io.Reader, which is how the opensearch package sends arbitrary JSON.
// Documents, query DSL, aggregation requests and aggregation results are
// json.RawMessage throughout.
package opensearchapi

//go:generate go run ../tools/specprep -config ../spec/specprep.yaml
//go:generate go tool oapi-codegen -config types.cfg.yaml ../spec/build/opensearch-openapi.prepared.yaml
//go:generate go tool oapi-codegen -config client.cfg.yaml ../spec/build/opensearch-openapi.prepared.yaml
