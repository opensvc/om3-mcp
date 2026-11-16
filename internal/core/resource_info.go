package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	ResourceInfoScopeObject   = "object"
	ResourceInfoScopeInstance = "instance"

	defaultResourceInfoLimit     = 100
	maxResourceInfoLimit         = 200
	maxResourceInfoItems         = 10000
	maxResourceInfoFilterRunes   = 255
	maxResourceInfoIdentityRunes = 255
	maxResourceInfoRawValueRunes = 1 << 20
	maxResourceInfoValueRunes    = 4096
	maxResourceInfoPageRunes     = 128 << 10
	maxResourceInfoCursorRunes   = 64
)

type ListResourceInfoOptions struct {
	Scope  string
	Path   string
	Node   string
	RID    string
	Key    string
	Limit  int
	Cursor string
}

type ResourceInfoList struct {
	Provenance      Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result; this is not the resource information cache refresh time"`
	Scope           string                 `json:"scope" jsonschema:"exact requested resource information scope: object or instance"`
	Object          ClusterObjectReference `json:"object" jsonschema:"exact svc or vol object whose cached resource information was requested"`
	Node            string                 `json:"node,omitempty" jsonschema:"exact node for instance scope; omitted for object scope"`
	Filters         ResourceInfoFilters    `json:"filters" jsonschema:"exact local filters applied by the MCP after reading the daemon response"`
	ReportedTotal   int                    `json:"reported_total" jsonschema:"number of entries returned by OpenSVC before MCP filtering and pagination"`
	Total           int                    `json:"total" jsonschema:"number of entries remaining after exact MCP filters and before pagination"`
	Count           int                    `json:"count" jsonschema:"number of entries returned in this page"`
	Entries         []ResourceInfoEntry    `json:"entries" jsonschema:"cached resource information entries sorted by node, object, resource id, key, and raw value"`
	ValuesTruncated int                    `json:"values_truncated" jsonschema:"number of values truncated to the per-value bound in this page"`
	NextCursor      string                 `json:"next_cursor,omitempty" jsonschema:"opaque cursor to pass unchanged for the next page with the same scope, object, node, and filters"`
	Truncated       bool                   `json:"truncated" jsonschema:"whether entries remain after this page; independent from values_truncated"`
}

type ResourceInfoFilters struct {
	RID string `json:"rid,omitempty" jsonschema:"exact resource id filter applied locally; empty when no resource filter was requested"`
	Key string `json:"key,omitempty" jsonschema:"exact information key filter applied locally; empty when no key filter was requested"`
}

type ResourceInfoEntry struct {
	Node           string `json:"node" jsonschema:"node owning the cached resource information"`
	Object         string `json:"object" jsonschema:"canonical OpenSVC object path reported by the daemon"`
	RID            string `json:"rid" jsonschema:"exact OpenSVC resource identifier"`
	Key            string `json:"key" jsonschema:"resource information key reported by OpenSVC"`
	Value          string `json:"value" jsonschema:"raw string value reported by OpenSVC, bounded to 4096 Unicode code points without type coercion"`
	ValueTruncated bool   `json:"value_truncated" jsonschema:"whether value was truncated by the MCP to its per-value bound"`
}

type daemonResourceInfoList struct {
	Kind  string                   `json:"kind"`
	Items []daemonResourceInfoItem `json:"items"`
}

type daemonResourceInfoItem struct {
	Node   string  `json:"node"`
	Object string  `json:"object"`
	RID    string  `json:"rid"`
	Key    string  `json:"key"`
	Value  *string `json:"value"`
}

type resourceInfoTarget struct {
	scope   string
	node    string
	object  ClusterObjectReference
	filters ResourceInfoFilters
}

type resourceInfoRecord struct {
	entry  ResourceInfoEntry
	cursor string
}

