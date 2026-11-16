package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxResponseBodySize       = 10 << 20
	maxFileResponseBodySize   = 1 << 20
	maxTextResponseBodySize   = 1 << 20
	maxStreamResponseBodySize = 2 << 20
)

type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// GetText reads a bounded text/plain response from the OpenSVC daemon.
func (c *Client) GetText(ctx context.Context, path string, query url.Values) ([]byte, error) {
	endpoint := c.baseURL.JoinPath(strings.TrimPrefix(path, "/"))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create OpenSVC daemon GET request: %w", err)
	}
	request.Header.Set("Accept", "text/plain")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request OpenSVC daemon %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, responseError(http.MethodGet, path, response)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/plain" {
		return nil, unexpected(http.MethodGet, path, fmt.Sprintf("returned unexpected content type %q", response.Header.Get("Content-Type")))
	}

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxTextResponseBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("read OpenSVC daemon %s response: %w", path, err)
	}
	if len(payload) > maxTextResponseBodySize {
		return nil, fmt.Errorf("OpenSVC daemon %s response exceeds %d bytes", path, maxTextResponseBodySize)
	}
	return payload, nil
}

func New(rawBaseURL string, httpClient *http.Client) (*Client, error) {
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse OpenSVC daemon URL: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, fmt.Errorf("OpenSVC daemon URL must use http or https")
	}
	if baseURL.Host == "" {
		return nil, fmt.Errorf("OpenSVC daemon URL must include a host")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: baseURL, httpClient: httpClient}, nil
}

func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, output any) error {
	return c.doJSON(ctx, http.MethodGet, path, query, nil, output)
}

// GetNoContent sends an authenticated GET and requires an exact 204 response.
func (c *Client) GetNoContent(ctx context.Context, path string, query url.Values) error {
	endpoint := c.baseURL.JoinPath(strings.TrimPrefix(path, "/"))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create OpenSVC daemon GET request: %w", err)
	}
	request.Header.Set("Accept", "*/*")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request OpenSVC daemon %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return responseError(http.MethodGet, path, response)
	}
	if response.StatusCode != http.StatusNoContent {
		return unexpected(http.MethodGet, path, fmt.Sprintf("returned unexpected status %d, want 204", response.StatusCode))
	}
	return nil
}

func (c *Client) PostJSON(ctx context.Context, path string, query url.Values, input any, output any) error {
	_, err := c.PostJSONWithHeader(ctx, path, query, input, output)
	return err
}

// GetFile reads a bounded configuration file response from the OpenSVC daemon.
func (c *Client) GetFile(ctx context.Context, path string, query url.Values) ([]byte, error) {
	endpoint := c.baseURL.JoinPath(strings.TrimPrefix(path, "/"))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create OpenSVC daemon GET request: %w", err)
	}
	request.Header.Set("Accept", "application/octet-stream")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request OpenSVC daemon %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, responseError(http.MethodGet, path, response)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/octet-stream" {
		return nil, unexpected(http.MethodGet, path, fmt.Sprintf("returned unexpected content type %q", response.Header.Get("Content-Type")))
	}

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxFileResponseBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("read OpenSVC daemon %s response: %w", path, err)
	}
	if len(payload) > maxFileResponseBodySize {
		return nil, fmt.Errorf("OpenSVC daemon %s response exceeds %d bytes", path, maxFileResponseBodySize)
	}
	return payload, nil
}

// GetBlob reads a bounded value or file and gives its media type. The daemon
// answers the value of a data key as text/plain when it is valid UTF-8,
// application/octet-stream otherwise, and relays the answer of a peer as
// application/json, and a resource file by its extension, text/xml for a
// domain definition: any type of a 200 is taken, its errors coming as 4xx or
// 5xx. A value beyond the bound is cut, and complete says so. A 200 without
// body nor content type gives an empty media type.
func (c *Client) GetBlob(ctx context.Context, path string, query url.Values) ([]byte, string, bool, error) {
	endpoint := c.baseURL.JoinPath(strings.TrimPrefix(path, "/"))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, "", false, fmt.Errorf("create OpenSVC daemon GET request: %w", err)
	}
	request.Header.Set("Accept", "*/*")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, "", false, fmt.Errorf("request OpenSVC daemon %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, "", false, responseError(http.MethodGet, path, response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxFileResponseBodySize+1))
	if err != nil {
		return nil, "", false, fmt.Errorf("read OpenSVC daemon %s response: %w", path, err)
	}
	raw := response.Header.Get("Content-Type")
	if raw == "" && len(payload) == 0 {
		return nil, "", true, nil
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		mediaType = "application/octet-stream"
	}
	if len(payload) > maxFileResponseBodySize {
		return payload[:maxFileResponseBodySize], mediaType, false, nil
	}
	return payload, mediaType, true, nil
}

