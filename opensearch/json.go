package opensearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/oh-tarnished/opensearch-go-client/opensearchapi"
)

// Documents, queries and aggregations are any JSON. A body argument typed
// any accepts:
//
//   - nil, for no body;
//   - json.RawMessage, []byte or string, sent as is;
//   - io.Reader, streamed as is;
//   - any other value, encoded with encoding/json (structs, maps, ...).
//
// Query DSL therefore passes through untouched: build it as a map, a struct
// of your own, or a literal JSON string.

const contentTypeJSON = "application/json"

// jsonBody turns a body argument into a reader.
func jsonBody(v any) (io.Reader, error) {
	switch b := v.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return bytes.NewReader(b), nil
	case []byte:
		return bytes.NewReader(b), nil
	case string:
		return strings.NewReader(b), nil
	case io.Reader:
		return b, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("opensearch: encode body: %w", err)
	}
	return bytes.NewReader(data), nil
}

// rawJSON encodes a body argument as a single JSON value (no reader).
func rawJSON(v any) (json.RawMessage, error) {
	switch b := v.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return b, nil
	case []byte:
		return b, nil
	case string:
		return json.RawMessage(b), nil
	case io.Reader:
		data, err := io.ReadAll(b)
		return data, err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("opensearch: encode body: %w", err)
	}
	return data, nil
}

// Decode unmarshals raw JSON into a T, for documents and aggregation
// results.
func Decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}
	err := json.Unmarshal(raw, &v)
	return v, err
}

// Source decodes a hit's _source into a T.
func Source[T any](hit opensearchapi.CoreSearchHit) (T, error) {
	if hit.Source == nil {
		var zero T
		return zero, nil
	}
	return Decode[T](*hit.Source)
}

// Sources decodes the _source of every hit into a T.
func Sources[T any](hits []opensearchapi.CoreSearchHit) ([]T, error) {
	out := make([]T, 0, len(hits))
	for i, h := range hits {
		v, err := Source[T](h)
		if err != nil {
			return nil, fmt.Errorf("opensearch: hit %d: %w", i, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// Aggregation decodes the aggregation called name from a search response
// into a T (such as a struct with Buckets, or map[string]any). ok is false
// when the response has no such aggregation.
func Aggregation[T any](resp *SearchResponse, name string) (v T, ok bool, err error) {
	if resp == nil || resp.Aggregations == nil {
		return v, false, nil
	}
	raw, ok := (*resp.Aggregations)[name]
	if !ok {
		return v, false, nil
	}
	v, err = Decode[T](raw)
	return v, true, err
}

// TotalHits returns hits.total.value from a search response, or -1 when the
// response carries no total (track_total_hits: false).
func TotalHits(resp *SearchResponse) int64 {
	if resp == nil || resp.Hits.Total == nil {
		return -1
	}
	raw, err := resp.Hits.Total.MarshalJSON()
	if err != nil {
		return -1
	}
	var total struct {
		Value int64 `json:"value"`
	}
	if json.Unmarshal(raw, &total) == nil && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return total.Value
	}
	if n, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64); err == nil {
		return n
	}
	return -1
}

// union fills one of the generated union types (types with an unexported
// union field) from a JSON literal.
func union[T any, P interface {
	*T
	json.Unmarshaler
}](literal string) T {
	var v T
	// The generated unions accept any JSON; the error is always nil.
	_ = P(&v).UnmarshalJSON([]byte(literal))
	return v
}

// Indices names one or more indices, aliases or patterns for a path
// parameter.
func Indices(names ...string) opensearchapi.CommonIndices {
	if len(names) == 1 {
		return union[opensearchapi.CommonIndices](strconv.Quote(names[0]))
	}
	data, _ := json.Marshal(names)
	return union[opensearchapi.CommonIndices](string(data))
}

// Refresh values for write operations.
const (
	RefreshTrue    = "true"
	RefreshFalse   = "false"
	RefreshWaitFor = "wait_for"
)

// Refresh builds a refresh query parameter: RefreshTrue, RefreshFalse or
// RefreshWaitFor. An empty value returns nil (the server default).
func Refresh(value string) *opensearchapi.CommonRefresh {
	if value == "" {
		return nil
	}
	r := union[opensearchapi.CommonRefresh](strconv.Quote(value))
	return &r
}

// Slices builds a slices query parameter; n <= 0 means "auto".
func Slices(n int) *opensearchapi.CommonSlices {
	var s opensearchapi.CommonSlices
	if n <= 0 {
		s = union[opensearchapi.CommonSlices](`"auto"`)
	} else {
		s = union[opensearchapi.CommonSlices](strconv.Itoa(n))
	}
	return &s
}

func ptr[T any](v T) *T { return &v }
