# opensearch-go-sdk

A Go client for [OpenSearch](https://opensearch.org/), generated from the
official [OpenSearch API Specification](https://github.com/opensearch-project/opensearch-api-specification)
with [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen), plus a
thin hand-written layer for the things a generator can't give you: retries,
typed errors, any-JSON documents and queries, bulk indexing at volume, deep
pagination and long-running tasks.

- Spec: **v0.2.0** (`spec/VERSION`, OpenSearch 3.x API surface)
- Generator: **oapi-codegen v2.8.0** (a `tool` directive in `go.mod`; no
  global install)
- Tested against: **OpenSearch 3.9.0**

Upgrading is bumping the spec pin and regenerating; nothing generated is
edited by hand.

## Layout

```
spec/
  opensearch-openapi.yaml   pinned release asset, untouched
  VERSION                   pinned version, URL and SHA-256
  LICENSE.txt               the specification's licence
  specprep.yaml             which operations to generate and how to name them
  overlay.yaml              OpenAPI Overlay with the remaining spec fixes
  build/                    prepared spec (generated, git-ignored)
tools/specprep/             the pre-processing step (Go)
opensearchapi/              generated package (*.gen.go) + oapi-codegen configs
opensearch/                 hand-written layer and tests
Makefile                    generate, check-generated, update-spec, vet, test
```

## Usage

```go
import (
	"github.com/oh-tarnished/opensearch-go-sdk/opensearch"
)

c, err := opensearch.New(opensearch.Config{
	URL:      "https://localhost:9200",
	Username: "admin", Password: "secret", // or BearerToken
	TLSConfig: tlsConfig,                  // or HTTPClient: myClient
	MaxRetries: 5,                         // 429/502/503/504 and transport errors
})

// Any JSON for bodies: maps, your own structs, json.RawMessage or a string.
err = c.CreateIndex(ctx, "resources", map[string]any{
	"mappings": map[string]any{"properties": map[string]any{
		"tenant": map[string]any{"type": "keyword"},
		"free":   map[string]any{"type": "date_range"},
	}},
})

res, err := c.Search(ctx, `{
  "query": {"bool": {"filter": [
    {"range": {"free": {"gte": "2026-11-02T10:00:00Z", "lte": "2026-11-02T11:00:00Z", "relation": "contains"}}},
    {"term": {"tenant": "acme"}}
  ]}}
}`, "resources")
docs, err := opensearch.Sources[Resource](res.Hits.Hits)   // typed decode of _source
buckets, ok, err := opensearch.Aggregation[Terms](res, "by_kind")

var oe *opensearch.Error
if errors.As(err, &oe) { log.Println(oe.StatusCode, oe.Type, oe.Reason) }
if opensearch.IsNotFound(err) { ... }
```

Volume work:

```go
// Streaming bulk indexer: batches by count and bytes, concurrent workers,
// Add blocks when the queue is full, 429 items are retried with backoff.
bi := opensearch.NewBulkIndexer(c, opensearch.BulkIndexerConfig{
	Index: "events", Workers: 4, FlushDocs: 5000, FlushBytes: 5 << 20,
	OnError: func(ctx context.Context, item opensearch.BulkIndexerItem, res opensearch.BulkItem, err error) { ... },
})
for _, e := range events {
	_ = bi.Add(ctx, opensearch.BulkIndexerItem{BulkOperation: opensearch.BulkOperation{
		Action: opensearch.ActionIndex, ID: e.ID, Body: e,
	}})
}
err = bi.Close(ctx) // flushes; bi.Stats() has the counters

// Every matching document, via point in time + search_after.
for hit, err := range c.Iterate(ctx, &opensearch.PageOptions{Query: q, Size: 5000}, "events") { ... }
// c.Scroll(...) is the same with the scroll API.

// Sliced delete_by_query as a task.
id, err := c.StartDeleteByQuery(ctx, map[string]any{"query": q}, &opensearch.ByQueryOptions{Slices: -1}, "events")
task, err := c.WaitForTask(ctx, id, time.Second)

// Zero-downtime reindex: new index, reindex task, atomic alias move.
_, err = c.SwapAlias(ctx, opensearch.AliasSwap{Alias: "events", NewIndex: "events-v2", NewIndexBody: mapping, Slices: -1, DeleteOld: true})
```

Also in `opensearch`: `Health`/`WaitForStatus`, `IndexExists`, `DeleteIndex`,
`PutMapping`/`GetMapping`, `Refresh`, `UpdateAliases`/`AliasIndices`/`GetAlias`,
`Index`/`Get`/`GetDocument[T]`/`Delete`, `Count`, `Bulk` with `BulkRequest`,
`MSearch`, `SubmitAsyncSearch`/`GetAsyncSearch`/`DeleteAsyncSearch`,
`UpdateByQuery`, `Reindex` (and `Start...` task variants), `GetTask`,
`PutIndexTemplate`/`IndexTemplateExists`/`DeleteIndexTemplate`, and
`PutISMPolicy`/`GetISMPolicy`/`DeleteISMPolicy`/`AddISMPolicy`.

Everything else in the specification is on `c.API` (typed responses) and
`c.Raw` (`*http.Response`), which share the client's auth and retries:

```go
resp, err := c.API.CatIndicesWithResponse(ctx, nil)
```

Helpers build the generated union parameters: `opensearch.Indices(...)`,
`opensearch.Names(...)`, `opensearch.Refresh(...)`, `opensearch.Slices(n)`.

## Generating

```sh
make generate          # = check-spec + go generate ./...
make check-generated   # regenerate and fail if anything changed (CI)
```

`go generate` runs three steps (see `opensearchapi/doc.go`):

1. `tools/specprep` reads `spec/opensearch-openapi.yaml` and
   `spec/specprep.yaml` and writes `spec/build/opensearch-openapi.prepared.yaml`.
2. oapi-codegen generates `opensearchapi/types.gen.go`.
3. oapi-codegen generates `opensearchapi/client.gen.go`.

Both oapi-codegen runs apply `spec/overlay.yaml`. Output is deterministic.

### Choosing operations

`spec/specprep.yaml` keeps every `x-operation-group` by default
(`include: ["*"]`). Narrow `include`, or add a group glob to `exclude` or an
operation id to `exclude-operations`, each a one-line change. Rename an
operation with `names: {original.id: GoName}`.

Operation names are stable: within a group the variant with the fewest path
parameters gets the group's name and the others append the parameters they
add (`Search`, `SearchWithIndex`, `IndicesGetAliasWithIndexAndName`), so a
spec release that reorders variants doesn't rename methods.

### Upgrading the specification

```sh
make update-spec VERSION=v0.3.0   # downloads, pins SHA-256, regenerates
make vet test
```

Then review the diff of `opensearchapi/`, fix any overlay action that no
longer matches (oapi-codegen applies overlays strictly and names the failing
target), and add a CHANGELOG entry. Releases are listed at
<https://github.com/opensearch-project/opensearch-api-specification/releases>.

## What is generated and what was changed

The whole specification generates and compiles: **531 operations in 418
operation groups**. Of the spec's 733 operations, 202 are not generated:

| Left out | Count | Why |
| --- | --- | --- |
| Deprecated operations (`skip-deprecated: true`) | 121 | Old `{type}` paths, `_opendistro` plugin paths and renamed APIs. Each has a generated replacement except the 6 groups below. |
| Equivalent methods on the same path | 75 | GET and POST `/_search`, PUT and POST `/{index}/_doc/{id}`, ...: one method per (group, path), by `method-priority`. |
| `/_aliases/...` forms of put_alias/delete_alias | 6 | Synonyms of the `/_alias/...` forms, which are generated. |

Groups with no generated operation (all deprecated): `cat.master` (use
`cat.cluster_manager`), `ml.load_model`, `ml.unload_model`,
`ml.upload_model`, `ml.chunk_model`, `ml.create_model_meta` (use the
register/deploy/undeploy APIs). Set `skip-deprecated: false` to get them.

Spec fixes, in the order they apply:

| Where | Fix | Why |
| --- | --- | --- |
| specprep | Positional ids (`search.3`) become Go names | Stable, readable method names. |
| specprep | `oneOf: [X, array of X]` parameters become arrays of X, `explode: false` | oapi-codegen generates a union over an undefined type (`N1`) that doesn't compile; OpenSearch takes comma-separated lists. |
| specprep | `type: [T, "null"]` becomes `type: T`; other multi-type schemas become untyped, or `string` for parameters | OpenAPI 3.1 type arrays are unsupported by oapi-codegen (and ogen). |
| specprep | Leading `_` dropped from component names, references rewritten | `CommonMappingProperty`, not `UnderscoreCommonMappingProperty`. |
| specprep | `x-go-name` on `_id`, `_source`, ... properties (`ID`, `Source`) unless a sibling would collide | Idiomatic field names. |
| overlay | Generic type parameters (`TDocument`, `T`, `TBucket`), `QueryContainer`, `AggregationContainer`, `Aggregate` are `json.RawMessage` | Documents, query DSL and aggregations pass through untouched. |
| overlay | `_source` of `GetResult` and `InlineGet` is `json.RawMessage` | Documents are any JSON. |
| overlay | `HitsMetadata.hits` items are `Hit` (not `allOf [Hit, {_source}]`) | Avoids an anonymous struct per hits array. |
| overlay | `nodes.info` `{node_id_or_metric}` is a string | Its inline union doesn't compile. |
| oapi-codegen config | `always-prefix-enum-values`, `ToCamelCaseWithInitialisms` | Enum constants collided with the `Client` type; Go initialisms. |

Known cosmetic leftovers: query parameters named `_source`,
`_source_includes` and `_source_excludes` generate fields called
`UnderscoreSource...` (a parameter `x-go-name` would also rename the shared
parameter type), and `_name`/`_nodes` keep the prefix where a sibling
`name`/`nodes` exists.

### Why oapi-codegen

oapi-codegen v2.8.0 (the requested generator) handles the 2.5 MB spec once
the 3.1 multi-type schemas are rewritten, generates in about 3 seconds, and
gives both a raw client and a typed one with `...WithBody` variants that take
any `io.Reader`, which is what makes any-JSON bodies and NDJSON (bulk,
msearch) possible without fighting the generator. ogen v1.24.0 rejects the
unmodified spec on the same multi-type schemas and generates strict,
validating decoders, which would turn every spec inaccuracy into a runtime
decode error; openapi-generator needs a JVM. oapi-codegen was kept.

## Testing

```sh
make test   # unit tests (httptest); integration tests skip without OPENSEARCH_URL

docker run -d --name opensearch-test -p 19200:9200 -e discovery.type=single-node \
  -e DISABLE_SECURITY_PLUGIN=true -e OPENSEARCH_JAVA_OPTS="-Xms512m -Xmx512m" \
  opensearchproject/opensearch:3.9.0
make test-integration   # OPENSEARCH_URL defaults to http://localhost:19200
```

The integration tests cover a `date_range` + keyword mapping with arrays of
free intervals and a `range` query with `relation: contains` plus term
filters, per-item bulk errors, 100k documents through the bulk indexer,
point-in-time iteration of all of them, scroll, aggregations, msearch, async
search, a sliced delete_by_query task, an alias-swap reindex, index
templates and ISM policies. `OPENSEARCH_USERNAME`/`OPENSEARCH_PASSWORD` are
used when set.

## Releasing

1. Move the "Unreleased" entries in `CHANGELOG.md` under `## [X.Y.Z] - date`.
2. Commit, then push a tag `vX.Y.Z`.

The `release` workflow checks the generated code, runs the unit and
integration tests, and publishes a GitHub release: the pinned specification
version, this version's `CHANGELOG.md` section, then the merged pull
requests.

Every Monday the `spec update` workflow (also runnable by hand, optionally
with a version) looks for a newer specification release. When there is one
it re-pins it, regenerates, runs the unit tests, adds the versions and the
operations added and removed under "Unreleased", and opens a pull request.
Merge it and tag to release. Dependabot opens weekly pull requests for Go
modules and GitHub Actions; security alerts and fixes are on.

## Licence

Apache-2.0 (see `LICENSE` and `NOTICE`). The specification in `spec/` is
Apache-2.0, Copyright OpenSearch Contributors.
