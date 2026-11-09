package main

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// httpRequestKey holds, in the context of a call, the context of the HTTP
// request that carries it.
type httpRequestKey struct{}

// withRequestContext lets the MCP middleware see the end of the HTTP request
// of a call. The stateless handler of the MCP SDK hides the cancellation of
// the request context from the calls but keeps its values.
func withRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), httpRequestKey{}, r.Context())))
	})
}

// cancelWithRequest cancels a call when its HTTP request ends: the client
// went away, timed out or was cut off. Its daemon and collector requests
// then end with it, instead of running to their bound for nobody.
//
// The two mechanisms share the work: PropagateRequestCancellation, set on
// the handler, cancels the calls of the protocol versions from 2026-07-28 on
// only, and cancelWithRequest the calls of every version, the earlier ones
// included. Together they have no side effect; neither alone covers every
// client, so do not remove one for the other.
func cancelWithRequest(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		requestContext, ok := ctx.Value(httpRequestKey{}).(context.Context)
		if !ok {
			return next(ctx, method, request)
		}
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(requestContext, cancel)
		defer stop()
		return next(ctx, method, request)
	}
}
