package core

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	defaultNodeCapabilityLimit     = 100
	maxNodeCapabilityLimit         = 200
	maxNodeCapabilityCursorLength  = 1024
	maxNodeCapabilityNameRunes     = 1024
	maxNodeCapabilityPageTextRunes = 64 << 10
)

type ListNodeCapabilitiesOptions struct {
	Node   string
	Limit  int
	Cursor string
}

type NodeCapabilityList struct {
	Provenance    Provenance `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node          string     `json:"node" jsonschema:"the exact requested OpenSVC node name, reported by every entry"`
	ReportedTotal int        `json:"reported_total" jsonschema:"number of capability entries returned by OpenSVC before exact duplicate removal"`
	Total         int        `json:"total" jsonschema:"number of distinct capability names returned by OpenSVC before MCP pagination"`
	Count         int        `json:"count" jsonschema:"number of distinct capability names returned in this page"`
	Capabilities  []string   `json:"capabilities" jsonschema:"exact capability names sorted lexicographically"`
	NextCursor    string     `json:"next_cursor,omitempty" jsonschema:"exact capability name to pass unchanged for the next page"`
	Truncated     bool       `json:"truncated" jsonschema:"whether distinct capability names remain after this page"`
}

type daemonNodeCapabilityList struct {
	Kind  string                     `json:"kind"`
	Items []daemonNodeCapabilityItem `json:"items"`
}

type daemonNodeCapabilityItem struct {
	Kind string `json:"kind"`
	Meta struct {
		Node string `json:"node"`
	} `json:"meta"`
	Data struct {
		Name string `json:"name"`
	} `json:"data"`
}

func (s *Service) ListNodeCapabilities(ctx context.Context, options ListNodeCapabilitiesOptions) (NodeCapabilityList, error) {
	node, limit, cursor, err := validateNodeCapabilityOptions(options)
	if err != nil {
		return NodeCapabilityList{}, err
	}

	var response daemonNodeCapabilityList
	endpoint := fmt.Sprintf("/api/node/name/%s/capabilities", node)
	if err := s.client.GetJSON(ctx, endpoint, url.Values{}, &response); err != nil {
		return NodeCapabilityList{}, fmt.Errorf("list node capabilities: %w", err)
	}
	if response.Kind != "CapabilityList" {
		return NodeCapabilityList{}, shapeErrorf("list node capabilities: unexpected response kind %q", response.Kind)
	}

	unique := make(map[string]struct{}, len(response.Items))
	for index, item := range response.Items {
		if item.Kind != "CapabilityItem" {
			return NodeCapabilityList{}, shapeErrorf("list node capabilities: item %d has unexpected kind %q", index, item.Kind)
		}
		if !validExactNodeName(item.Meta.Node) {
			return NodeCapabilityList{}, fmt.Errorf("list node capabilities: item %d reports invalid node %q", index, item.Meta.Node)
		}
		if item.Meta.Node != node {
			return NodeCapabilityList{}, fmt.Errorf("list node capabilities: item %d reports unexpected node %q", index, item.Meta.Node)
		}
		name := item.Data.Name
		if name == "" || len([]rune(name)) > maxNodeCapabilityNameRunes || strings.ContainsAny(name, "\r\n\x00") {
			return NodeCapabilityList{}, fmt.Errorf("list node capabilities: item %d has an empty, oversized, or control-containing name", index)
		}
		unique[name] = struct{}{}
	}

	names := make([]string, 0, len(unique))
	for name := range unique {
		names = append(names, name)
	}
	sort.Strings(names)
	start := 0
	if cursor != "" {
		index := sort.SearchStrings(names, cursor)
		if index == len(names) || names[index] != cursor {
			return NodeCapabilityList{}, fmt.Errorf("node capability cursor is no longer present in the result")
		}
		start = index + 1
	}

	items := make([]string, 0, min(limit, len(names)-start))
	textRunes := 0
	end := start
	for end < len(names) && len(items) < limit {
		nameRunes := len([]rune(names[end]))
		if len(items) > 0 && textRunes+nameRunes > maxNodeCapabilityPageTextRunes {
			break
		}
		items = append(items, names[end])
		textRunes += nameRunes
		end++
	}
	if items == nil {
		items = []string{}
	}
	result := NodeCapabilityList{
		Provenance:    s.newProvenance(),
		Node:          node,
		ReportedTotal: len(response.Items),
		Total:         len(names),
		Count:         len(items),
		Capabilities:  items,
		Truncated:     end < len(names),
	}
	if result.Truncated {
		result.NextCursor = items[len(items)-1]
	}
	return result, nil
}

func validateNodeCapabilityOptions(options ListNodeCapabilitiesOptions) (string, int, string, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return "", 0, "", err
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultNodeCapabilityLimit
	}
	if limit < 1 || limit > maxNodeCapabilityLimit {
		return "", 0, "", fmt.Errorf("node capability limit must be between 1 and %d", maxNodeCapabilityLimit)
	}
	if len(options.Cursor) > maxNodeCapabilityCursorLength || strings.ContainsAny(options.Cursor, "\r\n\x00") {
		return "", 0, "", fmt.Errorf("node capability cursor exceeds %d characters or contains control characters", maxNodeCapabilityCursorLength)
	}
	return node, limit, options.Cursor, nil
}

func validExactNodeName(node string) bool {
	return node != "" && node != "." && node != ".." && len(node) <= 255 && node == strings.TrimSpace(node) && strings.IndexFunc(node, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_.-", r))
	}) < 0
}
