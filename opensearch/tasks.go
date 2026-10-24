package opensearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/oh-tarnished/opensearch-go-client/opensearchapi"
)

// ByQueryOptions are the optional parameters of the by-query operations and
// Reindex.
type ByQueryOptions struct {
	// Slices splits the work into parallel slices: 0 leaves it to the
	// server default (one slice), a negative value means "auto".
	Slices int
	// Refresh refreshes the affected indices when done.
	Refresh bool
	// ProceedOnConflict counts version conflicts instead of aborting.
	ProceedOnConflict bool
	// RequestsPerSecond throttles the operation; 0 means unthrottled.
	RequestsPerSecond float32
	// ScrollSize is the batch size of the underlying scroll (by-query).
	ScrollSize int32
}

// ByQueryResult is the outcome of a delete_by_query, update_by_query or
// reindex, whether returned directly or by a finished task.
type ByQueryResult struct {
	Took             int64             `json:"took"`
	TimedOut         bool              `json:"timed_out"`
	Total            int64             `json:"total"`
	Created          int64             `json:"created"`
	Updated          int64             `json:"updated"`
	Deleted          int64             `json:"deleted"`
	Batches          int64             `json:"batches"`
	VersionConflicts int64             `json:"version_conflicts"`
	Noops            int64             `json:"noops"`
	Failures         []json.RawMessage `json:"failures"`
}

// TaskResult is the state of a task started with wait_for_completion=false.
type TaskResult struct {
	Completed bool `json:"completed"`
	// Task is the task description (status counters, running time, ...).
	Task json.RawMessage `json:"task"`
	// Response is set once a by-query or reindex task completed.
	Response *ByQueryResult `json:"response"`
	// Error is set when the task failed.
	Error *ErrorCause `json:"error"`
}

// DeleteByQuery deletes the documents of the named indices matching body
// ({"query": ...}) and waits for it to finish.
func (c *Client) DeleteByQuery(ctx context.Context, body any, opts *ByQueryOptions, indices ...string) (*ByQueryResult, error) {
	var out ByQueryResult
	return &out, c.deleteByQuery(ctx, body, opts, true, indices, &out)
}

// StartDeleteByQuery starts a delete_by_query as a task and returns its id;
// see WaitForTask.
func (c *Client) StartDeleteByQuery(ctx context.Context, body any, opts *ByQueryOptions, indices ...string) (string, error) {
	var out struct {
		Task string `json:"task"`
	}
	return out.Task, c.deleteByQuery(ctx, body, opts, false, indices, &out)
}

func (c *Client) deleteByQuery(ctx context.Context, body any, opts *ByQueryOptions, wait bool, indices []string, out any) error {
	r, err := jsonBody(body)
	if err != nil {
		return err
	}
	params := &opensearchapi.DeleteByQueryParams{WaitForCompletion: &wait}
	if opts != nil {
		params.Slices = byQuerySlices(opts.Slices)
		params.Refresh = refreshFlag(opts.Refresh)
		params.Conflicts = conflicts(opts.ProceedOnConflict)
		if opts.RequestsPerSecond > 0 {
			params.RequestsPerSecond = &opts.RequestsPerSecond
		}
		if opts.ScrollSize > 0 {
			params.ScrollSize = &opts.ScrollSize
		}
	}
	resp, err := c.Raw.DeleteByQueryWithBody(ctx, Indices(indices...), params, contentTypeJSON, r)
	return decode(resp, err, out)
}

// UpdateByQuery updates the documents of the named indices matching body
// ({"query": ..., "script": ...}) and waits for it to finish.
func (c *Client) UpdateByQuery(ctx context.Context, body any, opts *ByQueryOptions, indices ...string) (*ByQueryResult, error) {
	var out ByQueryResult
	return &out, c.updateByQuery(ctx, body, opts, true, indices, &out)
}

// StartUpdateByQuery starts an update_by_query as a task and returns its
// id; see WaitForTask.
func (c *Client) StartUpdateByQuery(ctx context.Context, body any, opts *ByQueryOptions, indices ...string) (string, error) {
	var out struct {
		Task string `json:"task"`
	}
	return out.Task, c.updateByQuery(ctx, body, opts, false, indices, &out)
}

func (c *Client) updateByQuery(ctx context.Context, body any, opts *ByQueryOptions, wait bool, indices []string, out any) error {
	r, err := jsonBody(body)
	if err != nil {
		return err
	}
	params := &opensearchapi.UpdateByQueryParams{WaitForCompletion: &wait}
	if opts != nil {
		params.Slices = byQuerySlices(opts.Slices)
		params.Refresh = refreshFlag(opts.Refresh)
		params.Conflicts = conflicts(opts.ProceedOnConflict)
		if opts.RequestsPerSecond > 0 {
			params.RequestsPerSecond = &opts.RequestsPerSecond
		}
		if opts.ScrollSize > 0 {
			params.ScrollSize = &opts.ScrollSize
		}
	}
	resp, err := c.Raw.UpdateByQueryWithBody(ctx, Indices(indices...), params, contentTypeJSON, r)
	return decode(resp, err, out)
}

// Reindex copies documents as described by body ({"source": ...,
// "dest": ...}) and waits for it to finish.
func (c *Client) Reindex(ctx context.Context, body any, opts *ByQueryOptions) (*ByQueryResult, error) {
	var out ByQueryResult
	return &out, c.reindex(ctx, body, opts, true, &out)
}

