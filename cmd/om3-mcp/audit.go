package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/om3-mcp/internal/auth"
	"github.com/opensvc/om3-mcp/internal/content"
	"github.com/opensvc/osvc-auth-mcp/mcpauth"
)

const maxAuditValueBytes = 256

// auditLogger receives one credential-free line per tool call. Tests replace it.
var auditLogger = slog.Default()

// auditToolCalls traces who called which tool on which cluster, through which
// client application, and the exchange and call outcomes. It never logs tokens,
// secrets, raw arguments or tool results.
func auditToolCalls(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		call, ok := request.(*mcp.CallToolRequest)
		if method != "tools/call" || !ok || call.Params == nil {
			return next(ctx, method, request)
		}
		identity, _, _ := mcpauth.OAuthFromContext(ctx)
		// On the Unix socket, the cluster comes from the header and the daemon
		// receives the caller's token unchanged. The middleware verified its
		// claims before the call: a refused token never reaches this line. It
		// is read when the call is admitted: the token may expire, or the call
		// be cancelled, before the call ends, and the line must still name its
		// caller.
		delegation, _, delegated := auth.DelegationFromContext(ctx)
		ctx, trace := mcpauth.WithCallTrace(ctx)
		start := time.Now()
		result, err := next(ctx, method, request)

		var target struct {
			ClusterID string `json:"cluster_id"`
		}
		_ = json.Unmarshal(call.Params.Arguments, &target)
		exchange, daemonSubject := trace.Exchange, trace.TargetSubject
		if exchange == "" {
			exchange = "none"
		}
		if delegated {
			identity.Issuer, identity.Subject = delegation.Issuer, delegation.Subject
			target.ClusterID, exchange, daemonSubject = delegation.ClusterID, "delegated", delegation.Subject
		}
		outcome, detail := "ok", ""
		if err != nil {
			outcome, detail = "error", err.Error()
		} else if r, ok := result.(*mcp.CallToolResult); ok && r.IsError {
			outcome = "tool_error"
			for _, content := range r.Content {
				if text, ok := content.(*mcp.TextContent); ok {
					detail = text.Text
					break
				}
			}
		}
		attrs := []slog.Attr{
			slog.String("tool", auditValue(call.Params.Name)),
			slog.String("cluster_id", auditValue(target.ClusterID)),
			slog.String("issuer", identity.Issuer),
			slog.String("subject", identity.Subject),
			slog.String("client_id", identity.ClientID),
			slog.String("exchange", auditValue(exchange)),
			slog.String("daemon_subject", daemonSubject),
			slog.String("outcome", outcome),
			slog.Duration("duration", time.Since(start).Round(time.Millisecond)),
		}
		if detail != "" {
			attrs = append(attrs, slog.String("detail", auditValue(detail)))
		}
		// Only the Unix socket keeps the request ID of the agent: it stays
		// empty on the HTTPS listener.
		attrs = append(attrs, slog.String("request_id", requestIDFromContext(ctx)))
		auditLogger.LogAttrs(context.Background(), slog.LevelInfo, "mcp tool call", attrs...)
		return result, err
	}
}

// auditContentReads traces each read of the resources and the skills of the
// collector, a content method: who read, through which client application,
// on which cluster for the socket, the exchange of the token for a collector
// token and its outcome. An operator reads there why an exchange fails, such
// as invalid_grant for the provider of a cluster not federated in the
// exchange client. It never logs tokens, secrets, parameters or results.
func auditContentReads(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		if !slices.Contains(content.Methods, method) {
			return next(ctx, method, request)
		}
		identity, _, _ := mcpauth.OAuthFromContext(ctx)
		clusterID := ""
		if delegation, _, ok := auth.DelegationFromContext(ctx); ok {
			identity.Issuer, identity.Subject, clusterID = delegation.Issuer, delegation.Subject, delegation.ClusterID
		}
		ctx, trace := mcpauth.WithCallTrace(ctx)
		start := time.Now()
		result, err := next(ctx, method, request)
		exchange := trace.Exchange
		if exchange == "" {
			exchange = "none"
		}
		outcome := "ok"
		attrs := []slog.Attr{
			slog.String("method", method),
			slog.String("cluster_id", auditValue(clusterID)),
			slog.String("issuer", identity.Issuer),
			slog.String("subject", identity.Subject),
			slog.String("client_id", identity.ClientID),
			slog.String("exchange", auditValue(exchange)),
			slog.String("collector_subject", trace.TargetSubject),
		}
		if err != nil {
			outcome = "error"
		}
		attrs = append(attrs, slog.String("outcome", outcome), slog.Duration("duration", time.Since(start).Round(time.Millisecond)))
		if err != nil {
			attrs = append(attrs, slog.String("detail", auditValue(err.Error())))
		}
		attrs = append(attrs, slog.String("request_id", requestIDFromContext(ctx)))
		auditLogger.LogAttrs(context.Background(), slog.LevelInfo, "mcp content read", attrs...)
		return result, err
	}
}

// auditWhoAmI records one identity check of the whoami bridge on the Unix
// socket: the verified claims of the token, the cluster whose daemon checked
// it, what the bridge answered and why.
func auditWhoAmI(ctx context.Context, delegation auth.Delegation, outcome string, status int, started time.Time, extra ...slog.Attr) {
	attrs := []slog.Attr{
		slog.String("cluster_id", auditValue(delegation.ClusterID)),
		slog.String("issuer", delegation.Issuer),
		slog.String("subject", delegation.Subject),
		slog.String("outcome", outcome),
		slog.Int("status", status),
		slog.Duration("duration", time.Since(started).Round(time.Millisecond)),
	}
	attrs = append(attrs, extra...)
	attrs = append(attrs, slog.String("request_id", requestIDFromContext(ctx)))
	auditLogger.LogAttrs(context.Background(), slog.LevelInfo, "mcp whoami", attrs...)
}

func auditValue(s string) string {
	if len(s) > maxAuditValueBytes {
		return s[:maxAuditValueBytes] + "..."
	}
	return s
}
