package tools

import (
	"errors"
	"fmt"

	"github.com/opensvc/om3-mcp/internal/client"
)

// serverVersion is the version of this MCP, which a refusal of a daemon it
// cannot use names: the one to upgrade.
var serverVersion = "unknown"

// SetServerVersion sets the version the refusals of an incompatible daemon name.
func SetServerVersion(version string) {
	if version != "" {
		serverVersion = version
	}
}

// explainIncompatible tells the model, in the result of a tool, that the
// daemon of the cluster answered in a way this MCP cannot use: its API moved
// on, a route gone or a response of another shape. On 2026-11-13, the daemon
// answering a list as {kind, items} since om3 rc62, list_cluster_objects
// failed on a decode error the model read as a passing failure, and it
// answered from other tools as if there were no object.
func explainIncompatible(tool string, err error) error {
	if err == nil || !errors.Is(err, client.ErrIncompatible) {
		return err
	}
	return &incompatibleToolError{tool: tool, version: serverVersion, err: err}
}

// incompatibleToolError is the result a tool gives the model when the daemon
// answered in a way this MCP cannot use: what to tell the user, and what not
// to infer. Its text is sentences, ending as sentences do.
type incompatibleToolError struct {
	tool    string
	version string
	err     error
}

func (e *incompatibleToolError) Error() string {
	return fmt.Sprintf("daemon_incompatible: the tool %s cannot be used on this cluster: its OpenSVC daemon answered in a way om3-mcp %s does not know (%v). "+
		"The API of the daemon changed since this MCP was built; om3-mcp must be upgraded to follow it. "+
		"Tell the user that %s is unusable on this cluster, and why. "+
		"Do not read this as an empty or a missing result, and do not guess its answer from other tools.",
		e.tool, e.version, e.err, e.tool)
}

func (e *incompatibleToolError) Unwrap() error { return e.err }
