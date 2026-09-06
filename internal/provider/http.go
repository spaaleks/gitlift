package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spaaleks/gitlift/internal/logx"
)

const (
	perPage     = 100
	maxPages    = 20
	maxAttempts = 3
	maxBodyLog  = 400
)

type APIError struct {
	Status  int
	Method  string
	URL     string
	Message string
	Details []string
	Body    string
}

func (e *APIError) Error() string {
	message := e.Message
	if message == "" {
		message = http.StatusText(e.Status)
	}
	if len(e.Details) > 0 {
		message += ": " + strings.Join(e.Details, ", ")
	}
	return fmt.Sprintf("HTTP %d %s %s: %s", e.Status, e.Method, e.URL, message)
}

func StatusOf(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

func IsStatus(err error, statuses ...int) bool {
	got := StatusOf(err)
	for _, want := range statuses {
		if got == want {
			return true
		}
	}
	return false
}

func Message(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		parts := []string{apiErr.Message}
		parts = append(parts, apiErr.Details...)
		joined := strings.Join(trimEmpty(parts), ": ")
		if joined != "" {
			return joined
		}
		return http.StatusText(apiErr.Status)
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func Explain(err error) string {
	if err == nil {
		return ""
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err.Error()
	}

	message := Message(err)
	switch apiErr.Status {
	case http.StatusUnauthorized:
		return "not authorized, the token was rejected (" + message + ")"
	case http.StatusForbidden:
		if PlanLimited(err) {
			return message
		}
		return "forbidden, the token is missing a scope or admin rights (" + message + ")"
	case http.StatusNotFound:
		return "not found, wrong path, or the token cannot see it (" + message + ")"
	}
	return fmt.Sprintf("HTTP %d: %s", apiErr.Status, message)
}

func PlanLimited(err error) bool {
	return IsStatus(err, http.StatusForbidden) &&
		strings.Contains(strings.ToLower(Message(err)), "upgrade")
}

func NameTaken(err error) bool {
	message := strings.ToLower(Message(err))
	return IsStatus(err, http.StatusUnprocessableEntity, http.StatusBadRequest, http.StatusConflict) &&
		(strings.Contains(message, "already exists") ||
			strings.Contains(message, "has already been taken") ||
			strings.Contains(message, "already been taken"))
}

type Client struct {
	baseURL string
	header  http.Header
	http    *http.Client
	label   string
}

func NewClient(label, baseURL string, header http.Header) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		header:  header,
		label:   label,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Host() string {
	parsed, err := url.Parse(c.baseURL)
	if err != nil {
		return c.baseURL
	}
	return parsed.Host
}

type Request struct {
	Method string
	Path   string
	Query  url.Values
	Body   any
	Out    any
}

func (c *Client) Do(ctx context.Context, req Request) error {
	_, err := c.do(ctx, req)
	return err
}

func (c *Client) do(ctx context.Context, req Request) (*http.Response, error) {
	endpoint := c.baseURL + req.Path
	if len(req.Query) > 0 {
		endpoint += "?" + req.Query.Encode()
	}

	var payload []byte
	if req.Body != nil {
		encoded, err := json.Marshal(req.Body)
		if err != nil {
			return nil, err
		}
		payload = encoded
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}

		httpReq, err := http.NewRequestWithContext(ctx, req.Method, endpoint, reader)
		if err != nil {
			return nil, err
		}
		for key, values := range c.header {
			for _, value := range values {
				httpReq.Header.Add(key, value)
			}
		}
		if payload != nil {
			httpReq.Header.Set("Content-Type", "application/json")
		}

		res, err := c.http.Do(httpReq)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt < maxAttempts {
				time.Sleep(backoff(attempt))
				continue
			}
			logx.Err(c.label+" "+req.Method+" "+req.Path, err)
			return nil, err
		}

		body, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if readErr != nil {
			return nil, readErr
		}

		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			lastErr = newAPIError(res.StatusCode, req.Method, req.Path, body)
			if attempt < maxAttempts {
				time.Sleep(retryAfter(res, attempt))
				continue
			}
		}

		if res.StatusCode >= 400 {
			apiErr := newAPIError(res.StatusCode, req.Method, req.Path, body)
			logx.Err(c.label, apiErr)
			return res, apiErr
		}

		if req.Out != nil && len(body) > 0 {
			if err := json.Unmarshal(body, req.Out); err != nil {
				return res, fmt.Errorf("decoding %s %s: %w", req.Method, req.Path, err)
			}
		}
		return res, nil
	}

	logx.Err(c.label, lastErr)
	return nil, lastErr
}

func (c *Client) Paginate(ctx context.Context, path string, query url.Values, page func(raw json.RawMessage) (int, error)) error {
	if query == nil {
		query = url.Values{}
	}
	query.Set("per_page", strconv.Itoa(perPage))

	for number := 1; number <= maxPages; number++ {
		query.Set("page", strconv.Itoa(number))

		var raw json.RawMessage
		if err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Query: query, Out: &raw}); err != nil {
			return err
		}

		count, err := page(raw)
		if err != nil {
			return err
		}
		if count < perPage {
			return nil
		}
	}
	return nil
}

func newAPIError(status int, method, path string, body []byte) *APIError {
	apiErr := &APIError{Status: status, Method: method, URL: path, Body: truncate(string(body))}

	var payload struct {
		Message          string          `json:"message"`
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
		Errors           json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return apiErr
	}

	apiErr.Message = payload.Message
	if apiErr.Message == "" && len(payload.Error) > 0 {
		apiErr.Message = flatten(payload.Error)
	}
	if apiErr.Message == "" {
		apiErr.Message = payload.ErrorDescription
	}
	if len(payload.Errors) > 0 {
		apiErr.Details = strings.Split(flatten(payload.Errors), ", ")
		apiErr.Details = trimEmpty(apiErr.Details)
	}

	return apiErr
}

func flatten(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}

	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			parts = append(parts, flatten(item))
		}
		return strings.Join(trimEmpty(parts), ", ")
	}

	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		if message, ok := object["message"]; ok {
			return flatten(message)
		}
		parts := make([]string, 0, len(object))
		for key, value := range object {
			parts = append(parts, key+" "+flatten(value))
		}
		return strings.Join(trimEmpty(parts), ", ")
	}

	return strings.Trim(string(raw), `"`)
}

func trimEmpty(in []string) []string {
	out := in[:0]
	for _, item := range in {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func truncate(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxBodyLog {
		return s
	}
	return s[:maxBodyLog] + "…"
}

func backoff(attempt int) time.Duration {
	return time.Duration(attempt*attempt) * 250 * time.Millisecond
}

func retryAfter(res *http.Response, attempt int) time.Duration {
	if header := res.Header.Get("Retry-After"); header != "" {
		if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 && seconds <= 30 {
			return time.Duration(seconds) * time.Second
		}
	}
	return backoff(attempt)
}
