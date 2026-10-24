package opensearch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oh-tarnished/opensearch-go-sdk/opensearch"
)

// Integration tests run against the cluster at OPENSEARCH_URL (security
// disabled, or credentials in OPENSEARCH_USERNAME / OPENSEARCH_PASSWORD)
// and are skipped without it.
func integrationClient(t *testing.T) *opensearch.Client {
	t.Helper()
	url := os.Getenv("OPENSEARCH_URL")
	if url == "" {
		t.Skip("OPENSEARCH_URL not set")
	}
	c, err := opensearch.New(opensearch.Config{
		URL:      url,
		Username: os.Getenv("OPENSEARCH_USERNAME"),
		Password: os.Getenv("OPENSEARCH_PASSWORD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := c.WaitForStatus(ctx, "yellow"); err != nil {
		t.Fatal(err)
	}
	return c
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// interval is a date_range value.
type interval struct {
	GTE string `json:"gte"`
	LT  string `json:"lt"`
}

type resource struct {
	ResourceID string     `json:"resource_id"`
	Tenant     string     `json:"tenant"`
	Kind       string     `json:"kind"`
	Free       []interval `json:"free"`
}

// TestIntegrationAvailability is freebusy's availability search: resources
// carry arrays of free intervals in a date_range field, and a search finds
// those with a free interval that contains the requested window.
func TestIntegrationAvailability(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	index := uniqueName("availability")
	alias := index + "-read"

	mapping := map[string]any{
		"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0},
		"mappings": map[string]any{
			"dynamic": "strict",
			"properties": map[string]any{
				"resource_id": map[string]any{"type": "keyword"},
				"tenant":      map[string]any{"type": "keyword"},
				"kind":        map[string]any{"type": "keyword"},
				"free":        map[string]any{"type": "date_range", "format": "strict_date_optional_time||epoch_millis"},
			},
		},
	}
	if err := c.CreateIndex(ctx, index, mapping); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.DeleteIndex(context.Background(), index) })

	if ok, err := c.IndexExists(ctx, index); err != nil || !ok {
		t.Fatalf("IndexExists = %v, %v", ok, err)
	}
	if ok, err := c.IndexExists(ctx, index+"-missing"); err != nil || ok {
		t.Fatalf("IndexExists(missing) = %v, %v", ok, err)
	}
	if err := c.PutMapping(ctx, map[string]any{"properties": map[string]any{"capacity": map[string]any{"type": "integer"}}}, index); err != nil {
		t.Fatal(err)
	}
	mappings, err := c.GetMapping(ctx, index)
	if err != nil || !strings.Contains(string(mappings[index]), `"date_range"`) || !strings.Contains(string(mappings[index]), `"capacity"`) {
		t.Fatalf("GetMapping = %s, %v", mappings[index], err)
	}
	if err := c.UpdateAliases(ctx, opensearch.AliasAction{Add: &opensearch.AliasSpec{Index: index, Alias: alias}}); err != nil {
		t.Fatal(err)
	}
	if got, err := c.AliasIndices(ctx, alias); err != nil || len(got) != 1 || got[0] != index {
		t.Fatalf("AliasIndices = %v, %v", got, err)
	}

	docs := []resource{
		{"room-1", "acme", "room", []interval{{"2026-11-02T08:00:00Z", "2026-11-02T12:00:00Z"}, {"2026-11-02T14:00:00Z", "2026-11-02T18:00:00Z"}}},
		{"room-2", "acme", "room", []interval{{"2026-11-02T09:30:00Z", "2026-11-02T10:30:00Z"}}},
		{"room-3", "acme", "desk", []interval{{"2026-11-02T00:00:00Z", "2026-11-03T00:00:00Z"}}},
		{"room-4", "other", "room", []interval{{"2026-11-02T00:00:00Z", "2026-11-03T00:00:00Z"}}},
		{"room-5", "acme", "room", []interval{{"2026-11-02T10:00:00Z", "2026-11-02T11:30:00Z"}, {"2026-11-03T08:00:00Z", "2026-11-03T12:00:00Z"}}},
	}
	var bulk opensearch.BulkRequest
	for _, d := range docs {
		if err := bulk.Index(index, d.ResourceID, d); err != nil {
			t.Fatal(err)
		}
	}
	res, err := c.Bulk(ctx, &bulk, &opensearch.BulkOptions{Refresh: opensearch.RefreshWaitFor})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors || len(res.Items) != len(docs) || len(res.Failed()) != 0 {
		t.Fatalf("bulk: errors=%v items=%d failed=%v", res.Errors, len(res.Items), res.Failed())
	}

	// Per-item errors are reported: a strict mapping rejects unknown fields.
	var bad opensearch.BulkRequest
	_ = bad.Index(index, "bad", map[string]any{"unknown": 1})
	_ = bad.Index(index, "room-6", resource{ResourceID: "room-6", Tenant: "acme", Kind: "room"})
	res, err = c.Bulk(ctx, &bad, &opensearch.BulkOptions{Refresh: opensearch.RefreshTrue})
	if err != nil {
		t.Fatal(err)
	}
	if failed := res.Failed(); !res.Errors || len(failed) != 1 || failed[0].ID != "bad" || failed[0].Error.Type != "strict_dynamic_mapping_exception" {
		t.Fatalf("bulk failures = %+v", failed)
	}

	// Free 10:00-11:00 on 2026-11-02, acme rooms only.
	query := map[string]any{
		"query": map[string]any{
			"bool": map[string]any{
				"filter": []any{
					map[string]any{"range": map[string]any{"free": map[string]any{
						"gte": "2026-11-02T10:00:00Z", "lte": "2026-11-02T11:00:00Z", "relation": "contains",
					}}},
					map[string]any{"term": map[string]any{"tenant": "acme"}},
					map[string]any{"term": map[string]any{"kind": "room"}},
				},
			},
		},
		"sort": []any{map[string]any{"resource_id": "asc"}},
	}
	sr, err := c.Search(ctx, query, alias)
	if err != nil {
		t.Fatal(err)
	}
	found, err := opensearch.Sources[resource](sr.Hits.Hits)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range found {
		ids = append(ids, r.ResourceID)
	}
	if got, want := strings.Join(ids, ","), "room-1,room-5"; got != want {
		t.Fatalf("available = %s, want %s", got, want)
	}
	if opensearch.TotalHits(sr) != 2 {
		t.Fatalf("total = %d", opensearch.TotalHits(sr))
	}

	n, err := c.Count(ctx, map[string]any{"query": query["query"]}, index)
	if err != nil || n != 2 {
		t.Fatalf("Count = %d, %v", n, err)
	}

	// Single-document operations.
	if _, err := c.Index(ctx, index, "room-7", resource{ResourceID: "room-7", Tenant: "acme", Kind: "room"}, &opensearch.WriteOptions{Refresh: opensearch.RefreshWaitFor}); err != nil {
		t.Fatal(err)
	}
	doc, ok, err := opensearch.GetDocument[resource](ctx, c, index, "room-1")
	if err != nil || !ok || len(doc.Free) != 2 {
		t.Fatalf("GetDocument = %+v, %v, %v", doc, ok, err)
	}
	if _, ok, err := c.Get(ctx, index, "nope"); err != nil || ok {
		t.Fatalf("Get(missing) = %v, %v", ok, err)
	}
	if ok, err := c.Delete(ctx, index, "room-7", &opensearch.WriteOptions{Refresh: opensearch.RefreshTrue}); err != nil || !ok {
		t.Fatalf("Delete = %v, %v", ok, err)
	}

	// The typed error carries type and reason.
	_, err = c.Search(ctx, `{"query": {"no_such_query": {}}}`, index)
	if err == nil {
		t.Fatal("bad query succeeded")
	}
	var oe *opensearch.Error
	if !errors.As(err, &oe) || oe.StatusCode != 400 || oe.Type == "" {
		t.Fatalf("error = %#v", err)
	}
	if _, err := c.Search(ctx, nil, index+"-missing"); !opensearch.IsNotFound(err) {
		t.Fatalf("missing index error = %v", err)
	}

	dbq, err := c.DeleteByQuery(ctx, map[string]any{"query": map[string]any{"term": map[string]any{"tenant": "other"}}}, &opensearch.ByQueryOptions{Refresh: true}, index)
	if err != nil || dbq.Deleted != 1 {
		t.Fatalf("DeleteByQuery = %+v, %v", dbq, err)
	}
	if n, err := c.Count(ctx, nil, index); err != nil || n != 5 {
		t.Fatalf("Count after delete = %d, %v", n, err)
	}

	if err := c.DeleteIndex(ctx, index); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.IndexExists(ctx, index); ok {
		t.Fatal("index still exists")
	}
}

type event struct {
	N      int    `json:"n"`
	Bucket string `json:"bucket"`
	Parity string `json:"parity"`
}

// TestIntegrationVolume indexes 100k documents with the bulk indexer, reads
// them all back through a point in time, aggregates, deletes half with a
// sliced delete_by_query task, and moves an alias to a reindexed copy.
func TestIntegrationVolume(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	const total = 100_000
	index := uniqueName("volume")
	alias := index + "-alias"
	newIndex := index + "-v2"
	body := map[string]any{
		"settings": map[string]any{"number_of_shards": 2, "number_of_replicas": 0, "refresh_interval": "-1"},
		"mappings": map[string]any{"properties": map[string]any{
			"n": map[string]any{"type": "integer"}, "bucket": map[string]any{"type": "keyword"}, "parity": map[string]any{"type": "keyword"},
		}},
	}
	if err := c.CreateIndex(ctx, index, body); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.DeleteIndex(context.Background(), index, newIndex) })
	if err := c.UpdateAliases(ctx, opensearch.AliasAction{Add: &opensearch.AliasSpec{Index: index, Alias: alias}}); err != nil {
		t.Fatal(err)
	}

	var failures atomic.Int64
	bi := opensearch.NewBulkIndexer(c, opensearch.BulkIndexerConfig{
		Index: index, Workers: 4, FlushDocs: 5000, FlushBytes: 2 << 20,
		OnError: func(_ context.Context, item opensearch.BulkIndexerItem, _ opensearch.BulkItem, err error) {
			if failures.Add(1) < 5 {
				t.Errorf("item %s: %v", item.ID, err)
			}
		},
	})
	start := time.Now()
	for i := range total {
		parity := "even"
		if i%2 == 1 {
			parity = "odd"
		}
		err := bi.Add(ctx, opensearch.BulkIndexerItem{BulkOperation: opensearch.BulkOperation{
			Action: opensearch.ActionIndex, ID: fmt.Sprint(i),
			Body: event{N: i, Bucket: fmt.Sprintf("b%d", i%10), Parity: parity},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := bi.Close(ctx); err != nil {
		t.Fatal(err)
	}
	st := bi.Stats()
	t.Logf("bulk indexed %d docs in %s: %+v", total, time.Since(start), st)
	if st.Succeeded != total || st.Failed != 0 || failures.Load() != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if err := c.Refresh(ctx, index); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Count(ctx, nil, index); err != nil || n != total {
		t.Fatalf("Count = %d, %v", n, err)
	}

	// Point in time + search_after over everything.
	start = time.Now()
	seen := make([]bool, total)
	count := 0
	for hit, err := range c.Iterate(ctx, &opensearch.PageOptions{Size: 5000}, index) {
		if err != nil {
			t.Fatal(err)
		}
		e, err := opensearch.Source[event](hit)
		if err != nil {
			t.Fatal(err)
		}
		if seen[e.N] {
			t.Fatalf("doc %d seen twice", e.N)
		}
		seen[e.N] = true
		count++
	}
	t.Logf("iterated %d docs via PIT in %s", count, time.Since(start))
	if count != total {
		t.Fatalf("iterated %d, want %d", count, total)
	}

	// Scroll fallback over a filtered subset.
	count = 0
	for _, err := range c.Scroll(ctx, &opensearch.PageOptions{Size: 2000, Query: map[string]any{"term": map[string]any{"bucket": "b3"}}}, index) {
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != total/10 {
		t.Fatalf("scrolled %d, want %d", count, total/10)
	}

	// Aggregation, decoded into a caller-defined type.
	sr, err := c.Search(ctx, map[string]any{
		"size": 0,
		"aggs": map[string]any{
			"buckets": map[string]any{"terms": map[string]any{"field": "bucket", "size": 20}},
			"max_n":   map[string]any{"max": map[string]any{"field": "n"}},
		},
	}, index)
	if err != nil {
		t.Fatal(err)
	}
	type terms struct {
		Buckets []struct {
			Key      string `json:"key"`
			DocCount int64  `json:"doc_count"`
		} `json:"buckets"`
	}
	agg, ok, err := opensearch.Aggregation[terms](sr, "buckets")
	if err != nil || !ok || len(agg.Buckets) != 10 || agg.Buckets[0].DocCount != total/10 {
		t.Fatalf("terms = %+v, %v, %v", agg, ok, err)
	}
	maxN, _, err := opensearch.Aggregation[struct {
		Value float64 `json:"value"`
	}](sr, "max_n")
	if err != nil || maxN.Value != total-1 {
		t.Fatalf("max = %+v, %v", maxN, err)
	}

	// Multi-search, one result per search, errors per item.
	ms, err := c.MSearch(ctx,
		opensearch.MSearchItem{Header: map[string]any{"index": index}, Body: map[string]any{"size": 0, "query": map[string]any{"term": map[string]any{"parity": "odd"}}, "track_total_hits": true}},
		opensearch.MSearchItem{Header: map[string]any{"index": index + "-missing"}, Body: map[string]any{"size": 0}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].Response == nil || opensearch.TotalHits(ms[0].Response) != total/2 || ms[1].Err == nil || ms[1].Err.StatusCode != 404 {
		t.Fatalf("msearch = %+v", ms)
	}

	// Asynchronous search.
	as, err := c.SubmitAsyncSearch(ctx, map[string]any{"size": 0, "track_total_hits": true}, &opensearch.AsyncSearchOptions{WaitForCompletion: 5 * time.Second, KeepOnCompletion: true}, index)
	if err != nil {
		t.Fatal(err)
	}
	if as.ID == nil {
		t.Fatalf("async search returned no id: %+v", as)
	}
	deadline := time.Now().Add(time.Minute)
	for as.Response == nil || (as.State != nil && strings.Contains(*as.State, "RUNNING")) {
		if time.Now().After(deadline) {
			t.Fatalf("async search still %v", *as.State)
		}
		time.Sleep(200 * time.Millisecond)
		if as, err = c.GetAsyncSearch(ctx, *as.ID); err != nil {
			t.Fatal(err)
		}
	}
	if opensearch.TotalHits(as.Response) != total {
		t.Fatalf("async total = %d", opensearch.TotalHits(as.Response))
	}
	if err := c.DeleteAsyncSearch(ctx, *as.ID); err != nil {
		t.Fatal(err)
	}

	// Sliced delete_by_query as a task.
	taskID, err := c.StartDeleteByQuery(ctx, map[string]any{"query": map[string]any{"term": map[string]any{"parity": "odd"}}},
		&opensearch.ByQueryOptions{Slices: -1, Refresh: true, ProceedOnConflict: true}, index)
	if err != nil {
		t.Fatal(err)
	}
	task, err := c.WaitForTask(ctx, taskID, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if task.Response == nil || task.Response.Deleted != total/2 {
		t.Fatalf("delete task = %+v", task.Response)
	}
	if n, err := c.Count(ctx, nil, index); err != nil || n != total/2 {
		t.Fatalf("Count after delete = %d, %v", n, err)
	}

	// Zero-downtime reindex behind the alias.
	swap, err := c.SwapAlias(ctx, opensearch.AliasSwap{
		Alias: alias, NewIndex: newIndex, Slices: -1, DeleteOld: true, PollInterval: 250 * time.Millisecond,
		NewIndexBody: map[string]any{
			"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0},
			"mappings": body["mappings"],
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(swap.OldIndices) != 1 || swap.OldIndices[0] != index || swap.Reindex == nil || swap.Reindex.Created != total/2 {
		t.Fatalf("swap = %+v / %+v", swap, swap.Reindex)
	}
	if got, err := c.AliasIndices(ctx, alias); err != nil || len(got) != 1 || got[0] != newIndex {
		t.Fatalf("alias now on %v, %v", got, err)
	}
	if n, err := c.Count(ctx, nil, alias); err != nil || n != total/2 {
		t.Fatalf("Count via alias = %d, %v", n, err)
	}
	if ok, _ := c.IndexExists(ctx, index); ok {
		t.Fatal("old index not deleted")
	}
}

// TestIntegrationTemplatesAndISM covers index templates and ISM policies.
func TestIntegrationTemplatesAndISM(t *testing.T) {
	c := integrationClient(t)
	ctx := context.Background()
	name := uniqueName("tmpl")
	err := c.PutIndexTemplate(ctx, name, map[string]any{
		"index_patterns": []string{name + "-*"},
		"template": map[string]any{
			"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0},
			"mappings": map[string]any{"properties": map[string]any{"at": map[string]any{"type": "date"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.DeleteIndex(context.Background(), name+"-1")
		_ = c.DeleteIndexTemplate(context.Background(), name)
		_ = c.DeleteISMPolicy(context.Background(), name)
	})
	if ok, err := c.IndexTemplateExists(ctx, name); err != nil || !ok {
		t.Fatalf("IndexTemplateExists = %v, %v", ok, err)
	}
	if err := c.CreateIndex(ctx, name+"-1", nil); err != nil {
		t.Fatal(err)
	}
	m, err := c.GetMapping(ctx, name+"-1")
	if err != nil || !strings.Contains(string(m[name+"-1"]), `"date"`) {
		t.Fatalf("template mapping not applied: %s, %v", m[name+"-1"], err)
	}

	policy := map[string]any{"policy": map[string]any{
		"description":   "delete after 30d",
		"default_state": "hot",
		"states": []any{
			map[string]any{"name": "hot", "actions": []any{}, "transitions": []any{map[string]any{"state_name": "delete", "conditions": map[string]any{"min_index_age": "30d"}}}},
			map[string]any{"name": "delete", "actions": []any{map[string]any{"delete": map[string]any{}}}, "transitions": []any{}},
		},
	}}
	if _, err := c.PutISMPolicy(ctx, name, policy); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetISMPolicy(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ID string `json:"_id"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil || decoded.ID != name {
		t.Fatalf("GetISMPolicy = %s, %v", got, err)
	}
	if err := c.AddISMPolicy(ctx, name+"-1", name); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteIndexTemplate(ctx, name); err != nil {
		t.Fatal(err)
	}
	if ok, err := c.IndexTemplateExists(ctx, name); err != nil || ok {
		t.Fatalf("template still exists: %v, %v", ok, err)
	}
}