func (s *Service) ListResourceInfo(ctx context.Context, options ListResourceInfoOptions) (ResourceInfoList, error) {
	target, limit, cursor, err := validateResourceInfoOptions(options)
	if err != nil {
		return ResourceInfoList{}, err
	}

	var response daemonResourceInfoList
	if err := s.client.GetJSON(ctx, resourceInfoEndpoint(target), url.Values{}, &response); err != nil {
		return ResourceInfoList{}, fmt.Errorf("list resource info: %w", err)
	}
	if response.Kind != "ResourceInfoList" {
		return ResourceInfoList{}, shapeErrorf("list resource info: unexpected response kind %q", response.Kind)
	}
	if response.Items == nil {
		return ResourceInfoList{}, fmt.Errorf("list resource info: response items must be an array")
	}
	if len(response.Items) > maxResourceInfoItems {
		return ResourceInfoList{}, fmt.Errorf("list resource info: response contains %d items, limit is %d", len(response.Items), maxResourceInfoItems)
	}

	records := make([]resourceInfoRecord, 0, len(response.Items))
	for index, raw := range response.Items {
		entry, err := projectResourceInfoEntry(raw, target)
		if err != nil {
			return ResourceInfoList{}, fmt.Errorf("list resource info: item %d: %w", index, err)
		}
		if target.filters.RID != "" && entry.RID != target.filters.RID {
			continue
		}
		if target.filters.Key != "" && entry.Key != target.filters.Key {
			continue
		}
		records = append(records, resourceInfoRecord{entry: entry})
	}

	sort.SliceStable(records, func(i, j int) bool {
		return compareResourceInfoEntries(records[i].entry, records[j].entry) < 0
	})
	occurrences := make(map[string]int, len(records))
	for index := range records {
		fingerprint := resourceInfoRecordFingerprint(target, records[index].entry)
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
			return ResourceInfoList{}, fmt.Errorf("resource info cursor is no longer present for this scope and filters")
		}
	}

	entries := make([]ResourceInfoEntry, 0, min(limit, len(records)-start))
	pageRunes := 0
	valuesTruncated := 0
	end := start
	for end < len(records) && len(entries) < limit {
		entry := records[end].entry
		entry.Value, entry.ValueTruncated = boundResourceInfoValue(entry.Value)
		entryRunes := resourceInfoEntryRunes(entry)
		if len(entries) > 0 && pageRunes+entryRunes > maxResourceInfoPageRunes {
			break
		}
		if entry.ValueTruncated {
			valuesTruncated++
		}
		entries = append(entries, entry)
		pageRunes += entryRunes
		end++
	}

	result := ResourceInfoList{
		Provenance:      s.newProvenance(),
		Scope:           target.scope,
		Object:          target.object,
		Node:            target.node,
		Filters:         target.filters,
		ReportedTotal:   len(response.Items),
		Total:           len(records),
		Count:           len(entries),
		Entries:         entries,
		ValuesTruncated: valuesTruncated,
		Truncated:       end < len(records),
	}
	if target.scope == ResourceInfoScopeObject {
		result.Node = ""
	}
	if result.Truncated {
		result.NextCursor = records[end-1].cursor
	}
	return result, nil
}

func validateResourceInfoOptions(options ListResourceInfoOptions) (resourceInfoTarget, int, string, error) {
	reference, err := validateExactObjectPath(options.Path)
	if err != nil {
		return resourceInfoTarget{}, 0, "", err
	}
	if reference.Kind != "svc" && reference.Kind != "vol" {
		return resourceInfoTarget{}, 0, "", fmt.Errorf("resource info object kind must be svc or vol")
	}
	target := resourceInfoTarget{
		scope:   options.Scope,
		node:    options.Node,
		object:  reference,
		filters: ResourceInfoFilters{RID: options.RID, Key: options.Key},
	}
	switch target.scope {
	case ResourceInfoScopeObject:
		if target.node != "" {
			return resourceInfoTarget{}, 0, "", fmt.Errorf("resource info node must be empty for object scope")
		}
	case ResourceInfoScopeInstance:
		if err := validateNodeTarget(target.node); err != nil {
			return resourceInfoTarget{}, 0, "", err
		}
	default:
		return resourceInfoTarget{}, 0, "", fmt.Errorf("resource info scope must be exactly object or instance")
	}
	for name, value := range map[string]string{"rid": target.filters.RID, "key": target.filters.Key} {
		if value != "" && (value != strings.TrimSpace(value) || len([]rune(value)) > maxResourceInfoFilterRunes || containsControl(value)) {
			return resourceInfoTarget{}, 0, "", fmt.Errorf("resource info %s filter must contain at most %d non-control characters without surrounding whitespace", name, maxResourceInfoFilterRunes)
		}
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultResourceInfoLimit
	}
	if limit < 1 || limit > maxResourceInfoLimit {
		return resourceInfoTarget{}, 0, "", fmt.Errorf("resource info limit must be between 1 and %d", maxResourceInfoLimit)
	}
	if err := validateResourceInfoCursor(options.Cursor); err != nil {
		return resourceInfoTarget{}, 0, "", err
	}
	return target, limit, options.Cursor, nil
}

