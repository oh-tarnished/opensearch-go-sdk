# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `opensearchapi`: client and models generated with oapi-codegen v2.8.0 from
  the OpenSearch API Specification v0.2.0 (OpenSearch 3.x): 531 operations
  in 418 operation groups, every namespace of the specification.
- `tools/specprep`: selects operation groups, picks one method per path,
  derives stable Go operation names, and rewrites the OpenAPI 3.1 constructs
  oapi-codegen can't express.
- `spec/overlay.yaml`: OpenAPI Overlay with the remaining fixes; documents,
  query DSL and aggregations are `json.RawMessage`.
- `opensearch`: hand-written client construction (basic/bearer auth, TLS,
  `http.Client` injection, retries with backoff on 429/5xx), typed `Error`,
  generic decode helpers, index/alias/mapping/template calls, document
  calls, search, count, multi-search, asynchronous search, bulk requests
  with per-item errors, a streaming `BulkIndexer`, point-in-time and scroll
  iterators, delete/update-by-query and reindex with slices and task
  polling, alias-swap reindexing, and ISM policy calls.
- Unit tests (httptest) and integration tests against OpenSearch 3.9.0.
