package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"
)

const (
	maxErrorResponseBodySize = 64 << 10
	maxErrorTitleRunes       = 256
	maxErrorDetailRunes      = 2048
)

// APIError describes a non-success response from the OpenSVC daemon API.
// StatusCode and Status always come from the HTTP response. Title and Detail
// are optional, bounded RFC 7807 problem fields returned by the daemon.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Status     string
	Title      string
	Detail     string
}

func (e *APIError) Error() string {
	status := e.Status
	if status == "" {
		status = fmt.Sprintf("%d %s", e.StatusCode, http.StatusText(e.StatusCode))
		status = strings.TrimSpace(status)
	}
	message := fmt.Sprintf("OpenSVC daemon %s %s returned HTTP %s", e.Method, e.Path, status)
	if e.Detail != "" {
		return message + ": " + e.Detail
	}
	if e.Title != "" && !strings.EqualFold(e.Title, http.StatusText(e.StatusCode)) {
		return message + ": " + e.Title
	}
	return message
}

func newAPIError(method string, path string, response *http.Response) *APIError {
	apiError := &APIError{
		Method:     method,
		Path:       path,
		StatusCode: response.StatusCode,
		Status:     response.Status,
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorResponseBodySize+1))
	if err != nil || len(body) > maxErrorResponseBodySize {
		return apiError
	}
	var problem struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &problem); err != nil {
		return apiError
	}
	apiError.Title = normalizeProblemText(problem.Title, maxErrorTitleRunes)
	apiError.Detail = normalizeProblemText(problem.Detail, maxErrorDetailRunes)
	return apiError
}

func normalizeProblemText(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

// HTTPStatus gives the status code of the answer, for the use cases that
// read some statuses as an outcome rather than a failure.
func (e *APIError) HTTPStatus() int { return e.StatusCode }

// ProblemDetail gives the bounded detail of the problem the daemon returned.
func (e *APIError) ProblemDetail() string { return e.Detail }

// ProblemTitle gives the bounded title of the problem the daemon returned.
func (e *APIError) ProblemTitle() string { return e.Title }