// StartReindex starts a reindex as a task and returns its id; see
// WaitForTask.
func (c *Client) StartReindex(ctx context.Context, body any, opts *ByQueryOptions) (string, error) {
	var out struct {
		Task string `json:"task"`
	}
	return out.Task, c.reindex(ctx, body, opts, false, &out)
}

func (c *Client) reindex(ctx context.Context, body any, opts *ByQueryOptions, wait bool, out any) error {
	r, err := jsonBody(body)
	if err != nil {
		return err
	}
	params := &opensearchapi.ReindexParams{WaitForCompletion: &wait}
	if opts != nil {
		params.Slices = byQuerySlices(opts.Slices)
		params.Refresh = refreshFlag(opts.Refresh)
		if opts.RequestsPerSecond > 0 {
			params.RequestsPerSecond = &opts.RequestsPerSecond
		}
	}
	resp, err := c.Raw.ReindexWithBody(ctx, params, contentTypeJSON, r)
	return decode(resp, err, out)
}

// GetTask returns the current state of a task.
func (c *Client) GetTask(ctx context.Context, taskID string) (*TaskResult, error) {
	var out TaskResult
	resp, err := c.Raw.TasksGet(ctx, taskID, nil)
	return &out, decode(resp, err, &out)
}

// WaitForTask polls a task every interval (default 1s) until it completes
// or ctx ends. A task that completed with an error, or with failures in its
// response, returns them as the error alongside the result.
func (c *Client) WaitForTask(ctx context.Context, taskID string, interval time.Duration) (*TaskResult, error) {
	if interval <= 0 {
		interval = time.Second
	}
	for {
		t, err := c.GetTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if t.Completed {
			switch {
			case t.Error != nil:
				return t, t.Error
			case t.Response != nil && len(t.Response.Failures) > 0:
				return t, fmt.Errorf("opensearch: task %s finished with %d failures, first: %s", taskID, len(t.Response.Failures), t.Response.Failures[0])
			}
			return t, nil
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func byQuerySlices(n int) *opensearchapi.CommonSlices {
	if n == 0 {
		return nil
	}
	return Slices(n)
}

func refreshFlag(on bool) *opensearchapi.CommonRefresh {
	if !on {
		return nil
	}
	return Refresh(RefreshTrue)
}

func conflicts(proceed bool) *opensearchapi.CommonConflicts {
	if !proceed {
		return nil
	}
	v := union[opensearchapi.CommonConflicts](strconv.Quote("proceed"))
	return &v
}

// AliasSwap describes a zero-downtime reindex behind an alias.
type AliasSwap struct {
	// Alias is the alias readers and writers use.
	Alias string
	// NewIndex is created with NewIndexBody (settings and mappings) and
	// filled from the alias' current indices.
	NewIndex     string
	NewIndexBody any
	// Query limits the documents copied (any JSON query clause); nil copies
	// everything.
	Query any
	// Script transforms documents while copying (any JSON script object).
	Script any
	// Slices parallelises the reindex; negative means "auto".
	Slices int
	// DeleteOld deletes the indices the alias pointed to after the swap.
	DeleteOld bool
	// PollInterval is the task polling interval; default 1s.
	PollInterval time.Duration
}

// AliasSwapResult reports a completed AliasSwap.
type AliasSwapResult struct {
	// OldIndices are the indices the alias pointed to before the swap.
	OldIndices []string
	Reindex    *ByQueryResult
}

// SwapAlias reindexes the documents behind s.Alias into s.NewIndex as a
// task, then atomically moves the alias to the new index (as its write
// index). When the alias doesn't exist yet it is created on the new index.
// Writes that reach the old index after the reindex started are not
// copied; pause writers or replay them for a strict cut-over.
func (c *Client) SwapAlias(ctx context.Context, s AliasSwap) (*AliasSwapResult, error) {
	if s.Alias == "" || s.NewIndex == "" {
		return nil, errors.New("opensearch: AliasSwap needs Alias and NewIndex")
	}
	old, err := c.AliasIndices(ctx, s.Alias)
	if err != nil {
		return nil, err
	}
	if err := c.CreateIndex(ctx, s.NewIndex, s.NewIndexBody); err != nil {
		return nil, err
	}
	res := &AliasSwapResult{OldIndices: old}
	if len(old) > 0 {
		source := map[string]any{"index": old}
		if s.Query != nil {
			source["query"] = s.Query
		}
		body := map[string]any{"source": source, "dest": map[string]any{"index": s.NewIndex}}
		if s.Script != nil {
			body["script"] = s.Script
		}
		taskID, err := c.StartReindex(ctx, body, &ByQueryOptions{Slices: s.Slices})
		if err != nil {
			return nil, err
		}
		task, err := c.WaitForTask(ctx, taskID, s.PollInterval)
		if err != nil {
			return nil, err
		}
		res.Reindex = task.Response
		if err := c.Refresh(ctx, s.NewIndex); err != nil {
			return nil, err
		}
	}
	actions := []AliasAction{{Add: &AliasSpec{Index: s.NewIndex, Alias: s.Alias, IsWriteIndex: ptr(true)}}}
	for _, index := range old {
		actions = append(actions, AliasAction{Remove: &AliasSpec{Index: index, Alias: s.Alias}})
	}
	if err := c.UpdateAliases(ctx, actions...); err != nil {
		return nil, err
	}
	if s.DeleteOld && len(old) > 0 {
		if err := c.DeleteIndex(ctx, old...); err != nil {
			return res, err
		}
	}
	return res, nil
}