func resourceInfoEndpoint(target resourceInfoTarget) string {
	if target.scope == ResourceInfoScopeObject {
		return fmt.Sprintf("/api/object/path/%s/%s/%s/resource/info", target.object.Namespace, target.object.Kind, target.object.Name)
	}
	return fmt.Sprintf("/api/node/name/%s/instance/path/%s/%s/%s/resource/info", target.node, target.object.Namespace, target.object.Kind, target.object.Name)
}

func projectResourceInfoEntry(raw daemonResourceInfoItem, target resourceInfoTarget) (ResourceInfoEntry, error) {
	if !validExactNodeName(raw.Node) {
		return ResourceInfoEntry{}, fmt.Errorf("invalid node %q", raw.Node)
	}
	if target.scope == ResourceInfoScopeInstance && raw.Node != target.node {
		return ResourceInfoEntry{}, fmt.Errorf("unexpected node %q", raw.Node)
	}
	reference, err := validateExactObjectPath(raw.Object)
	if err != nil || reference.Path != raw.Object {
		return ResourceInfoEntry{}, fmt.Errorf("invalid object path %q", raw.Object)
	}
	if raw.Object != target.object.Path {
		return ResourceInfoEntry{}, fmt.Errorf("unexpected object path %q", raw.Object)
	}
	for name, value := range map[string]string{"rid": raw.RID, "key": raw.Key} {
		if value == "" || value != strings.TrimSpace(value) || len([]rune(value)) > maxResourceInfoIdentityRunes || containsControl(value) {
			return ResourceInfoEntry{}, fmt.Errorf("%s is empty, oversized, contains control characters, or has surrounding whitespace", name)
		}
	}
	if raw.Value == nil {
		return ResourceInfoEntry{}, fmt.Errorf("value is missing or null")
	}
	value := *raw.Value
	if len([]rune(value)) > maxResourceInfoRawValueRunes {
		return ResourceInfoEntry{}, fmt.Errorf("value exceeds %d Unicode code points", maxResourceInfoRawValueRunes)
	}
	if containsControl(value) {
		return ResourceInfoEntry{}, fmt.Errorf("value contains control characters")
	}
	return ResourceInfoEntry{Node: raw.Node, Object: raw.Object, RID: raw.RID, Key: raw.Key, Value: value}, nil
}

func compareResourceInfoEntries(left, right ResourceInfoEntry) int {
	leftFields := [...]string{left.Node, left.Object, left.RID, left.Key, left.Value}
	rightFields := [...]string{right.Node, right.Object, right.RID, right.Key, right.Value}
	for index := range leftFields {
		if comparison := strings.Compare(leftFields[index], rightFields[index]); comparison != 0 {
			return comparison
		}
	}
	return 0
}

func resourceInfoRecordFingerprint(target resourceInfoTarget, entry ResourceInfoEntry) string {
	payload := strings.Join([]string{
		target.scope, target.node, target.object.Path, target.filters.RID, target.filters.Key,
		entry.Node, entry.Object, entry.RID, entry.Key, entry.Value,
	}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func validateResourceInfoCursor(cursor string) error {
	if cursor == "" {
		return nil
	}
	if len([]rune(cursor)) > maxResourceInfoCursorRunes || containsControl(cursor) {
		return fmt.Errorf("resource info cursor exceeds %d characters or contains control characters", maxResourceInfoCursorRunes)
	}
	fingerprint, occurrence, ok := strings.Cut(cursor, ".")
	decoded, err := base64.RawURLEncoding.DecodeString(fingerprint)
	if !ok || err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid resource info cursor")
	}
	index, err := strconv.Atoi(occurrence)
	if err != nil || index < 0 || strconv.Itoa(index) != occurrence {
		return fmt.Errorf("invalid resource info cursor")
	}
	return nil
}

func boundResourceInfoValue(value string) (string, bool) {
	runes := []rune(value)
	if len(runes) <= maxResourceInfoValueRunes {
		return value, false
	}
	return string(runes[:maxResourceInfoValueRunes-1]) + "…", true
}

func resourceInfoEntryRunes(entry ResourceInfoEntry) int {
	return len([]rune(entry.Node)) + len([]rune(entry.Object)) + len([]rune(entry.RID)) + len([]rune(entry.Key)) + len([]rune(entry.Value))
}
