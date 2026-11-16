package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// answering serves one answer, its status, content type and body.
func answering(t *testing.T, status int, contentType, body string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", contentType)
		response.WriteHeader(status)
		fmt.Fprint(response, body)
	}))
	t.Cleanup(server.Close)
	apiClient, err := New(server.URL, daemonTestHTTPClient(server.Client(), "test-daemon-token"))
	if err != nil {
		t.Fatal(err)
	}
	return apiClient
}

func TestAnAnswerOfAnotherAPIIsIncompatible(t *testing.T) {
	for name, test := range map[string]struct {
		status      int
		contentType string
		body        string
		reason      string
	}{
		// om3 rc62 answers the paths of a selector as {kind, items}.
		"a response of another shape": {http.StatusOK, "application/json", `{"kind":"ObjectPathList","items":[]}`, "not in the form this MCP reads"},
		"a response not JSON":         {http.StatusOK, "application/json", `<html>`, "not in the form this MCP reads"},
		// The router of the daemon, an unknown route, before any handler.
		"a route the daemon does not know": {http.StatusNotFound, "application/json", `{"message":"Not Found"}`, "knows no such route"},
		"a method the route does not take": {http.StatusMethodNotAllowed, "application/json", `{"message":"Method Not Allowed"}`, "does not take this method"},
	} {
		t.Run(name, func(t *testing.T) {
			var paths []string
			err := answering(t, test.status, test.contentType, test.body).GetJSON(context.Background(), "/api/object/path", nil, &paths)
			if !errors.Is(err, ErrIncompatible) {
				t.Fatalf("got %v, want an incompatible daemon", err)
			}
			if !strings.Contains(err.Error(), "GET /api/object/path") || !strings.Contains(err.Error(), test.reason) {
				t.Errorf("got %q, want the request and %q", err, test.reason)
			}
		})
	}
}

func TestAFailureOfTheDaemonIsNoIncompatibility(t *testing.T) {
	for name, test := range map[string]struct {
		status int
		body   string
	}{
		// A handler: an object not found, the problem of the daemon.
		"a problem not found": {http.StatusNotFound, `{"title":"Not Found","detail":"object root/svc/x not found","status":404}`},
		// The instance handler: an instance absent from the node, no body.
		"a 404 with no body": {http.StatusNotFound, ``},
		// A reverse proxy before the daemon: its own page, no route of the daemon said.
		"a 404 page of a proxy": {http.StatusNotFound, `<html><body>Not Found</body></html>`},
		"a refusal":             {http.StatusForbidden, `{"title":"Forbidden","detail":"missing grant","status":403}`},
		"a server error":        {http.StatusInternalServerError, `{"message":"Internal Server Error"}`},
		// A body cut short: the request failed, not the API.
		"an answer cut short": {http.StatusOK, `["root/svc/a",`},
	} {
		t.Run(name, func(t *testing.T) {
			var paths []string
			err := answering(t, test.status, "application/json", test.body).GetJSON(context.Background(), "/api/object/path", nil, &paths)
			if err == nil || errors.Is(err, ErrIncompatible) {
				t.Fatalf("got %v, want an error of the request, not an incompatible daemon", err)
			}
		})
	}
}

func TestATextOfAnotherContentTypeIsIncompatible(t *testing.T) {
	_, err := answering(t, http.StatusOK, "application/json", `{}`).GetText(context.Background(), "/api/node/name/n1/log", nil)
	if !errors.Is(err, ErrIncompatible) || !strings.Contains(err.Error(), `unexpected content type "application/json"`) {
		t.Fatalf("got %v, want an incompatible daemon naming the content type", err)
	}
}
