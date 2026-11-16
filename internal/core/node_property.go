package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
)

const (
	defaultNodePropertyLimit     = 100
	maxNodePropertyLimit         = 200
	maxNodePropertyFilters       = 32
	maxNodePropertyNameRunes     = 255
	maxNodePropertySourceRunes   = 255
	maxNodePropertyTitleRunes    = 1024
	maxNodePropertyValueRunes    = 4096
	maxNodePropertyErrorRunes    = 4096
	maxNodePropertyPageTextRunes = 64 << 10
	maxNodePropertyItems         = 1000
	maxNodePropertyCursorRunes   = maxNodePropertyNameRunes
	nodePropertyValueTypeString  = "string"
	nodePropertyValueTypeNumber  = "number"
	nodePropertyValueTypeBoolean = "boolean"
)

type ListNodePropertiesOptions struct {
	Node    string
	Names   []string
	Sources []string
	Limit   int
	Cursor  string
}

type NodePropertyList struct {
	Provenance    Provenance     `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node          string         `json:"node" jsonschema:"the exact requested OpenSVC node name, reported by every entry"`
	ReportedTotal int            `json:"reported_total" jsonschema:"number of property entries returned by OpenSVC before MCP filtering"`
	Total         int            `json:"total" jsonschema:"number of properties matching the exact name and source filters before pagination"`
	Count         int            `json:"count" jsonschema:"number of properties returned in this page"`
	Properties    []NodeProperty `json:"properties" jsonschema:"matching cached node properties sorted by exact property name"`
	NextCursor    string         `json:"next_cursor,omitempty" jsonschema:"exact property name to pass unchanged for the next page with the same node and filters"`
	Truncated     bool           `json:"truncated" jsonschema:"whether matching properties remain after this page"`
}

type NodeProperty struct {
	Name           string            `json:"name" jsonschema:"exact OpenSVC node property name"`
	Title          string            `json:"title" jsonschema:"human-readable property title reported by OpenSVC"`
	Source         string            `json:"source" jsonschema:"exact collection source reported by OpenSVC such as probe config or default"`
	Value          NodePropertyValue `json:"value" jsonschema:"typed property value reported by OpenSVC"`
	ValueTruncated bool              `json:"value_truncated" jsonschema:"whether a string property value exceeded the 4096-rune output limit"`
	Error          string            `json:"error" jsonschema:"per-property collection error reported by OpenSVC; an empty string means no error was reported"`
	ErrorTruncated bool              `json:"error_truncated" jsonschema:"whether the per-property collection error exceeded the 4096-rune output limit"`
}

type NodePropertyValue struct {
	Type    string   `json:"type" jsonschema:"daemon value type; exactly string number or boolean"`
	String  *string  `json:"string,omitempty" jsonschema:"property value when type is string; empty strings are preserved"`
	Number  *float64 `json:"number,omitempty" jsonschema:"property value when type is number"`
	Boolean *bool    `json:"boolean,omitempty" jsonschema:"property value when type is boolean"`
}

type daemonNodePropertyList struct {
	Kind  string                   `json:"kind"`
	Items []daemonNodePropertyItem `json:"items"`
}

type daemonNodePropertyItem struct {
	Kind string `json:"kind"`
	Meta struct {
		Node string `json:"node"`
	} `json:"meta"`
	Data struct {
		Name   string          `json:"name"`
		Title  string          `json:"title"`
		Source string          `json:"source"`
		Value  json.RawMessage `json:"value"`
		Error  string          `json:"error"`
	} `json:"data"`
}

func (s *Service) ListNodeProperties(ctx context.Context, options ListNodePropertiesOptions) (NodePropertyList, error) {
	node, names, sources, limit, cursor, err := validateNodePropertyOptions(options)
	if err != nil {
		return NodePropertyList{}, err
	}

	var response daemonNodePropertyList
	endpoint := fmt.Sprintf("/api/node/name/%s/system/property", node)
	if err := s.client.GetJSON(ctx, endpoint, url.Values{}, &response); err != nil {
		return NodePropertyList{}, fmt.Errorf("list node properties: %w", err)
	}
	if response.Kind != "PropertyList" {
		return NodePropertyList{}, shapeErrorf("list node properties: unexpected response kind %q", response.Kind)
	}
	if len(response.Items) > maxNodePropertyItems {
		return NodePropertyList{}, fmt.Errorf("list node properties: response contains %d items, limit is %d", len(response.Items), maxNodePropertyItems)
	}

	seenNames := make(map[string]struct{}, len(response.Items))
	properties := make([]NodeProperty, 0, len(response.Items))
	for index, raw := range response.Items {
		if raw.Kind != "PropertyItem" {
			return NodePropertyList{}, shapeErrorf("list node properties: item %d has unexpected kind %q", index, raw.Kind)
		}
		if !validExactNodeName(raw.Meta.Node) {
			return NodePropertyList{}, fmt.Errorf("list node properties: item %d reports invalid node %q", index, raw.Meta.Node)
		}
		if raw.Meta.Node != node {
			return NodePropertyList{}, fmt.Errorf("list node properties: item %d reports unexpected node %q", index, raw.Meta.Node)
		}

		property, err := projectNodeProperty(raw)
		if err != nil {
			return NodePropertyList{}, fmt.Errorf("list node properties: item %d: %w", index, err)
		}
		if _, ok := seenNames[property.Name]; ok {
			return NodePropertyList{}, fmt.Errorf("list node properties: duplicate property name %q", property.Name)
		}
		seenNames[property.Name] = struct{}{}
		if matchesNodePropertyFilters(property, names, sources) {
			properties = append(properties, property)
		}
	}

	sort.Slice(properties, func(i, j int) bool { return properties[i].Name < properties[j].Name })
	start := 0
	if cursor != "" {
		index := sort.Search(len(properties), func(index int) bool { return properties[index].Name >= cursor })
		if index == len(properties) || properties[index].Name != cursor {
			return NodePropertyList{}, fmt.Errorf("node property cursor is no longer present in the filtered result")
		}
		start = index + 1
	}

	items := make([]NodeProperty, 0, min(limit, len(properties)-start))
	textRunes := 0
	end := start
	for end < len(properties) && len(items) < limit {
		itemRunes := nodePropertyTextRunes(properties[end])
		if len(items) > 0 && textRunes+itemRunes > maxNodePropertyPageTextRunes {
			break
		}
		items = append(items, properties[end])
		textRunes += itemRunes
		end++
	}
	if items == nil {
		items = []NodeProperty{}
	}
	result := NodePropertyList{
		Provenance:    s.newProvenance(),
		Node:          node,
		ReportedTotal: len(response.Items),
		Total:         len(properties),
		Count:         len(items),
		Properties:    items,
		Truncated:     end < len(properties),
	}
	if result.Truncated {
		result.NextCursor = items[len(items)-1].Name
	}
	return result, nil
}

func validateNodePropertyOptions(options ListNodePropertiesOptions) (string, []string, []string, int, string, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return "", nil, nil, 0, "", err
	}
	names, err := normalizeNodePropertyFilters("name", options.Names, maxNodePropertyNameRunes)
	if err != nil {
		return "", nil, nil, 0, "", err
	}
	sources, err := normalizeNodePropertyFilters("source", options.Sources, maxNodePropertySourceRunes)
	if err != nil {
		return "", nil, nil, 0, "", err
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultNodePropertyLimit
	}
	if limit < 1 || limit > maxNodePropertyLimit {
		return "", nil, nil, 0, "", fmt.Errorf("node property limit must be between 1 and %d", maxNodePropertyLimit)
	}
	if len([]rune(options.Cursor)) > maxNodePropertyCursorRunes || strings.ContainsAny(options.Cursor, "\r\n\x00") {
		return "", nil, nil, 0, "", fmt.Errorf("node property cursor exceeds %d characters or contains control characters", maxNodePropertyCursorRunes)
	}
	return node, names, sources, limit, options.Cursor, nil
}

func normalizeNodePropertyFilters(kind string, values []string, maxRunes int) ([]string, error) {
	if len(values) > maxNodePropertyFilters {
		return nil, fmt.Errorf("node property %s filters are limited to %d entries", kind, maxNodePropertyFilters)
	}
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || len([]rune(value)) > maxRunes || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("node property %s filters must contain 1 to %d non-control characters without surrounding whitespace", kind, maxRunes)
		}
		unique[value] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func matchesNodePropertyFilters(property NodeProperty, names, sources []string) bool {
	if len(names) > 0 {
		index := sort.SearchStrings(names, property.Name)
		if index == len(names) || names[index] != property.Name {
			return false
		}
	}
	if len(sources) > 0 {
		index := sort.SearchStrings(sources, property.Source)
		if index == len(sources) || sources[index] != property.Source {
			return false
		}
	}
	return true
}

func projectNodeProperty(raw daemonNodePropertyItem) (NodeProperty, error) {
	if err := validateNodePropertyIdentity("name", raw.Data.Name, maxNodePropertyNameRunes, false); err != nil {
		return NodeProperty{}, err
	}
	if err := validateNodePropertyIdentity("title", raw.Data.Title, maxNodePropertyTitleRunes, true); err != nil {
		return NodeProperty{}, err
	}
	if err := validateNodePropertyIdentity("source", raw.Data.Source, maxNodePropertySourceRunes, false); err != nil {
		return NodeProperty{}, err
	}
	value, truncated, err := projectNodePropertyValue(raw.Data.Value)
	if err != nil {
		return NodeProperty{}, err
	}
	errorText, errorTruncated := boundedRunes(raw.Data.Error, maxNodePropertyErrorRunes)
	return NodeProperty{
		Name: raw.Data.Name, Title: raw.Data.Title, Source: raw.Data.Source,
		Value: value, ValueTruncated: truncated, Error: errorText, ErrorTruncated: errorTruncated,
	}, nil
}

func validateNodePropertyIdentity(field, value string, maxRunes int, allowEmpty bool) error {
	if (!allowEmpty && value == "") || len([]rune(value)) > maxRunes || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("property %s is empty, oversized, or contains control characters", field)
	}
	return nil
}

func projectNodePropertyValue(raw json.RawMessage) (NodePropertyValue, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return NodePropertyValue{}, false, fmt.Errorf("property value is invalid JSON: %w", err)
	}
	switch typed := value.(type) {
	case string:
		bounded, truncated := boundedRunes(typed, maxNodePropertyValueRunes)
		return NodePropertyValue{Type: nodePropertyValueTypeString, String: &bounded}, truncated, nil
	case json.Number:
		number, err := typed.Float64()
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return NodePropertyValue{}, false, fmt.Errorf("property number is invalid or outside the supported range")
		}
		return NodePropertyValue{Type: nodePropertyValueTypeNumber, Number: &number}, false, nil
	case bool:
		return NodePropertyValue{Type: nodePropertyValueTypeBoolean, Boolean: &typed}, false, nil
	default:
		return NodePropertyValue{}, false, fmt.Errorf("property value must be a string, number, or boolean")
	}
}

func nodePropertyTextRunes(property NodeProperty) int {
	total := len([]rune(property.Name)) + len([]rune(property.Title)) + len([]rune(property.Source)) + len([]rune(property.Error))
	if property.Value.String != nil {
		total += len([]rune(*property.Value.String))
	}
	return total
}
