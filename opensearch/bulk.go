package opensearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/oh-tarnished/opensearch-go-sdk/opensearchapi"
)

const contentTypeNDJSON = "application/x-ndjson"

// Bulk actions.
const (
	ActionIndex  = "index"
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

// BulkOperation is one action of a bulk request.
type BulkOperation struct {
	// Action is ActionIndex, ActionCreate, ActionUpdate or ActionDelete.
	Action string
	// Index may be empty when the request names a default index.
	Index string
	// ID may be empty for ActionIndex (OpenSearch assigns one).
	ID      string
	Routing string
	// RetryOnConflict applies to ActionUpdate.
	RetryOnConflict int
	// Body is the document for index and create, the update body
	// ({"doc": ...}, {"script": ...}) for update, and unused for delete.
	// Any JSON (see the package documentation).
	Body any
}

// BulkRequest builds an NDJSON bulk body. The zero value is ready to use.
type BulkRequest struct {
	buf bytes.Buffer
	n   int
}

// Add appends op.
func (b *BulkRequest) Add(op BulkOperation) error {
	line, err := encodeOperation(op)
	if err != nil {
		return err
	}
	b.buf.Write(line)
	b.n++
	return nil
}

// Index appends an index action.
func (b *BulkRequest) Index(index, id string, doc any) error {
	return b.Add(BulkOperation{Action: ActionIndex, Index: index, ID: id, Body: doc})
}

// Create appends a create action, which fails if the id exists.
func (b *BulkRequest) Create(index, id string, doc any) error {
	return b.Add(BulkOperation{Action: ActionCreate, Index: index, ID: id, Body: doc})
}

// Update appends a partial-document update ({"doc": partial}).
func (b *BulkRequest) Update(index, id string, partial any) error {
	return b.Add(BulkOperation{Action: ActionUpdate, Index: index, ID: id, Body: map[string]any{"doc": partial}})
}

// Delete appends a delete action.
func (b *BulkRequest) Delete(index, id string) error {
	return b.Add(BulkOperation{Action: ActionDelete, Index: index, ID: id})
}

// Len is the number of operations added.
func (b *BulkRequest) Len() int { return b.n }

// Size is the body size in bytes.
func (b *BulkRequest) Size() int { return b.buf.Len() }

// Reset empties the request for reuse.
func (b *BulkRequest) Reset() { b.buf.Reset(); b.n = 0 }

// encodeOperation renders op as its NDJSON lines.
func encodeOperation(op BulkOperation) ([]byte, error) {
	meta := map[string]any{}
	if op.Index != "" {
		meta["_index"] = op.Index
	}
	if op.ID != "" {
		meta["_id"] = op.ID
	}
	if op.Routing != "" {
		meta["routing"] = op.Routing
	}
	if op.RetryOnConflict > 0 {
		meta["retry_on_conflict"] = op.RetryOnConflict
	}
	var buf bytes.Buffer
	switch op.Action {
	case ActionIndex, ActionCreate, ActionUpdate, ActionDelete:
	default:
		return nil, fmt.Errorf("opensearch: unknown bulk action %q", op.Action)
	}
	header, err := json.Marshal(map[string]any{op.Action: meta})
	if err != nil {
		return nil, err
	}
	buf.Write(header)
	buf.WriteByte('\n')
	if op.Action == ActionDelete {
		return buf.Bytes(), nil
	}
	if op.Body == nil {
		return nil, fmt.Errorf("opensearch: bulk %s %s/%s has no body", op.Action, op.Index, op.ID)
	}
	raw, err := rawJSON(op.Body)
	if err != nil {
		return nil, err
	}
	if err := compactLine(&buf, raw); err != nil {
		return nil, fmt.Errorf("opensearch: bulk %s %s/%s: %w", op.Action, op.Index, op.ID, err)
	}
	return buf.Bytes(), nil
}

// BulkOptions are the optional parameters of Bulk.
type BulkOptions struct {
	// Index is the default index for operations that name none.
	Index string
	// Refresh is RefreshTrue, RefreshFalse, RefreshWaitFor or empty.
	Refresh string
}

// BulkResult is a bulk response.
type BulkResult struct {
	Took   int64
	Errors bool
	// Items holds one entry per operation, in request order.
	Items []BulkItem
}

// BulkItem is the outcome of one bulk operation.
type BulkItem struct {
	Action  string
	Index   string
	ID      string
	Status  int
	Result  string
	Version int64
	SeqNo   int64
	// Error is set when the operation failed.
	Error *ErrorCause
}

// Failed returns the items that failed.
func (r *BulkResult) Failed() []BulkItem {
	var out []BulkItem
	for _, it := range r.Items {
		if it.Error != nil || it.Status >= 300 {
			out = append(out, it)
		}
	}
	return out
}

// Bulk sends req. A nil error means the request succeeded; individual
// operations may still have failed (see BulkResult.Failed).
func (c *Client) Bulk(ctx context.Context, req *BulkRequest, opts *BulkOptions) (*BulkResult, error) {
	return c.bulk(ctx, req.buf.Bytes(), opts)
}

func (c *Client) bulk(ctx context.Context, body []byte, opts *BulkOptions) (*BulkResult, error) {
	var raw bulkResponse
	var refresh *opensearchapi.CommonRefresh
	if opts != nil {
		refresh = Refresh(opts.Refresh)
	}
	if opts != nil && opts.Index != "" {
		resp, err := c.Raw.BulkWithIndexWithBody(ctx, opts.Index, &opensearchapi.BulkWithIndexParams{Refresh: refresh}, contentTypeNDJSON, bytes.NewReader(body))
		if err := decode(resp, err, &raw); err != nil {
			return nil, err
		}
	} else {
		resp, err := c.Raw.BulkWithBody(ctx, &opensearchapi.BulkParams{Refresh: refresh}, contentTypeNDJSON, bytes.NewReader(body))
		if err := decode(resp, err, &raw); err != nil {
			return nil, err
		}
	}
	return raw.result(), nil
}

type bulkResponse struct {
	Took   int64                               `json:"took"`
	Errors bool                                `json:"errors"`
	Items  []map[string]bulkResponseItemDetail `json:"items"`
}

type bulkResponseItemDetail struct {
	Index   string      `json:"_index"`
	ID      string      `json:"_id"`
	Status  int         `json:"status"`
	Result  string      `json:"result"`
	Version int64       `json:"_version"`
	SeqNo   int64       `json:"_seq_no"`
	Error   *ErrorCause `json:"error"`
}

func (r *bulkResponse) result() *BulkResult {
	out := &BulkResult{Took: r.Took, Errors: r.Errors, Items: make([]BulkItem, 0, len(r.Items))}
	for _, entry := range r.Items {
		for action, d := range entry {
			out.Items = append(out.Items, BulkItem{
				Action: action, Index: d.Index, ID: d.ID, Status: d.Status,
				Result: d.Result, Version: d.Version, SeqNo: d.SeqNo, Error: d.Error,
			})
		}
	}
	return out
}
