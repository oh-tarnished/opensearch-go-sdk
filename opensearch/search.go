package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/oh-tarnished/opensearch-go-sdk/opensearchapi"
)

// SearchResponse is a search response. Hits carry their _source as raw
// JSON (see Source and Sources); aggregations are raw JSON keyed by name
// (see Aggregation).
type SearchResponse = opensearchapi.Search200

// Hit is one search hit.
type Hit = opensearchapi.CoreSearchHit

// Search runs body (any JSON: query, aggs, sort, size, ...) against the
// named indices, or all indices when none are named.
func (c *Client) Search(ctx context.Context, body any, indices ...string) (*SearchResponse, error) {
	return c.search(ctx, body, indices, nil)
}

func (c *Client) search(ctx context.Context, body any, indices []string, params *opensearchapi.SearchWithIndexParams) (*SearchResponse, error) {
	r, err := jsonBody(body)
	if err != nil {
		return nil, err
	}
	var out SearchResponse
	if len(indices) == 0 {
		var p *opensearchapi.SearchParams
		if params != nil {
			p = &opensearchapi.SearchParams{Scroll: params.Scroll}
		}
		resp, err := c.Raw.SearchWithBody(ctx, p, contentTypeJSON, r)
		return &out, decode(resp, err, &out)
	}
	resp, err := c.Raw.SearchWithIndexWithBody(ctx, Indices(indices...), params, contentTypeJSON, r)
	return &out, decode(resp, err, &out)
}

// Count returns how many documents in the named indices match body, which
// may be nil (all documents) or {"query": ...}.
func (c *Client) Count(ctx context.Context, body any, indices ...string) (int64, error) {
	r, err := jsonBody(body)
	if err != nil {
		return 0, err
	}
	var out opensearchapi.Count200
	resp, err := c.Raw.CountWithIndexWithBody(ctx, Indices(indices...), nil, contentTypeJSON, r)
	return out.Count, decode(resp, err, &out)
}

// MSearchItem is one search of a multi-search. Header holds the per-search
// metadata ({"index": ...}); Body is the search body. Both are any JSON.
type MSearchItem struct {
	Header any
	Body   any
}

// MSearchResult is the outcome of one search of a multi-search: exactly one
// of Response and Err is set.
type MSearchResult struct {
	Response *SearchResponse
	Err      *Error
}

