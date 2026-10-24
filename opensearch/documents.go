package opensearch

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/oh-tarnished/opensearch-go-client/opensearchapi"
)

// WriteResult is the response to a single-document write.
type WriteResult = opensearchapi.CommonWriteResponseBase

// WriteOptions are the optional parameters of single-document writes.
type WriteOptions struct {
	// Refresh is RefreshTrue, RefreshFalse, RefreshWaitFor or empty.
	Refresh string
	// Routing routes the document to a shard.
	Routing string
}

// Index creates or replaces the document id in index. An empty id lets
// OpenSearch assign one.
func (c *Client) Index(ctx context.Context, index, id string, doc any, opts *WriteOptions) (*WriteResult, error) {
	r, err := jsonBody(doc)
	if err != nil {
		return nil, err
	}
	var out WriteResult
	if id == "" {
		params := &opensearchapi.IndexParams{}
		if opts != nil {
			params.Refresh = Refresh(opts.Refresh)
			params.Routing = routing(opts.Routing)
		}
		resp, err := c.Raw.IndexWithBody(ctx, index, params, contentTypeJSON, r)
		return &out, decode(resp, err, &out)
	}
	params := &opensearchapi.IndexWithIDParams{}
	if opts != nil {
		params.Refresh = Refresh(opts.Refresh)
		params.Routing = routing(opts.Routing)
	}
	resp, err := c.Raw.IndexWithIDWithBody(ctx, index, id, params, contentTypeJSON, r)
	return &out, decode(resp, err, &out)
}

// Get fetches a document's source as raw JSON. found is false when the
// document (or index) doesn't exist.
func (c *Client) Get(ctx context.Context, index, id string) (source json.RawMessage, found bool, err error) {
	var out opensearchapi.Get200
	resp, err := c.Raw.Get(ctx, index, id, nil)
	if err := decode(resp, err, &out); err != nil {
		if IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !out.Found || out.Source == nil {
		return nil, out.Found, nil
	}
	return *out.Source, true, nil
}

// GetDocument fetches a document and decodes its source into a T.
func GetDocument[T any](ctx context.Context, c *Client, index, id string) (doc T, found bool, err error) {
	raw, found, err := c.Get(ctx, index, id)
	if err != nil || !found {
		return doc, found, err
	}
	doc, err = Decode[T](raw)
	return doc, true, err
}

// Delete deletes a document. found is false when it didn't exist.
func (c *Client) Delete(ctx context.Context, index, id string, opts *WriteOptions) (found bool, err error) {
	params := &opensearchapi.DeleteParams{}
	if opts != nil {
		params.Refresh = Refresh(opts.Refresh)
		params.Routing = routing(opts.Routing)
	}
	resp, err := c.Raw.Delete(ctx, index, id, params)
	if err := decode(resp, err, nil); err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func routing(s string) *opensearchapi.CommonStringOrStringArray {
	if s == "" {
		return nil
	}
	r := union[opensearchapi.CommonStringOrStringArray](strconv.Quote(s))
	return &r
}
