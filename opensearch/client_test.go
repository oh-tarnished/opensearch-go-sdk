package opensearch_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-tarnished/opensearch-go-client/opensearch"
)

func newTestClient(t *testing.T, h http.HandlerFunc, cfg opensearch.Config) *opensearch.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg.URL = srv.URL
	if cfg.RetryBackoff == nil {
		cfg.RetryBackoff = func(int) time.Duration { return time.Millisecond }
	}
	c, err := opensearch.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestNewRequiresURL(t *testing.T) {
	if _, err := opensearch.New(opensearch.Config{}); err == nil {
		t.Fatal("New without URL succeeded")
	}
}

func TestAuthAndHeaders(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  opensearch.Config
		want string
	}{
		"basic":  {opensearch.Config{Username: "u", Password: "p"}, "Basic dTpw"},
		"bearer": {opensearch.Config{Username: "u", Password: "p", BearerToken: "tok"}, "Bearer tok"},
		"none":   {opensearch.Config{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			tc.cfg.Header = http.Header{"X-Opaque-Id": {"test"}}
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != tc.want {
					t.Errorf("Authorization = %q, want %q", got, tc.want)
				}
				if got := r.Header.Get("X-Opaque-Id"); got != "test" {
					t.Errorf("X-Opaque-Id = %q", got)
				}
				writeJSON(w, 200, map[string]any{"version": map[string]any{"number": "3.9.0"}})
			}, tc.cfg)
			if err := c.Ping(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRetriesReplayBody(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"query":{"match_all":{}}}` {
			t.Errorf("attempt %d body = %q", calls.Load()+1, body)
		}
		switch calls.Add(1) {
		case 1:
			writeJSON(w, 429, map[string]any{"error": map[string]any{"type": "es_rejected_execution_exception", "reason": "busy"}, "status": 429})
		case 2:
			w.WriteHeader(503)
		default:
			writeJSON(w, 200, map[string]any{"count": 7, "_shards": map[string]any{}})
		}
	}, opensearch.Config{})
	n, err := c.Count(context.Background(), json.RawMessage(`{"query":{"match_all":{}}}`), "idx")
	if err != nil || n != 7 {
		t.Fatalf("Count = %d, %v", n, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

func TestRetriesExhausted(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, 429, map[string]any{"error": map[string]any{"type": "too_many", "reason": "slow down"}, "status": 429})
	}, opensearch.Config{MaxRetries: 2})
	_, err := c.Count(context.Background(), nil, "idx")
	if !opensearch.IsTooManyRequests(err) {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, 400, map[string]any{"error": "plain string error", "status": 400})
	}, opensearch.Config{})
	_, err := c.Search(context.Background(), nil, "idx")
	var oe *opensearch.Error
	if !errors.As(err, &oe) || oe.StatusCode != 400 || oe.Reason != "plain string error" {
		t.Fatalf("err = %#v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestErrorParsing(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]any{
			"error": map[string]any{
				"type": "index_not_found_exception", "reason": "no such index [x]",
				"root_cause": []any{map[string]any{"type": "index_not_found_exception", "reason": "no such index [x]", "index": "x"}},
			},
			"status": 404,
		})
	}, opensearch.Config{})
	_, err := c.Search(context.Background(), nil, "x")
	var oe *opensearch.Error
	if !errors.As(err, &oe) {
		t.Fatalf("err = %T %v", err, err)
	}
	if oe.Type != "index_not_found_exception" || oe.Reason != "no such index [x]" || len(oe.RootCause) != 1 || oe.RootCause[0].Index != "x" {
		t.Fatalf("parsed = %+v", oe)
	}
	if !opensearch.IsNotFound(err) || opensearch.IsConflict(err) {
		t.Fatal("status helpers disagree")
	}
	if got := err.Error(); got != "opensearch: 404 index_not_found_exception: no such index [x]" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestPathAndQueryParameters(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/a,b/_delete_by_query" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		for k, want := range map[string]string{"slices": "auto", "refresh": "true", "conflicts": "proceed", "wait_for_completion": "false"} {
			if q.Get(k) != want {
				t.Errorf("%s = %q, want %q", k, q.Get(k), want)
			}
		}
		writeJSON(w, 200, map[string]any{"task": "node:1"})
	}, opensearch.Config{})
	id, err := c.StartDeleteByQuery(context.Background(), map[string]any{"query": map[string]any{"match_all": map[string]any{}}},
		&opensearch.ByQueryOptions{Slices: -1, Refresh: true, ProceedOnConflict: true}, "a", "b")
	if err != nil || id != "node:1" {
		t.Fatalf("StartDeleteByQuery = %q, %v", id, err)
	}
}

func TestWaitForTask(t *testing.T) {
	var polls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_tasks/node:1" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if polls.Add(1) < 3 {
			writeJSON(w, 200, map[string]any{"completed": false, "task": map[string]any{}})
			return
		}
		writeJSON(w, 200, map[string]any{"completed": true, "task": map[string]any{}, "response": map[string]any{"deleted": 42, "failures": []any{}}})
	}, opensearch.Config{})
	res, err := c.WaitForTask(context.Background(), "node:1", time.Millisecond)
	if err != nil || res.Response.Deleted != 42 || polls.Load() != 3 {
		t.Fatalf("WaitForTask = %+v, %v (polls %d)", res, err, polls.Load())
	}
}

func readNDJSON(t *testing.T, r io.Reader) []map[string]json.RawMessage {
	t.Helper()
	var lines []map[string]json.RawMessage
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Errorf("line %q: %v", sc.Text(), err)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestBulk(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_bulk" || r.Header.Get("Content-Type") != "application/x-ndjson" || r.URL.Query().Get("refresh") != "wait_for" {
			t.Errorf("request %s %s %s", r.URL, r.Header.Get("Content-Type"), r.URL.RawQuery)
		}
		lines := readNDJSON(t, r.Body)
		if len(lines) != 5 { // index + doc, update + body, delete
			t.Errorf("lines = %d", len(lines))
		}
		writeJSON(w, 200, map[string]any{"took": 3, "errors": true, "items": []any{
			map[string]any{"index": map[string]any{"_index": "i", "_id": "1", "status": 201, "result": "created"}},
			map[string]any{"update": map[string]any{"_index": "i", "_id": "2", "status": 404, "error": map[string]any{"type": "document_missing_exception", "reason": "missing"}}},
			map[string]any{"delete": map[string]any{"_index": "i", "_id": "3", "status": 200, "result": "deleted"}},
		}})
	}, opensearch.Config{})
	var b opensearch.BulkRequest
	if err := b.Index("i", "1", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := b.Update("i", "2", `{"b": 2}`); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete("i", "3"); err != nil {
		t.Fatal(err)
	}
	if err := b.Index("i", "x", `{not json`); err == nil {
		t.Fatal("invalid JSON document accepted")
	}
	if err := b.Add(opensearch.BulkOperation{Action: "upsert"}); err == nil {
		t.Fatal("unknown action accepted")
	}
	if b.Len() != 3 {
		t.Fatalf("Len = %d", b.Len())
	}
	res, err := c.Bulk(context.Background(), &b, &opensearch.BulkOptions{Refresh: opensearch.RefreshWaitFor})
	if err != nil {
		t.Fatal(err)
	}
	failed := res.Failed()
	if len(res.Items) != 3 || len(failed) != 1 || failed[0].ID != "2" || failed[0].Action != "update" || failed[0].Error.Type != "document_missing_exception" {
		t.Fatalf("result = %+v", res)
	}
}

func TestBulkIndexer(t *testing.T) {
	const total = 2500
	var (
		mu       sync.Mutex
		received = map[string]int{}
		rejected atomic.Int32
	)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events/_bulk" {
			t.Errorf("path = %s", r.URL.Path)
		}
		lines := readNDJSON(t, r.Body)
		var items []any
		for i := 0; i < len(lines); i += 2 {
			var meta struct {
				ID string `json:"_id"`
			}
			_ = json.Unmarshal(lines[i]["index"], &meta)
			mu.Lock()
			received[meta.ID]++
			n := received[meta.ID]
			mu.Unlock()
			switch {
			case meta.ID == "13":
				items = append(items, map[string]any{"index": map[string]any{"_id": meta.ID, "status": 400, "error": map[string]any{"type": "mapper_parsing_exception", "reason": "bad"}}})
			case n == 1 && meta.ID[len(meta.ID)-1] == '7':
				// First attempt of ids ending in 7 is rejected.
				rejected.Add(1)
				items = append(items, map[string]any{"index": map[string]any{"_id": meta.ID, "status": 429, "error": map[string]any{"type": "es_rejected_execution_exception", "reason": "queue full"}}})
			default:
				items = append(items, map[string]any{"index": map[string]any{"_id": meta.ID, "status": 201, "result": "created"}})
			}
		}
		writeJSON(w, 200, map[string]any{"took": 1, "errors": true, "items": items})
	}, opensearch.Config{})

	var errorsSeen atomic.Int32
	var successes atomic.Int32
	bi := opensearch.NewBulkIndexer(c, opensearch.BulkIndexerConfig{
		Index: "events", Workers: 3, FlushDocs: 100, FlushInterval: -1,
		RetryBackoff: func(int) time.Duration { return time.Millisecond },
		OnError: func(_ context.Context, item opensearch.BulkIndexerItem, res opensearch.BulkItem, err error) {
			errorsSeen.Add(1)
			if item.ID != "13" || res.Status != 400 {
				t.Errorf("unexpected failure %s: %v", item.ID, err)
			}
		},
	})
	ctx := context.Background()
	for i := range total {
		err := bi.Add(ctx, opensearch.BulkIndexerItem{
			BulkOperation: opensearch.BulkOperation{Action: opensearch.ActionIndex, ID: fmt.Sprint(i), Body: map[string]int{"n": i}},
			OnSuccess:     func(context.Context, opensearch.BulkIndexerItem, opensearch.BulkItem) { successes.Add(1) },
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == total/2 {
			if err := bi.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			if st := bi.Stats(); st.Succeeded+st.Failed != uint64(i+1) {
				t.Fatalf("after Flush: %+v", st)
			}
		}
	}
	if err := bi.Close(ctx); err != nil {
		t.Fatal(err)
	}
	st := bi.Stats()
	if st.Added != total || st.Succeeded != total-1 || st.Failed != 1 || st.Retried != uint64(rejected.Load()) || st.Requests == 0 {
		t.Fatalf("stats = %+v (rejected %d)", st, rejected.Load())
	}
	if successes.Load() != total-1 || errorsSeen.Load() != 1 {
		t.Fatalf("callbacks: %d successes, %d errors", successes.Load(), errorsSeen.Load())
	}
	if err := bi.Add(ctx, opensearch.BulkIndexerItem{BulkOperation: opensearch.BulkOperation{Action: opensearch.ActionDelete, ID: "1"}}); !errors.Is(err, opensearch.ErrBulkIndexerClosed) {
		t.Fatalf("Add after Close = %v", err)
	}
}

func TestBulkIndexerRequestFailure(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 413, map[string]any{"error": map[string]any{"type": "too_large", "reason": "body too large"}, "status": 413})
	}, opensearch.Config{})
	var failed atomic.Int32
	bi := opensearch.NewBulkIndexer(c, opensearch.BulkIndexerConfig{
		Index: "x", OnError: func(_ context.Context, _ opensearch.BulkIndexerItem, _ opensearch.BulkItem, err error) {
			if !strings.Contains(err.Error(), "too_large") {
				t.Errorf("err = %v", err)
			}
			failed.Add(1)
		},
	})
	for i := range 10 {
		_ = bi.Add(context.Background(), opensearch.BulkIndexerItem{BulkOperation: opensearch.BulkOperation{Action: opensearch.ActionIndex, Body: map[string]int{"i": i}}})
	}
	_ = bi.Close(context.Background())
	if failed.Load() != 10 || bi.Stats().Failed != 10 {
		t.Fatalf("failed = %d, stats %+v", failed.Load(), bi.Stats())
	}
}

func TestIteratePIT(t *testing.T) {
	const docs = 25
	var deleted atomic.Bool
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/idx/_search/point_in_time":
			if r.URL.Query().Get("keep_alive") != "30s" {
				t.Errorf("keep_alive = %q", r.URL.Query().Get("keep_alive"))
			}
			writeJSON(w, 200, map[string]any{"pit_id": "pit-1"})
		case r.URL.Path == "/_search":
			var body struct {
				Size        int                 `json:"size"`
				SearchAfter []int               `json:"search_after"`
				Pit         struct{ ID string } `json:"pit"`
				Query       json.RawMessage     `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Pit.ID != "pit-1" || string(body.Query) != `{"term":{"k":"v"}}` {
				t.Errorf("search body = %+v", body)
			}
			from := 0
			if len(body.SearchAfter) == 1 {
				from = body.SearchAfter[0] + 1
			}
			var hits []any
			for i := from; i < min(from+body.Size, docs); i++ {
				hits = append(hits, map[string]any{"_id": fmt.Sprint(i), "_source": map[string]int{"n": i}, "sort": []int{i}})
			}
			writeJSON(w, 200, map[string]any{"pit_id": "pit-1", "took": 1, "_shards": map[string]any{}, "hits": map[string]any{"hits": hits}})
		case r.Method == http.MethodDelete && r.URL.Path == "/_search/point_in_time":
			deleted.Store(true)
			writeJSON(w, 200, map[string]any{"pits": []any{}})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	}, opensearch.Config{})
	var got []int
	for hit, err := range c.Iterate(context.Background(), &opensearch.PageOptions{Size: 10, KeepAlive: 30 * time.Second, Query: json.RawMessage(`{"term":{"k":"v"}}`)}, "idx") {
		if err != nil {
			t.Fatal(err)
		}
		doc, err := opensearch.Source[struct{ N int }](hit)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, doc.N)
	}
	if len(got) != docs || got[docs-1] != docs-1 {
		t.Fatalf("got %v", got)
	}
	if !deleted.Load() {
		t.Fatal("point in time not deleted")
	}
}

