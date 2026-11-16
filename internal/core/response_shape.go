package core

import (
	"fmt"

	"github.com/opensvc/om3-mcp/internal/client"
)

// shapeError is a daemon answer of another API: a list, or an item of it,
// of another kind than the one this MCP reads. errors.Is finds it as
// client.ErrIncompatible: the tool tells the model the daemon moved on,
// rather than a passing failure. An item that names another node or object,
// or a value out of its bounds, is no answer of another API: those stay
// errors of the request.
type shapeError struct{ message string }

func (e *shapeError) Error() string { return e.message }

func (e *shapeError) Is(target error) bool { return target == client.ErrIncompatible }

// shapeErrorf is the error of an answer of another API, said as given.
func shapeErrorf(format string, args ...any) error {
	return &shapeError{message: fmt.Sprintf(format, args...)}
}
