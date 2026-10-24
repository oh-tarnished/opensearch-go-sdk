// Package opensearch is the hand-written layer over the generated
// opensearchapi package: client construction (authentication, TLS, retries),
// typed errors, generic JSON helpers, and the helpers large-scale work needs
// (bulk indexing, point-in-time pagination, by-query tasks, multi and async
// search, alias-swap reindexing, index templates and ISM policies).
//
// Every OpenSearch operation in the specification is reachable through
// Client.API (typed responses) or Client.Raw (plain *http.Response); the
// methods here cover the common paths with any-JSON request bodies.
package opensearch

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/oh-tarnished/opensearch-go-sdk/opensearchapi"
)

// Config configures a Client. Only URL is required.
type Config struct {
	// URL is the cluster's base URL, such as https://localhost:9200.
	URL string

	// Username and Password enable HTTP basic authentication.
	Username string
	Password string
	// BearerToken sends an Authorization: Bearer header. It takes precedence
	// over Username and Password.
	BearerToken string
	// Header is added to every request.
	Header http.Header

	// HTTPClient sends the requests. When nil, a client with TLSConfig and
	// Timeout is built.
	HTTPClient *http.Client
	// TLSConfig is used when HTTPClient is nil.
	TLSConfig *tls.Config
	// Timeout bounds each attempt when HTTPClient is nil; zero means none.
	Timeout time.Duration

	// MaxRetries is how many times a request that got 429, 502, 503 or 504,
	// or failed in transport, is retried. Zero means 3; negative disables
	// retries.
	MaxRetries int
	// RetryBackoff returns the wait before retry attempt n (1-based). The
	// default is exponential from 100ms, capped at 10s, with full jitter.
	RetryBackoff func(attempt int) time.Duration
	// RetryStatuses replaces the statuses that are retried.
	RetryStatuses []int
}

// Client talks to one OpenSearch cluster.
type Client struct {
	// API is the generated client with typed responses, covering every
	// operation in the specification.
	API *opensearchapi.ClientWithResponses
	// Raw is the generated client returning *http.Response.
	Raw *opensearchapi.Client

	transport *transport
}

// New builds a Client from cfg.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("opensearch: Config.URL is required")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = cfg.TLSConfig
		t.MaxIdleConnsPerHost = 64
		httpClient = &http.Client{Transport: t, Timeout: cfg.Timeout}
	}
	tr := &transport{
		client:     httpClient,
		header:     cfg.Header.Clone(),
		maxRetries: cfg.MaxRetries,
		backoff:    cfg.RetryBackoff,
		statuses:   cfg.RetryStatuses,
	}
	switch {
	case cfg.BearerToken != "":
		tr.authorization = "Bearer " + cfg.BearerToken
	case cfg.Username != "" || cfg.Password != "":
		tr.authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(cfg.Username+":"+cfg.Password))
	}
	if tr.maxRetries == 0 {
		tr.maxRetries = 3
	}
	if tr.backoff == nil {
		tr.backoff = DefaultBackoff
	}
	if tr.statuses == nil {
		tr.statuses = []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	}
	base := strings.TrimRight(cfg.URL, "/") + "/"
	raw, err := opensearchapi.NewClient(base, opensearchapi.WithHTTPClient(tr))
	if err != nil {
		return nil, err
	}
	api, err := opensearchapi.NewClientWithResponses(base, opensearchapi.WithHTTPClient(tr))
	if err != nil {
		return nil, err
	}
	return &Client{API: api, Raw: raw, transport: tr}, nil
}

// DefaultBackoff is exponential backoff from 100ms, capped at 10s, with
// full jitter.
func DefaultBackoff(attempt int) time.Duration {
	d := 100 * time.Millisecond << min(attempt-1, 7)
	d = min(d, 10*time.Second)
	return time.Duration(rand.Int64N(int64(d)) + 1)
}

// transport adds authentication and retries to every generated request.
type transport struct {
	client        *http.Client
	header        http.Header
	authorization string
	maxRetries    int
	backoff       func(int) time.Duration
	statuses      []int
}

// Do implements opensearchapi.HttpRequestDoer.
func (t *transport) Do(req *http.Request) (*http.Response, error) {
	for k, vs := range t.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if t.authorization != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", t.authorization)
	}
	// Requests are replayed on retry, so the body must be re-readable.
	if req.Body != nil && req.GetBody == nil {
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	for attempt := 0; ; attempt++ {
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
		resp, err := t.client.Do(req)
		retry := attempt < t.maxRetries && req.Context().Err() == nil &&
			(err != nil || t.retryable(resp.StatusCode))
		if !retry {
			return resp, err
		}
		if resp != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		timer := time.NewTimer(t.backoff(attempt + 1))
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
}

func (t *transport) retryable(status int) bool {
	for _, s := range t.statuses {
		if s == status {
			return true
		}
	}
	return false
}

// Ping reports whether the cluster answers GET /.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.Raw.Info(ctx, nil)
	return decode(resp, err, nil)
}

// Health returns cluster health. params may be nil.
func (c *Client) Health(ctx context.Context, params *opensearchapi.ClusterHealthParams) (*opensearchapi.ClusterHealth200, error) {
	var out opensearchapi.ClusterHealth200
	resp, err := c.Raw.ClusterHealth(ctx, params)
	if err := decode(resp, err, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WaitForStatus blocks until the cluster reaches status ("green", "yellow")
// or ctx ends.
func (c *Client) WaitForStatus(ctx context.Context, status string) error {
	for {
		h, err := c.Health(ctx, nil)
		if err == nil {
			got, _ := h.Status.MarshalJSON()
			if rank(strings.Trim(string(got), `"`)) >= rank(status) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return fmt.Errorf("opensearch: waiting for %s: %w (last error: %v)", status, ctx.Err(), err)
			}
			return fmt.Errorf("opensearch: waiting for %s: %w", status, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func rank(status string) int {
	switch strings.ToLower(status) {
	case "green":
		return 2
	case "yellow":
		return 1
	}
	return 0
}
