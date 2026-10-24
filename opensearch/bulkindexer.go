package opensearch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// BulkIndexerConfig configures a BulkIndexer. Zero values take defaults.
type BulkIndexerConfig struct {
	// Workers is the number of concurrent bulk requests; default 4.
	Workers int
	// FlushDocs flushes a worker's batch at this many operations; default
	// 1000.
	FlushDocs int
	// FlushBytes flushes a worker's batch at this many body bytes; default
	// 5 MiB.
	FlushBytes int
	// FlushInterval flushes partial batches this often; default 1s,
	// negative disables.
	FlushInterval time.Duration
	// QueueSize is how many added items may wait for a worker before Add
	// blocks (backpressure); default Workers * FlushDocs.
	QueueSize int

	// Index is the default index for operations that name none.
	Index string
	// Refresh is passed to every bulk request.
	Refresh string

	// MaxRetries is how many times an operation rejected with 429 is sent
	// again; default 3, negative disables. Whole requests are also retried
	// by the client on 429 and 5xx.
	MaxRetries int
	// RetryBackoff returns the wait before item retry n; default
	// DefaultBackoff.
	RetryBackoff func(attempt int) time.Duration

	// OnError is called for every operation that finally failed, after the
	// item's own OnFailure. err is the request error when the whole request
	// failed, otherwise the item's ErrorCause.
	OnError func(ctx context.Context, item BulkIndexerItem, result BulkItem, err error)
}

// BulkIndexerItem is one operation queued on a BulkIndexer.
type BulkIndexerItem struct {
	BulkOperation
	// OnSuccess is called when the operation succeeded.
	OnSuccess func(ctx context.Context, item BulkIndexerItem, result BulkItem)
	// OnFailure is called when the operation finally failed.
	OnFailure func(ctx context.Context, item BulkIndexerItem, result BulkItem, err error)
}

// BulkIndexerStats are cumulative counters.
type BulkIndexerStats struct {
	Added     uint64 // operations accepted by Add
	Flushed   uint64 // operations sent, retries included
	Succeeded uint64
	Failed    uint64
	Retried   uint64 // operations sent again after a 429
	Requests  uint64 // bulk requests sent
	Bytes     uint64 // body bytes sent
}

// ErrBulkIndexerClosed is returned by Add after Close.
var ErrBulkIndexerClosed = errors.New("opensearch: bulk indexer closed")

// BulkIndexer streams operations into concurrent bulk requests, batching by
// count, size and time.
type BulkIndexer struct {
	client *Client
	cfg    BulkIndexerConfig
	queue  chan queued
	wg     sync.WaitGroup

	mu      sync.RWMutex
	closed  bool
	flushMu sync.Mutex // one flush barrier in flight at a time

	added, flushed, succeeded, failed, retried, requests, bytes atomic.Uint64
}

type queued struct {
	item    BulkIndexerItem
	line    []byte
	attempt int
	barrier *flushBarrier
}

type flushBarrier struct {
	ctx     context.Context
	done    sync.WaitGroup
	release chan struct{}
}

// NewBulkIndexer starts a BulkIndexer. Close it to flush and stop.
func NewBulkIndexer(c *Client, cfg BulkIndexerConfig) *BulkIndexer {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.FlushDocs <= 0 {
		cfg.FlushDocs = 1000
	}
	if cfg.FlushBytes <= 0 {
		cfg.FlushBytes = 5 << 20
	}
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = time.Second
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = cfg.Workers * cfg.FlushDocs
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.RetryBackoff == nil {
		cfg.RetryBackoff = DefaultBackoff
	}
	bi := &BulkIndexer{client: c, cfg: cfg, queue: make(chan queued, cfg.QueueSize)}
	for range cfg.Workers {
		bi.wg.Add(1)
		go bi.work()
	}
	return bi
}