// MSearch runs several searches in one request. Results are in item order.
func (c *Client) MSearch(ctx context.Context, items ...MSearchItem) ([]MSearchResult, error) {
	var buf bytes.Buffer
	for i, item := range items {
		for _, part := range []any{item.Header, item.Body} {
			if part == nil {
				part = json.RawMessage("{}")
			}
			raw, err := rawJSON(part)
			if err != nil {
				return nil, fmt.Errorf("opensearch: msearch item %d: %w", i, err)
			}
			if err := compactLine(&buf, raw); err != nil {
				return nil, fmt.Errorf("opensearch: msearch item %d: %w", i, err)
			}
		}
	}
	var out struct {
		Responses []json.RawMessage `json:"responses"`
	}
	resp, err := c.Raw.MsearchWithBody(ctx, nil, contentTypeNDJSON, &buf)
	if err := decode(resp, err, &out); err != nil {
		return nil, err
	}
	results := make([]MSearchResult, len(out.Responses))
	for i, raw := range out.Responses {
		var probe struct {
			Error  json.RawMessage `json:"error"`
			Status int             `json:"status"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return nil, err
		}
		if len(probe.Error) > 0 {
			results[i].Err = newError(probe.Status, raw)
			continue
		}
		var r SearchResponse
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("opensearch: msearch response %d: %w", i, err)
		}
		results[i].Response = &r
	}
	return results, nil
}

// compactLine writes raw as one NDJSON line.
func compactLine(buf *bytes.Buffer, raw json.RawMessage) error {
	if err := json.Compact(buf, raw); err != nil {
		return err
	}
	buf.WriteByte('\n')
	return nil
}

// AsyncSearch is the state of an asynchronous search.
type AsyncSearch = opensearchapi.AsynchronousSearchCommonResponseBody

// AsyncSearchOptions are the optional parameters of SubmitAsyncSearch.
type AsyncSearchOptions struct {
	// WaitForCompletion is how long the submit call waits for results
	// before returning the search id (default 1s on the server).
	WaitForCompletion time.Duration
	// KeepAlive is how long results are kept (server default 12h).
	KeepAlive time.Duration
	// KeepOnCompletion keeps results that finished within
	// WaitForCompletion.
	KeepOnCompletion bool
}

// SubmitAsyncSearch starts an asynchronous search (the
// asynchronous-search plugin) over the named indices.
func (c *Client) SubmitAsyncSearch(ctx context.Context, body any, opts *AsyncSearchOptions, indices ...string) (*AsyncSearch, error) {
	r, err := jsonBody(body)
	if err != nil {
		return nil, err
	}
	params := &opensearchapi.AsynchronousSearchSearchParams{}
	if len(indices) > 0 {
		params.Index = ptr(strings.Join(indices, ","))
	}
	if opts != nil {
		if opts.WaitForCompletion > 0 {
			params.WaitForCompletionTimeout = ptr(duration(opts.WaitForCompletion))
		}
		if opts.KeepAlive > 0 {
			params.KeepAlive = ptr(duration(opts.KeepAlive))
		}
		params.KeepOnCompletion = ptr(opts.KeepOnCompletion)
	}
	var out AsyncSearch
	resp, err := c.Raw.AsynchronousSearchSearchWithBody(ctx, params, contentTypeJSON, r)
	return &out, decode(resp, err, &out)
}

// GetAsyncSearch returns the state, and results so far, of an asynchronous
// search.
func (c *Client) GetAsyncSearch(ctx context.Context, id string) (*AsyncSearch, error) {
	var out AsyncSearch
	resp, err := c.Raw.AsynchronousSearchGet(ctx, id, nil)
	return &out, decode(resp, err, &out)
}

// DeleteAsyncSearch cancels an asynchronous search and drops its results.
func (c *Client) DeleteAsyncSearch(ctx context.Context, id string) error {
	resp, err := c.Raw.AsynchronousSearchDelete(ctx, id, nil)
	return decode(resp, err, nil)
}

// duration formats d in the largest whole OpenSearch unit.
func duration(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// PageOptions configure Iterate and Scroll.
type PageOptions struct {
	// Query is the query clause (any JSON); nil matches all documents.
	Query any
	// Sort is the sort clause (any JSON). Iterate needs a total order and
	// defaults to [{"_shard_doc": "asc"}], which the point in time adds as
	// a tiebreaker; Scroll defaults to ["_doc"].
	Sort any
	// Source is the _source clause (any JSON: false, fields, includes).
	Source any
	// Size is the page size; default 1000.
	Size int
	// KeepAlive is how long the point in time or scroll context lives
	// between pages; default 1m.
	KeepAlive time.Duration
}

func (o *PageOptions) defaults() PageOptions {
	out := PageOptions{}
	if o != nil {
		out = *o
	}
	if out.Size <= 0 {
		out.Size = 1000
	}
	if out.KeepAlive <= 0 {
		out.KeepAlive = time.Minute
	}
	return out
}

// Iterate yields every hit matching opts.Query in the named indices, in
// opts.Sort order, using a point in time and search_after: the iteration
// sees one consistent snapshot and has no depth limit. The point in time is
// deleted when the loop ends. On error the iterator yields it once and
// stops.
func (c *Client) Iterate(ctx context.Context, opts *PageOptions, indices ...string) iter.Seq2[Hit, error] {
	o := opts.defaults()
	return func(yield func(Hit, error) bool) {
		var pit opensearchapi.CreatePit200
		resp, err := c.Raw.CreatePIT(ctx, indices, &opensearchapi.CreatePITParams{KeepAlive: ptr(duration(o.KeepAlive))})
		if err := decode(resp, err, &pit); err != nil {
			yield(Hit{}, err)
			return
		}
		if pit.PitID == nil {
			yield(Hit{}, fmt.Errorf("opensearch: create point in time returned no pit_id"))
			return
		}
		pitID := *pit.PitID
		defer func() {
			// Best effort, detached from a possibly cancelled ctx.
			body, _ := json.Marshal(map[string]any{"pit_id": []string{pitID}})
			resp, err := c.Raw.DeletePITWithBody(context.WithoutCancel(ctx), nil, contentTypeJSON, bytes.NewReader(body))
			_ = decode(resp, err, nil)
		}()

		sort := o.Sort
		if sort == nil {
			sort = []any{map[string]string{"_shard_doc": "asc"}}
		}
		var searchAfter []json.RawMessage
		for {
			body := map[string]any{
				"size":             o.Size,
				"sort":             sort,
				"track_total_hits": false,
				"pit":              map[string]any{"id": pitID, "keep_alive": duration(o.KeepAlive)},
			}
			if o.Query != nil {
				body["query"] = o.Query
			}
			if o.Source != nil {
				body["_source"] = o.Source
			}
			if searchAfter != nil {
				body["search_after"] = searchAfter
			}
			page, err := c.search(ctx, body, nil, nil)
			if err != nil {
				yield(Hit{}, err)
				return
			}
			if page.PitID != nil && *page.PitID != "" {
				pitID = *page.PitID
			}
			hits := page.Hits.Hits
			for _, h := range hits {
				if !yield(h, nil) {
					return
				}
			}
			if len(hits) < o.Size {
				return
			}
			last := hits[len(hits)-1]
			if last.Sort == nil {
				yield(Hit{}, fmt.Errorf("opensearch: hit without sort values; set PageOptions.Sort"))
				return
			}
			raw, err := json.Marshal(last.Sort)
			if err != nil {
				yield(Hit{}, err)
				return
			}
			if err := json.Unmarshal(raw, &searchAfter); err != nil {
				yield(Hit{}, err)
				return
			}
		}
	}
}

// Scroll yields every hit matching opts.Query in the named indices using
// the scroll API. Prefer Iterate; Scroll is for clusters or plugins without
// point in time support. The scroll context is cleared when the loop ends.
func (c *Client) Scroll(ctx context.Context, opts *PageOptions, indices ...string) iter.Seq2[Hit, error] {
	o := opts.defaults()
	return func(yield func(Hit, error) bool) {
		sort := o.Sort
		if sort == nil {
			sort = []string{"_doc"}
		}
		body := map[string]any{"size": o.Size, "sort": sort}
		if o.Query != nil {
			body["query"] = o.Query
		}
		if o.Source != nil {
			body["_source"] = o.Source
		}
		keepAlive := duration(o.KeepAlive)
		page, err := c.search(ctx, body, indices, &opensearchapi.SearchWithIndexParams{Scroll: &keepAlive})
		if err != nil {
			yield(Hit{}, err)
			return
		}
		var scrollID string
		defer func() {
			if scrollID == "" {
				return
			}
			body, _ := json.Marshal(map[string]any{"scroll_id": []string{scrollID}})
			resp, err := c.Raw.ClearScrollWithBody(context.WithoutCancel(ctx), nil, contentTypeJSON, bytes.NewReader(body))
			_ = decode(resp, err, nil)
		}()
		for {
			if page.ScrollID != nil {
				scrollID = *page.ScrollID
			}
			for _, h := range page.Hits.Hits {
				if !yield(h, nil) {
					return
				}
			}
			if len(page.Hits.Hits) == 0 || scrollID == "" {
				return
			}
			next, _ := json.Marshal(map[string]any{"scroll": keepAlive, "scroll_id": scrollID})
			var out SearchResponse
			resp, err := c.Raw.ScrollWithBody(ctx, nil, contentTypeJSON, bytes.NewReader(next))
			if err := decode(resp, err, &out); err != nil {
				yield(Hit{}, err)
				return
			}
			page = &out
		}
	}
}
