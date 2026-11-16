package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const (
	maxSSEResponseBodySize = 2 << 20
	maxSSELineSize         = 256 << 10
)

// GetSSE sends an authenticated GET request and delivers bounded server-sent
// events to consume. It does not reconnect or follow a stream after EOF.
func (c *Client) GetSSE(
	ctx context.Context,
	path string,
	query url.Values,
	consume func(event string, id string, data []byte) error,
) error {
	if consume == nil {
		return fmt.Errorf("OpenSVC daemon SSE consumer is nil")
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

	reader := &limitedSSEReader{reader: response.Body, remaining: maxSSEResponseBodySize + 1}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxSSELineSize)

	var eventType string
	var eventID string
	dataLines := make([]string, 0, 1)
	dispatch := func() error {
		if len(dataLines) == 0 {
			eventType = ""
			eventID = ""
			return nil
		}
		data := []byte(strings.Join(dataLines, "\n"))
		if err := consume(eventType, eventID, data); err != nil {
			return fmt.Errorf("consume OpenSVC daemon SSE event: %w", err)
		}
		eventType = ""
		eventID = ""
		dataLines = dataLines[:0]
		return nil
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventType = value
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				eventID = value
			}
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read OpenSVC daemon %s SSE response: %w", path, err)
	}
	if reader.remaining == 0 {
		return fmt.Errorf("OpenSVC daemon %s SSE response exceeds %d bytes", path, maxSSEResponseBodySize)
	}
	if err := dispatch(); err != nil {
		return err
	}
	return nil
}

type limitedSSEReader struct {
	reader    io.Reader
	remaining int64
}

func (r *limitedSSEReader) Read(buffer []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	n, err := r.reader.Read(buffer)
	r.remaining -= int64(n)
	return n, err
}