// Add queues item, blocking while the queue is full. It fails when item
// can't be encoded, ctx ends, or the indexer is closed.
func (bi *BulkIndexer) Add(ctx context.Context, item BulkIndexerItem) error {
	line, err := encodeOperation(item.BulkOperation)
	if err != nil {
		return err
	}
	bi.mu.RLock()
	defer bi.mu.RUnlock()
	if bi.closed {
		return ErrBulkIndexerClosed
	}
	select {
	case bi.queue <- queued{item: item, line: line}:
		bi.added.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Flush sends every operation added before the call and waits for the
// responses.
func (bi *BulkIndexer) Flush(ctx context.Context) error {
	bi.mu.RLock()
	defer bi.mu.RUnlock()
	if bi.closed {
		return ErrBulkIndexerClosed
	}
	return bi.flush(ctx)
}

func (bi *BulkIndexer) flush(ctx context.Context) error {
	bi.flushMu.Lock()
	defer bi.flushMu.Unlock()
	b := &flushBarrier{ctx: ctx, release: make(chan struct{})}
	b.done.Add(bi.cfg.Workers)
	defer close(b.release)
	for range bi.cfg.Workers {
		select {
		case bi.queue <- queued{barrier: b}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	waited := make(chan struct{})
	go func() { b.done.Wait(); close(waited) }()
	select {
	case <-waited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close flushes, stops the workers and waits for them.
func (bi *BulkIndexer) Close(ctx context.Context) error {
	bi.mu.Lock()
	defer bi.mu.Unlock()
	if bi.closed {
		return ErrBulkIndexerClosed
	}
	err := bi.flush(ctx)
	bi.closed = true
	close(bi.queue)
	bi.wg.Wait()
	return err
}

// Stats returns the counters so far.
func (bi *BulkIndexer) Stats() BulkIndexerStats {
	return BulkIndexerStats{
		Added: bi.added.Load(), Flushed: bi.flushed.Load(), Succeeded: bi.succeeded.Load(),
		Failed: bi.failed.Load(), Retried: bi.retried.Load(), Requests: bi.requests.Load(),
		Bytes: bi.bytes.Load(),
	}
}

func (bi *BulkIndexer) work() {
	defer bi.wg.Done()
	var (
		batch []queued
		size  int
		tick  <-chan time.Time
	)
	if bi.cfg.FlushInterval > 0 {
		t := time.NewTicker(bi.cfg.FlushInterval)
		defer t.Stop()
		tick = t.C
	}
	send := func(ctx context.Context) {
		if len(batch) > 0 {
			bi.send(ctx, batch)
		}
		batch, size = nil, 0
	}
	for {
		select {
		case q, ok := <-bi.queue:
			if !ok {
				send(context.Background())
				return
			}
			if q.barrier != nil {
				send(q.barrier.ctx)
				q.barrier.done.Done()
				<-q.barrier.release
				continue
			}
			batch = append(batch, q)
			size += len(q.line)
			if len(batch) >= bi.cfg.FlushDocs || size >= bi.cfg.FlushBytes {
				send(context.Background())
			}
		case <-tick:
			send(context.Background())
		}
	}
}

// send issues one bulk request for batch, then resends the operations
// rejected with 429 until they succeed or run out of retries.
func (bi *BulkIndexer) send(ctx context.Context, batch []queued) {
	for len(batch) > 0 {
		var body bytes.Buffer
		for _, q := range batch {
			body.Write(q.line)
		}
		bi.requests.Add(1)
		bi.flushed.Add(uint64(len(batch)))
		bi.bytes.Add(uint64(body.Len()))
		res, err := bi.client.bulk(ctx, body.Bytes(), &BulkOptions{Index: bi.cfg.Index, Refresh: bi.cfg.Refresh})
		if err != nil {
			for _, q := range batch {
				bi.fail(ctx, q.item, BulkItem{Action: q.item.Action, Index: q.item.Index, ID: q.item.ID}, err)
			}
			return
		}
		var again []queued
		for i, q := range batch {
			if i >= len(res.Items) {
				bi.fail(ctx, q.item, BulkItem{}, errors.New("opensearch: bulk response has fewer items than the request"))
				continue
			}
			it := res.Items[i]
			switch {
			case it.Status == http.StatusTooManyRequests && bi.cfg.MaxRetries > 0 && q.attempt < bi.cfg.MaxRetries:
				q.attempt++
				again = append(again, q)
			case it.Error != nil || it.Status >= 300:
				var cause error = it.Error
				if it.Error == nil {
					cause = &Error{StatusCode: it.Status}
				}
				bi.fail(ctx, q.item, it, cause)
			default:
				bi.succeeded.Add(1)
				if q.item.OnSuccess != nil {
					q.item.OnSuccess(ctx, q.item, it)
				}
			}
		}
		if len(again) == 0 {
			return
		}
		bi.retried.Add(uint64(len(again)))
		select {
		case <-time.After(bi.cfg.RetryBackoff(again[0].attempt)):
		case <-ctx.Done():
			for _, q := range again {
				bi.fail(ctx, q.item, BulkItem{}, ctx.Err())
			}
			return
		}
		batch = again
	}
}

func (bi *BulkIndexer) fail(ctx context.Context, item BulkIndexerItem, result BulkItem, err error) {
	bi.failed.Add(1)
	if item.OnFailure != nil {
		item.OnFailure(ctx, item, result, err)
	}
	if bi.cfg.OnError != nil {
		bi.cfg.OnError(ctx, item, result, err)
	}
}