func TestMSearch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		lines := readNDJSON(t, r.Body)
		if len(lines) != 4 {
			t.Errorf("lines = %d", len(lines))
		}
		writeJSON(w, 200, map[string]any{"took": 1, "responses": []any{
			map[string]any{"took": 1, "_shards": map[string]any{}, "hits": map[string]any{"total": map[string]any{"value": 3, "relation": "eq"}, "hits": []any{}}, "status": 200},
			map[string]any{"error": map[string]any{"type": "index_not_found_exception", "reason": "no such index"}, "status": 404},
		}})
	}, opensearch.Config{})
	res, err := c.MSearch(context.Background(),
		opensearch.MSearchItem{Header: map[string]string{"index": "a"}, Body: `{"query": {"match_all": {}}}`},
		opensearch.MSearchItem{Header: map[string]string{"index": "b"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if opensearch.TotalHits(res[0].Response) != 3 || res[1].Err == nil || res[1].Err.Type != "index_not_found_exception" || res[1].Err.StatusCode != 404 {
		t.Fatalf("res = %+v", res)
	}
}

func TestDecodeHelpers(t *testing.T) {
	type doc struct{ A int }
	v, err := opensearch.Decode[doc](json.RawMessage(`{"A": 3}`))
	if err != nil || v.A != 3 {
		t.Fatalf("Decode = %+v, %v", v, err)
	}
	var resp opensearch.SearchResponse
	if err := json.Unmarshal([]byte(`{"took":1,"_shards":{},"hits":{"total":5,"hits":[]},"aggregations":{"x":{"value":2}}}`), &resp); err != nil {
		t.Fatal(err)
	}
	if opensearch.TotalHits(&resp) != 5 {
		t.Fatalf("TotalHits = %d", opensearch.TotalHits(&resp))
	}
	agg, ok, err := opensearch.Aggregation[struct{ Value int }](&resp, "x")
	if err != nil || !ok || agg.Value != 2 {
		t.Fatalf("Aggregation = %+v, %v, %v", agg, ok, err)
	}
	if _, ok, _ := opensearch.Aggregation[any](&resp, "missing"); ok {
		t.Fatal("missing aggregation reported")
	}
}
