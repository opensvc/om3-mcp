package core

import (
	"bytes"
	"encoding/json"
)

// daemonItems is a list the daemon answers, as {kind, items} since om3
// 953065019 (v3.0.0-rc62), as a bare array before it: the paths a selector
// matches and the records of the dns zone. Both decode, a null or missing
// items as none, so that a cluster not yet upgraded keeps its tools.
type daemonItems[T any] []T

func (l *daemonItems[T]) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var items []T
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return err
		}
		*l = items
		return nil
	}
	var list struct {
		Items []T `json:"items"`
	}
	if err := json.Unmarshal(trimmed, &list); err != nil {
		return err
	}
	*l = list.Items
	return nil
}
