package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultNodeHardwareLimit        = 100
	maxNodeHardwareLimit            = 200
	maxNodeHardwareFilters          = 32
	maxNodeHardwareTypeRunes        = 255
	maxNodeHardwareClassRunes       = 1024
	maxNodeHardwareDriverRunes      = 1024
	maxNodeHardwarePathRunes        = 1024
	maxNodeHardwareDescriptionRunes = 4096
	maxNodeHardwarePageTextRunes    = 128 << 10
	maxNodeHardwareItems            = 5000
	maxNodeHardwareCursorRunes      = 128
)

type ListNodeHardwareOptions struct {
	Node    string
	Types   []string
	Classes []string
	Drivers []string
	Limit   int
	Cursor  string
}

type NodeHardwareList struct {
	Provenance    Provenance     `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node          string         `json:"node" jsonschema:"the exact requested OpenSVC node name, reported by every entry"`
	ReportedTotal int            `json:"reported_total" jsonschema:"number of hardware entries returned by OpenSVC before MCP filtering"`
	Total         int            `json:"total" jsonschema:"number of hardware entries matching all requested filter families before pagination"`
	Count         int            `json:"count" jsonschema:"number of hardware entries returned in this page"`
	Hardware      []NodeHardware `json:"hardware" jsonschema:"matching cached hardware entries sorted by type path class driver and description"`
	NextCursor    string         `json:"next_cursor,omitempty" jsonschema:"opaque cursor to pass unchanged for the next page with the same node and filters"`
	Truncated     bool           `json:"truncated" jsonschema:"whether matching hardware entries remain after this page"`
}

type NodeHardware struct {
	Type                 string `json:"type" jsonschema:"exact hardware type reported by OpenSVC such as pci or mem"`
	Class                string `json:"class" jsonschema:"exact hardware class reported by OpenSVC; this is descriptive source data rather than a normalized category"`
	Driver               string `json:"driver" jsonschema:"exact driver reported by OpenSVC; an empty string is preserved and is not a missing-driver verdict"`
	Path                 string `json:"path" jsonschema:"exact hardware path reported by OpenSVC"`
	Description          string `json:"description" jsonschema:"bounded hardware description reported by OpenSVC"`
	DescriptionTruncated bool   `json:"description_truncated" jsonschema:"whether the hardware description exceeded the 4096-rune output limit"`
}

type daemonNodeHardwareList struct {
	Kind  string                   `json:"kind"`
	Items []daemonNodeHardwareItem `json:"items"`
}

type daemonNodeHardwareItem struct {
	Kind string `json:"kind"`
	Meta struct {
		Node string `json:"node"`
	} `json:"meta"`
	Data struct {
		Type        string `json:"type"`
		Class       string `json:"class"`
		Driver      string `json:"driver"`
		Path        string `json:"path"`
		Description string `json:"description"`
	} `json:"data"`
}

type nodeHardwareRecord struct {
	item           NodeHardware
	rawDescription string
	cursor         string
}

func (s *Service) ListNodeHardware(ctx context.Context, options ListNodeHardwareOptions) (NodeHardwareList, error) {
	node, types, classes, drivers, limit, cursor, err := validateNodeHardwareOptions(options)
	if err != nil {
		return NodeHardwareList{}, err
	}

	var response daemonNodeHardwareList
	endpoint := fmt.Sprintf("/api/node/name/%s/system/hardware", node)
	if err := s.client.GetJSON(ctx, endpoint, url.Values{}, &response); err != nil {
		return NodeHardwareList{}, fmt.Errorf("list node hardware: %w", err)
	}
	if response.Kind != "HardwareList" {
		return NodeHardwareList{}, shapeErrorf("list node hardware: unexpected response kind %q", response.Kind)
	}
	if len(response.Items) > maxNodeHardwareItems {
		return NodeHardwareList{}, fmt.Errorf("list node hardware: response contains %d items, limit is %d", len(response.Items), maxNodeHardwareItems)
	}

	records := make([]nodeHardwareRecord, 0, len(response.Items))
	for index, raw := range response.Items {
		if raw.Kind != "HardwareItem" {
			return NodeHardwareList{}, shapeErrorf("list node hardware: item %d has unexpected kind %q", index, raw.Kind)
		}
		if !validExactNodeName(raw.Meta.Node) {
			return NodeHardwareList{}, fmt.Errorf("list node hardware: item %d reports invalid node %q", index, raw.Meta.Node)
		}
		if raw.Meta.Node != node {
			return NodeHardwareList{}, fmt.Errorf("list node hardware: item %d reports unexpected node %q", index, raw.Meta.Node)
		}

		record, err := projectNodeHardware(raw)
		if err != nil {
			return NodeHardwareList{}, fmt.Errorf("list node hardware: item %d: %w", index, err)
		}
		if matchesNodeHardwareFilters(record.item, types, classes, drivers) {
			records = append(records, record)
		}
	}

	sort.SliceStable(records, func(i, j int) bool { return compareNodeHardwareRecords(records[i], records[j]) < 0 })
	occurrences := make(map[string]int, len(records))
	for index := range records {
		fingerprint, err := nodeHardwareFingerprint(records[index])
		if err != nil {
			return NodeHardwareList{}, fmt.Errorf("list node hardware: encode item cursor: %w", err)
		}
		occurrence := occurrences[fingerprint]
		records[index].cursor = fingerprint + "." + strconv.Itoa(occurrence)
		occurrences[fingerprint] = occurrence + 1
	}

	start := 0
	if cursor != "" {
		found := false
		for index := range records {
			if records[index].cursor == cursor {
				start = index + 1
				found = true
				break
			}
		}
		if !found {
			return NodeHardwareList{}, fmt.Errorf("node hardware cursor is no longer present in the filtered result")
		}
	}

	items := make([]NodeHardware, 0, min(limit, len(records)-start))
	textRunes := 0
	end := start
	for end < len(records) && len(items) < limit {
		itemRunes := nodeHardwareTextRunes(records[end].item)
		if len(items) > 0 && textRunes+itemRunes > maxNodeHardwarePageTextRunes {
			break
		}
		items = append(items, records[end].item)
		textRunes += itemRunes
		end++
	}
	if items == nil {
		items = []NodeHardware{}
	}
	result := NodeHardwareList{
		Provenance: s.newProvenance(), Node: node, ReportedTotal: len(response.Items),
		Total: len(records), Count: len(items), Hardware: items, Truncated: end < len(records),
	}
	if result.Truncated {
		result.NextCursor = records[end-1].cursor
	}
	return result, nil
}

func validateNodeHardwareOptions(options ListNodeHardwareOptions) (string, []string, []string, []string, int, string, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return "", nil, nil, nil, 0, "", err
	}
	types, err := normalizeNodeHardwareFilters("type", options.Types, maxNodeHardwareTypeRunes, false)
	if err != nil {
		return "", nil, nil, nil, 0, "", err
	}
	classes, err := normalizeNodeHardwareFilters("class", options.Classes, maxNodeHardwareClassRunes, false)
	if err != nil {
		return "", nil, nil, nil, 0, "", err
	}
	drivers, err := normalizeNodeHardwareFilters("driver", options.Drivers, maxNodeHardwareDriverRunes, true)
	if err != nil {
		return "", nil, nil, nil, 0, "", err
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultNodeHardwareLimit
	}
	if limit < 1 || limit > maxNodeHardwareLimit {
		return "", nil, nil, nil, 0, "", fmt.Errorf("node hardware limit must be between 1 and %d", maxNodeHardwareLimit)
	}
	if len([]rune(options.Cursor)) > maxNodeHardwareCursorRunes || strings.ContainsAny(options.Cursor, "\r\n\x00") {
		return "", nil, nil, nil, 0, "", fmt.Errorf("node hardware cursor exceeds %d characters or contains control characters", maxNodeHardwareCursorRunes)
	}
	return node, types, classes, drivers, limit, options.Cursor, nil
}

func normalizeNodeHardwareFilters(kind string, values []string, maxRunes int, allowEmpty bool) ([]string, error) {
	if len(values) > maxNodeHardwareFilters {
		return nil, fmt.Errorf("node hardware %s filters are limited to %d entries", kind, maxNodeHardwareFilters)
	}
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if (!allowEmpty && value == "") || value != strings.TrimSpace(value) || len([]rune(value)) > maxRunes || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("node hardware %s filters must contain at most %d non-control characters without surrounding whitespace", kind, maxRunes)
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

func projectNodeHardware(raw daemonNodeHardwareItem) (nodeHardwareRecord, error) {
	if err := validateNodeHardwareField("type", raw.Data.Type, maxNodeHardwareTypeRunes, false); err != nil {
		return nodeHardwareRecord{}, err
	}
	if err := validateNodeHardwareField("class", raw.Data.Class, maxNodeHardwareClassRunes, true); err != nil {
		return nodeHardwareRecord{}, err
	}
	if err := validateNodeHardwareField("driver", raw.Data.Driver, maxNodeHardwareDriverRunes, true); err != nil {
		return nodeHardwareRecord{}, err
	}
	if err := validateNodeHardwareField("path", raw.Data.Path, maxNodeHardwarePathRunes, false); err != nil {
		return nodeHardwareRecord{}, err
	}
	if strings.ContainsAny(raw.Data.Description, "\r\n\x00") {
		return nodeHardwareRecord{}, fmt.Errorf("hardware description contains control characters")
	}
	description, truncated := boundedRunes(raw.Data.Description, maxNodeHardwareDescriptionRunes)
	return nodeHardwareRecord{
		item: NodeHardware{
			Type: raw.Data.Type, Class: raw.Data.Class, Driver: raw.Data.Driver, Path: raw.Data.Path,
			Description: description, DescriptionTruncated: truncated,
		},
		rawDescription: raw.Data.Description,
	}, nil
}

func validateNodeHardwareField(field, value string, maxRunes int, allowEmpty bool) error {
	if (!allowEmpty && value == "") || len([]rune(value)) > maxRunes || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("hardware %s is empty, oversized, or contains control characters", field)
	}
	return nil
}

func matchesNodeHardwareFilters(item NodeHardware, types, classes, drivers []string) bool {
	return matchesExactNodeHardwareFilter(item.Type, types) &&
		matchesExactNodeHardwareFilter(item.Class, classes) &&
		matchesExactNodeHardwareFilter(item.Driver, drivers)
}

func matchesExactNodeHardwareFilter(value string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	index := sort.SearchStrings(filters, value)
	return index < len(filters) && filters[index] == value
}

func compareNodeHardwareRecords(left, right nodeHardwareRecord) int {
	leftFields := [...]string{left.item.Type, left.item.Path, left.item.Class, left.item.Driver, left.rawDescription}
	rightFields := [...]string{right.item.Type, right.item.Path, right.item.Class, right.item.Driver, right.rawDescription}
	for index := range leftFields {
		if comparison := strings.Compare(leftFields[index], rightFields[index]); comparison != 0 {
			return comparison
		}
	}
	return 0
}

func nodeHardwareFingerprint(record nodeHardwareRecord) (string, error) {
	payload, err := json.Marshal([5]string{record.item.Type, record.item.Path, record.item.Class, record.item.Driver, record.rawDescription})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func nodeHardwareTextRunes(item NodeHardware) int {
	return len([]rune(item.Type)) + len([]rune(item.Path)) + len([]rune(item.Class)) + len([]rune(item.Driver)) + len([]rune(item.Description))
}
