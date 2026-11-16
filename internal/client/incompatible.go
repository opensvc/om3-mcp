package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ErrIncompatible marks an answer of the daemon this MCP cannot use because
// the API of the daemon is not the one it was built for: a route the daemon
// does not know, a response of another shape, another content type, another
// status. The daemon of a cluster upgraded after the MCP answers so: its
// routes moved on. errors.Is finds it under the errors the tools wrap.
var ErrIncompatible = errors.New("daemon_incompatible")

// IncompatibleError is an answer of the daemon this MCP cannot use, the
// request it answered and what in the answer was not expected.
type IncompatibleError struct {
	Method string
	Path   string
	Reason string
}

func (e *IncompatibleError) Error() string {
	return fmt.Sprintf("OpenSVC daemon %s %s: %s", e.Method, e.Path, e.Reason)
}

func (e *IncompatibleError) Is(target error) bool { return target == ErrIncompatible }

// decodeError is the error of a JSON answer that could not be read: one of
// another shape than the one this MCP reads is incompatible; a body cut short,
// a network failure, stays an error of the request.
func decodeError(method, path string, err error) error {
	var syntax *json.SyntaxError
	var unmarshal *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &unmarshal) {
		return &IncompatibleError{Method: method, Path: path, Reason: "the answer is not in the form this MCP reads: " + err.Error()}
	}
	return fmt.Errorf("decode OpenSVC daemon %s response: %w", path, err)
}

// unexpected is the answer of a status, or a content type, the route does not
// give in the API this MCP was built for.
func unexpected(method, path, reason string) error {
	return &IncompatibleError{Method: method, Path: path, Reason: reason}
}

// responseError is the error of a non-success answer. A 404 or a 405 of the
// router, which answers {"message": ...} and no problem, means the daemon
// knows no such route, or not with this method: its API moved on. A 404 of a
// handler, a problem with its title and detail, or no body at all as the
// instance handler answers, is an object, a node, an instance or a key not
// found, which the tools read as such.
func responseError(method, path string, response *http.Response) error {
	apiError := newAPIError(method, path, response)
	if (apiError.StatusCode == http.StatusNotFound || apiError.StatusCode == http.StatusMethodNotAllowed) && apiError.routed {
		reason := "the daemon knows no such route"
		if apiError.StatusCode == http.StatusMethodNotAllowed {
			reason = "the daemon does not take this method on this route"
		}
		return &IncompatibleError{Method: method, Path: path, Reason: reason}
	}
	return apiError
}
