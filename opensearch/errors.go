package opensearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Error is an OpenSearch error response: any status of 300 or more.
type Error struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Type and Reason come from the response's error object, such as
	// "index_not_found_exception" and "no such index [x]".
	Type   string
	Reason string
	// RootCause lists the underlying causes OpenSearch reported.
	RootCause []ErrorCause
	// Body is the raw response body.
	Body []byte
}

// ErrorCause is one cause inside an error response, or a bulk item error.
type ErrorCause struct {
	Type      string          `json:"type"`
	Reason    string          `json:"reason"`
	Index     string          `json:"index,omitempty"`
	Shard     json.RawMessage `json:"shard,omitempty"`
	CausedBy  *ErrorCause     `json:"caused_by,omitempty"`
	RootCause []ErrorCause    `json:"root_cause,omitempty"`
}

func (e *Error) Error() string {
	switch {
	case e.Type != "":
		return fmt.Sprintf("opensearch: %d %s: %s", e.StatusCode, e.Type, e.Reason)
	case e.Reason != "":
		return fmt.Sprintf("opensearch: %d: %s", e.StatusCode, e.Reason)
	}
	body := strings.TrimSpace(string(e.Body))
	if len(body) > 512 {
		body = body[:512] + "..."
	}
	if body == "" {
		body = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("opensearch: %d: %s", e.StatusCode, body)
}

func (c ErrorCause) Error() string {
	return c.Type + ": " + c.Reason
}

// IsNotFound reports whether err is an OpenSearch 404.
func IsNotFound(err error) bool { return statusOf(err) == http.StatusNotFound }

// IsConflict reports whether err is an OpenSearch 409 (version conflict,
// document or index already exists).
func IsConflict(err error) bool { return statusOf(err) == http.StatusConflict }

// IsTooManyRequests reports whether err is an OpenSearch 429 that outlived
// the client's retries.
func IsTooManyRequests(err error) bool { return statusOf(err) == http.StatusTooManyRequests }

func statusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.StatusCode
	}
	return 0
}

// newError builds an *Error from a response status and body.
func newError(status int, body []byte) *Error {
	e := &Error{StatusCode: status, Body: body}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Error) == 0 {
		return e
	}
	var cause ErrorCause
	if json.Unmarshal(envelope.Error, &cause) == nil {
		e.Type, e.Reason, e.RootCause = cause.Type, cause.Reason, cause.RootCause
		return e
	}
	var reason string
	if json.Unmarshal(envelope.Error, &reason) == nil {
		e.Reason = reason
	}
	return e
}

// decode reads resp, returns an *Error for statuses of 300 or more, and
// otherwise decodes the body into out (skipped when out is nil).
func decode(resp *http.Response, err error, out any) error {
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return newError(resp.StatusCode, body)
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], body...)
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("opensearch: decode %s response: %w", resp.Request.URL.Path, err)
	}
	return nil
}

// exists maps 200 to true and 404 to false, for HEAD requests.
func exists(resp *http.Response, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode >= 300:
		return false, newError(resp.StatusCode, body)
	}
	return true, nil
}
