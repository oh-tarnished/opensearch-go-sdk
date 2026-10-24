# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.1] - 2026-10-24

### Fixed

- The module path is `github.com/oh-tarnished/opensearch-go-sdk`, matching
  the repository; v0.1.0 declared `opensearch-go-client` and cannot be
  fetched.

### Added

- Releases: pushing a tag `vX.Y.Z` runs the generated-code check and the
  tests, then publishes a GitHub release whose notes are this file's section
  for the version plus the merged pull requests.
- Spec updates: a weekly workflow re-pins a newer OpenSearch API
  Specification, regenerates, tests, records the operations added and removed
  here, and opens a pull request.
- Dependabot: weekly updates for Go modules and GitHub Actions; security
  alerts and fixes are on.

## [0.1.0] - 2026-10-24

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