// GetStream sends an authenticated GET request and delivers bounded opaque
// response chunks to consume. The OpenSVC container-log endpoint declares
// text/event-stream but returns raw log bytes rather than SSE framing.
func (c *Client) GetStream(ctx context.Context, path string, query url.Values, consume func([]byte) error) error {
	if consume == nil {
		return fmt.Errorf("OpenSVC daemon stream consumer is nil")
	}
	endpoint := c.baseURL.JoinPath(strings.TrimPrefix(path, "/"))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("create OpenSVC daemon GET request: %w", err)
	}
	request.Header.Set("Accept", "text/event-stream")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request OpenSVC daemon %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return responseError(http.MethodGet, path, response)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return unexpected(http.MethodGet, path, fmt.Sprintf("returned unexpected content type %q", response.Header.Get("Content-Type")))
	}

	reader := io.LimitReader(response.Body, maxStreamResponseBodySize+1)
	buffer := make([]byte, 32<<10)
	total := 0
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			total += n
			if total > maxStreamResponseBodySize {
				return fmt.Errorf("OpenSVC daemon %s stream exceeds %d bytes", path, maxStreamResponseBodySize)
			}
			if err := consume(buffer[:n]); err != nil {
				return fmt.Errorf("consume OpenSVC daemon stream: %w", err)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return fmt.Errorf("read OpenSVC daemon %s stream: %w", path, readErr)
		}
	}
}

func (c *Client) doJSON(ctx context.Context, method string, path string, query url.Values, body io.Reader, output any) error {
	endpoint := c.baseURL.JoinPath(strings.TrimPrefix(path, "/"))
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create OpenSVC daemon %s request: %w", method, err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request OpenSVC daemon %s: %w", path, err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return responseError(method, path, response)
	}

	if output == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBodySize))
	if err := decoder.Decode(output); err != nil {
		return decodeError(method, path, err)
	}
	return nil
}

// PatchJSON sends an authenticated PATCH request, its parameters in the
// query, and decodes the JSON answer. It returns the headers of the answer,
// such as the time the daemon gives a write. A wait the request carries is
// held under the timeout of the client, so that the answer, written or
// still propagating, arrives before the client gives up on a done write.
func (c *Client) PatchJSON(ctx context.Context, path string, query url.Values, output any) (http.Header, error) {
	return c.patch(ctx, path, query, nil, output)
}

// PatchJSONBody is PatchJSON with a JSON body, such as the operations on
// the data keys of an object.
func (c *Client) PatchJSONBody(ctx context.Context, path string, query url.Values, input any, output any) (http.Header, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode OpenSVC daemon %s request: %w", path, err)
	}
	return c.patch(ctx, path, query, payload, output)
}

func (c *Client) patch(ctx context.Context, path string, query url.Values, payload []byte, output any) (http.Header, error) {
	query = c.boundWait(query)
	endpoint := c.baseURL.JoinPath(strings.TrimPrefix(path, "/"))
	endpoint.RawQuery = query.Encode()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create OpenSVC daemon PATCH request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request OpenSVC daemon %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return response.Header, responseError(http.MethodPatch, path, response)
	}
	if output != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBodySize)).Decode(output); err != nil {
			return response.Header, decodeError(http.MethodPatch, path, err)
		}
	}
	return response.Header, nil
}

// PostFile sends an authenticated POST request with a file body, as
// application/octet-stream, and expects 204 No Content. It returns the
// headers of the answer, such as the time the daemon gives the file written.
func (c *Client) PostFile(ctx context.Context, path string, query url.Values, body []byte) (http.Header, error) {
	endpoint := c.baseURL.JoinPath(strings.TrimPrefix(path, "/"))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create OpenSVC daemon POST request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request OpenSVC daemon %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return response.Header, responseError(http.MethodPost, path, response)
	}
	if response.StatusCode != http.StatusNoContent {
		return response.Header, unexpected(http.MethodPost, path, fmt.Sprintf("returned unexpected status %d, want 204", response.StatusCode))
	}
	return response.Header, nil
}

// waitMargin is the time a held answer keeps under the timeout of the
// client, for the daemon to answer and the answer to arrive.
const waitMargin = 3 * time.Second

// boundWait holds the wait of a request under the timeout of the client,
// and drops it when the timeout leaves no time to wait.
func (c *Client) boundWait(query url.Values) url.Values {
	raw := query.Get("wait")
	if raw == "" || c.httpClient.Timeout <= 0 {
		return query
	}
	wait, err := time.ParseDuration(raw)
	if err != nil {
		return query
	}
	bounded := url.Values{}
	for key, values := range query {
		bounded[key] = values
	}
	limit := c.httpClient.Timeout - waitMargin
	switch {
	case limit < time.Second:
		bounded.Del("wait")
	case wait > limit:
		bounded.Set("wait", limit.Truncate(time.Second).String())
	}
	return bounded
}

// PostJSONWithHeader sends an authenticated POST request and decodes the JSON
// answer, as PostJSON does, and returns the headers of the answer, such as the
// time the daemon gives a configuration it wrote.
func (c *Client) PostJSONWithHeader(ctx context.Context, path string, query url.Values, input any, output any) (http.Header, error) {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("encode OpenSVC daemon POST request body: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	endpoint := c.baseURL.JoinPath(strings.TrimPrefix(path, "/"))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create OpenSVC daemon POST request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request OpenSVC daemon %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return response.Header, responseError(http.MethodPost, path, response)
	}
	if output != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBodySize)).Decode(output); err != nil {
			return response.Header, decodeError(http.MethodPost, path, err)
		}
	}
	return response.Header, nil
}
