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
	"time"
)

const (
	defaultNodePackageLimit         = 100
	maxNodePackageLimit             = 200
	maxNodePackageFilters           = 32
	maxNodePackageNameRunes         = 255
	maxNodePackageVersionRunes      = 1024
	maxNodePackageArchitectureRunes = 255
	maxNodePackageTypeRunes         = 255
	maxNodePackageTimestampRunes    = 64
	maxNodePackageSignatureRunes    = 1024
	maxNodePackagePageTextRunes     = 128 << 10
	maxNodePackageItems             = 20000
	maxNodePackageCursorRunes       = 128
)

type ListNodePackagesOptions struct {
	Node          string
	Names         []string
	NamePrefixes  []string
	Types         []string
	Architectures []string
	Limit         int
	Cursor        string
}

type NodePackageList struct {
	Provenance    Provenance    `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node          string        `json:"node" jsonschema:"the exact requested OpenSVC node name, reported by every entry"`
	ReportedTotal int           `json:"reported_total" jsonschema:"number of package entries returned by OpenSVC before MCP filtering"`
	Total         int           `json:"total" jsonschema:"number of package entries matching all requested filter families before pagination"`
	Count         int           `json:"count" jsonschema:"number of package entries returned in this page"`
	Packages      []NodePackage `json:"packages" jsonschema:"matching cached package entries sorted by name architecture type version installation timestamp and signature"`
	NextCursor    string        `json:"next_cursor,omitempty" jsonschema:"opaque cursor to pass unchanged for the next page with the same node and filters"`
	Truncated     bool          `json:"truncated" jsonschema:"whether matching package entries remain after this page"`
}

type NodePackage struct {
	Name         string `json:"name" jsonschema:"exact package name reported by OpenSVC"`
	Version      string `json:"version" jsonschema:"exact package version reported by OpenSVC without comparison or normalization; an empty source value is preserved"`
	Architecture string `json:"architecture" jsonschema:"exact package architecture reported by OpenSVC; an empty source value is preserved"`
	Type         string `json:"type" jsonschema:"exact package manager type reported by OpenSVC such as deb rpm or snap; an empty source value is preserved"`
	InstalledAt  string `json:"installed_at" jsonschema:"installation timestamp reported by OpenSVC; the all-zero timestamp can mean the package collector did not supply one"`
	Signature    string `json:"signature" jsonschema:"exact package signature field reported by OpenSVC; an empty value is preserved and is not a trust verdict"`
}

type daemonNodePackageList struct {
	Kind  string                  `json:"kind"`
	Items []daemonNodePackageItem `json:"items"`
}

type daemonNodePackageItem struct {
	Kind string `json:"kind"`
	Meta struct {
		Node string `json:"node"`
	} `json:"meta"`
	Data struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Arch        string `json:"arch"`
		Type        string `json:"type"`
		InstalledAt string `json:"installedat"`
		Signature   string `json:"sig"`
	} `json:"data"`
}

type nodePackageRecord struct {
	item   NodePackage
	cursor string
}

func (s *Service) ListNodePackages(ctx context.Context, options ListNodePackagesOptions) (NodePackageList, error) {
	node, names, prefixes, types, architectures, limit, cursor, err := validateNodePackageOptions(options)
	if err != nil {
		return NodePackageList{}, err
	}

	var response daemonNodePackageList
	endpoint := fmt.Sprintf("/api/node/name/%s/system/package", node)
	if err := s.client.GetJSON(ctx, endpoint, url.Values{}, &response); err != nil {
		return NodePackageList{}, fmt.Errorf("list node packages: %w", err)
	}
	if response.Kind != "PackageList" {
		return NodePackageList{}, shapeErrorf("list node packages: unexpected response kind %q", response.Kind)
	}
	if len(response.Items) > maxNodePackageItems {
		return NodePackageList{}, fmt.Errorf("list node packages: response contains %d items, limit is %d", len(response.Items), maxNodePackageItems)
	}

	records := make([]nodePackageRecord, 0, len(response.Items))
	for index, raw := range response.Items {
		if raw.Kind != "PackageItem" {
			return NodePackageList{}, shapeErrorf("list node packages: item %d has unexpected kind %q", index, raw.Kind)
		}
		if !validExactNodeName(raw.Meta.Node) {
			return NodePackageList{}, fmt.Errorf("list node packages: item %d reports invalid node %q", index, raw.Meta.Node)
		}
		if raw.Meta.Node != node {
			return NodePackageList{}, fmt.Errorf("list node packages: item %d reports unexpected node %q", index, raw.Meta.Node)
		}

		record, err := projectNodePackage(raw)
		if err != nil {
			return NodePackageList{}, fmt.Errorf("list node packages: item %d: %w", index, err)
		}
		if matchesNodePackageFilters(record.item, names, prefixes, types, architectures) {
			records = append(records, record)
		}
	}

	sort.SliceStable(records, func(i, j int) bool { return compareNodePackageRecords(records[i], records[j]) < 0 })
	occurrences := make(map[string]int, len(records))
	for index := range records {
		fingerprint, err := nodePackageFingerprint(records[index])
		if err != nil {
			return NodePackageList{}, fmt.Errorf("list node packages: encode item cursor: %w", err)
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
			return NodePackageList{}, fmt.Errorf("node package cursor is no longer present in the filtered result")
		}
	}

	items := make([]NodePackage, 0, min(limit, len(records)-start))
	textRunes := 0
	end := start
	for end < len(records) && len(items) < limit {
		itemRunes := nodePackageTextRunes(records[end].item)
		if len(items) > 0 && textRunes+itemRunes > maxNodePackagePageTextRunes {
			break
		}
		items = append(items, records[end].item)
		textRunes += itemRunes
		end++
	}
	if items == nil {
		items = []NodePackage{}
	}
	result := NodePackageList{
		Provenance: s.newProvenance(), Node: node, ReportedTotal: len(response.Items),
		Total: len(records), Count: len(items), Packages: items, Truncated: end < len(records),
	}
	if result.Truncated {
		result.NextCursor = records[end-1].cursor
	}
	return result, nil
}

func validateNodePackageOptions(options ListNodePackagesOptions) (string, []string, []string, []string, []string, int, string, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return "", nil, nil, nil, nil, 0, "", err
	}
	names, err := normalizeNodePackageFilters("name", options.Names, maxNodePackageNameRunes, false)
	if err != nil {
		return "", nil, nil, nil, nil, 0, "", err
	}
	prefixes, err := normalizeNodePackageFilters("name prefix", options.NamePrefixes, maxNodePackageNameRunes, false)
	if err != nil {
		return "", nil, nil, nil, nil, 0, "", err
	}
	types, err := normalizeNodePackageFilters("type", options.Types, maxNodePackageTypeRunes, true)
	if err != nil {
		return "", nil, nil, nil, nil, 0, "", err
	}
	architectures, err := normalizeNodePackageFilters("architecture", options.Architectures, maxNodePackageArchitectureRunes, true)
	if err != nil {
		return "", nil, nil, nil, nil, 0, "", err
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultNodePackageLimit
	}
	if limit < 1 || limit > maxNodePackageLimit {
		return "", nil, nil, nil, nil, 0, "", fmt.Errorf("node package limit must be between 1 and %d", maxNodePackageLimit)
	}
	if len([]rune(options.Cursor)) > maxNodePackageCursorRunes || strings.ContainsAny(options.Cursor, "\r\n\x00") {
		return "", nil, nil, nil, nil, 0, "", fmt.Errorf("node package cursor exceeds %d characters or contains control characters", maxNodePackageCursorRunes)
	}
	return node, names, prefixes, types, architectures, limit, options.Cursor, nil
}

func normalizeNodePackageFilters(kind string, values []string, maxRunes int, allowEmpty bool) ([]string, error) {
	if len(values) > maxNodePackageFilters {
		return nil, fmt.Errorf("node package %s filters are limited to %d entries", kind, maxNodePackageFilters)
	}
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if (!allowEmpty && value == "") || value != strings.TrimSpace(value) || len([]rune(value)) > maxRunes || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("node package %s filters must contain at most %d non-control characters without surrounding whitespace", kind, maxRunes)
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

func projectNodePackage(raw daemonNodePackageItem) (nodePackageRecord, error) {
	if err := validateNodePackageField("name", raw.Data.Name, maxNodePackageNameRunes, false); err != nil {
		return nodePackageRecord{}, err
	}
	for field, value := range map[string]string{
		"version": raw.Data.Version, "architecture": raw.Data.Arch, "type": raw.Data.Type,
	} {
		limit := maxNodePackageVersionRunes
		switch field {
		case "architecture":
			limit = maxNodePackageArchitectureRunes
		case "type":
			limit = maxNodePackageTypeRunes
		}
		if err := validateNodePackageField(field, value, limit, true); err != nil {
			return nodePackageRecord{}, err
		}
	}
	if err := validateNodePackageField("signature", raw.Data.Signature, maxNodePackageSignatureRunes, true); err != nil {
		return nodePackageRecord{}, err
	}
	if raw.Data.InstalledAt == "" || len([]rune(raw.Data.InstalledAt)) > maxNodePackageTimestampRunes || strings.ContainsAny(raw.Data.InstalledAt, "\r\n\x00") {
		return nodePackageRecord{}, fmt.Errorf("package installed_at is empty, oversized, or contains control characters")
	}
	if _, err := time.Parse(time.RFC3339Nano, raw.Data.InstalledAt); err != nil {
		return nodePackageRecord{}, fmt.Errorf("package installed_at %q is not an RFC 3339 timestamp", raw.Data.InstalledAt)
	}
	return nodePackageRecord{item: NodePackage{
		Name: raw.Data.Name, Version: raw.Data.Version, Architecture: raw.Data.Arch, Type: raw.Data.Type,
		InstalledAt: raw.Data.InstalledAt, Signature: raw.Data.Signature,
	}}, nil
}

func validateNodePackageField(field, value string, maxRunes int, allowEmpty bool) error {
	if (!allowEmpty && value == "") || value != strings.TrimSpace(value) || len([]rune(value)) > maxRunes || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("package %s is empty, oversized, contains control characters, or has surrounding whitespace", field)
	}
	return nil
}

func matchesNodePackageFilters(item NodePackage, names, prefixes, types, architectures []string) bool {
	return matchesNodePackageName(item.Name, names, prefixes) &&
		matchesExactNodePackageFilter(item.Type, types) &&
		matchesExactNodePackageFilter(item.Architecture, architectures)
}

func matchesNodePackageName(name string, names, prefixes []string) bool {
	if len(names) == 0 && len(prefixes) == 0 {
		return true
	}
	if len(names) > 0 && matchesExactNodePackageFilter(name, names) {
		return true
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func matchesExactNodePackageFilter(value string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	index := sort.SearchStrings(filters, value)
	return index < len(filters) && filters[index] == value
}

func compareNodePackageRecords(left, right nodePackageRecord) int {
	leftFields := [...]string{left.item.Name, left.item.Architecture, left.item.Type, left.item.Version, left.item.InstalledAt, left.item.Signature}
	rightFields := [...]string{right.item.Name, right.item.Architecture, right.item.Type, right.item.Version, right.item.InstalledAt, right.item.Signature}
	for index := range leftFields {
		if comparison := strings.Compare(leftFields[index], rightFields[index]); comparison != 0 {
			return comparison
		}
	}
	return 0
}

func nodePackageFingerprint(record nodePackageRecord) (string, error) {
	payload, err := json.Marshal([6]string{
		record.item.Name, record.item.Architecture, record.item.Type, record.item.Version, record.item.InstalledAt, record.item.Signature,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func nodePackageTextRunes(item NodePackage) int {
	return len([]rune(item.Name)) + len([]rune(item.Version)) + len([]rune(item.Architecture)) +
		len([]rune(item.Type)) + len([]rune(item.InstalledAt)) + len([]rune(item.Signature))
}
