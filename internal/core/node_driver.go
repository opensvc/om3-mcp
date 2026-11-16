package core

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const (
	defaultNodeDriverLimit     = 100
	maxNodeDriverLimit         = 200
	maxNodeDriverCursorLength  = 1024
	maxNodeDriverNameRunes     = 1024
	maxNodeDriverPageTextRunes = 64 << 10
)

// nodeDriverWord is a driver group or a feature, the words the filters take.
var nodeDriverWord = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

type ListNodeDriversOptions struct {
	Node   string
	Limit  int
	Cursor string
	// Group keeps the drivers of one group, such as fs or container.
	Group string
	// Feature keeps the drivers having one feature, such as resize.
	Feature string
	// Available, when set, keeps the drivers the node can or cannot use.
	Available *bool
}

type NodeDriverList struct {
	Provenance Provenance `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node       string     `json:"node" jsonschema:"the exact requested OpenSVC node name, reported by every entry"`
	// Described is false for a daemon giving the driver names only, before
	// the catalog described them.
	Described     bool                `json:"described" jsonschema:"true when the daemon describes each driver; false for an older daemon giving the names only: every other field of the drivers is then unknown, and the filters other than group refused"`
	Features      []NodeDriverFeature `json:"features,omitempty" jsonschema:"the features vocabulary, each word with its meaning, as the daemon gives it"`
	ReportedTotal int                 `json:"reported_total" jsonschema:"number of driver entries returned by OpenSVC before exact duplicate removal"`
	Total         int                 `json:"total" jsonschema:"number of distinct drivers matching the filters before MCP pagination"`
	Count         int                 `json:"count" jsonschema:"number of drivers returned in this page"`
	Drivers       []NodeDriver        `json:"drivers" jsonschema:"the drivers sorted by name"`
	NextCursor    string              `json:"next_cursor,omitempty" jsonschema:"exact driver name to pass unchanged, with the same filters, for the next page"`
	Truncated     bool                `json:"truncated" jsonschema:"whether drivers remain after this page"`
}

// NodeDriver is a driver of the catalog of a node, as its daemon describes
// it.
type NodeDriver struct {
	Name           string   `json:"name" jsonschema:"the driver, <group>.<type>, or <group> for a driver of no type, the value of the type keyword of a resource of that group"`
	Group          string   `json:"group,omitempty" jsonschema:"the driver group, the section of the resource, such as fs, disk, ip, container, app, task or sync"`
	Type           string   `json:"type,omitempty" jsonschema:"the driver type within its group, such as ext4; empty for a driver of no type"`
	Doc            string   `json:"doc,omitempty" jsonschema:"a line saying what the driver does"`
	Features       []string `json:"features,omitempty" jsonschema:"what a resource driver does, as words of the features vocabulary; a virtual driver has those of the driver it selects on the node; empty for the drivers of other groups"`
	Selects        []string `json:"selects,omitempty" jsonschema:"for a virtual driver, the drivers it selects among on the node, in the order it prefers them, as container.oci selects container.docker then container.podman"`
	AliasOf        string   `json:"alias_of,omitempty" jsonschema:"the driver this one is another name of, kept for the configurations written with it: write new ones with that driver"`
	Available      *bool    `json:"available,omitempty" jsonschema:"true when the node has what the driver needs, as its capabilities say; a virtual driver is available when one of the drivers it selects is"`
	Grant          string   `json:"grant,omitempty" jsonschema:"the grant a service needs to use the driver at all, such as root; empty when any user granted on the namespace may"`
	ProvisionGrant string   `json:"provision_grant,omitempty" jsonschema:"the grant only the provision of the resource needs, beyond grant: a user not holding it may still use the driver on what a sysadmin provisioned, with provision = false; empty when the provision needs no more than grant"`
}

// NodeDriverFeature is a word of the features vocabulary, with its meaning.
type NodeDriverFeature struct {
	Name string `json:"name" jsonschema:"the feature, such as provision or resize"`
	Doc  string `json:"doc" jsonschema:"what a driver having it does"`
}

type daemonNodeDriverList struct {
	Kind     string                 `json:"kind"`
	Features *[]NodeDriverFeature   `json:"features"`
	Items    []daemonNodeDriverItem `json:"items"`
}

type daemonNodeDriverItem struct {
	Kind string `json:"kind"`
	Meta struct {
		Node string `json:"node"`
	} `json:"meta"`
	Data struct {
		Name           string   `json:"name"`
		Group          string   `json:"group"`
		Type           string   `json:"type"`
		Doc            string   `json:"doc"`
		Features       []string `json:"features"`
		Selects        []string `json:"selects"`
		AliasOf        string   `json:"aliasOf"`
		Available      *bool    `json:"available"`
		Grant          string   `json:"grant"`
		ProvisionGrant string   `json:"provisionGrant"`
	} `json:"data"`
}

func (s *Service) ListNodeDrivers(ctx context.Context, options ListNodeDriversOptions) (NodeDriverList, error) {
	node, limit, cursor, err := validateNodeDriverOptions(options)
	if err != nil {
		return NodeDriverList{}, err
	}

	var response daemonNodeDriverList
	endpoint := fmt.Sprintf("/api/node/name/%s/drivers", node)
	if err := s.client.GetJSON(ctx, endpoint, url.Values{}, &response); err != nil {
		return NodeDriverList{}, fmt.Errorf("list node drivers: %w", err)
	}
	if response.Kind != "DriverList" {
		return NodeDriverList{}, shapeErrorf("list node drivers: unexpected response kind %q", response.Kind)
	}
	// The vocabulary comes with the descriptions: a daemon without it gives
	// the names only.
	described := response.Features != nil
	var vocabulary []NodeDriverFeature
	if described {
		vocabulary = *response.Features
	}
	if !described && (options.Feature != "" || options.Available != nil) {
		return NodeDriverList{}, fmt.Errorf("the daemon of node %s gives the driver names only, an older version: filter by group, or call without feature and available", node)
	}
	if options.Feature != "" && !slices.ContainsFunc(vocabulary, func(feature NodeDriverFeature) bool { return feature.Name == options.Feature }) {
		names := make([]string, 0, len(vocabulary))
		for _, feature := range vocabulary {
			names = append(names, feature.Name)
		}
		return NodeDriverList{}, fmt.Errorf("feature %q is none of the daemon of node %s: %s", options.Feature, node, strings.Join(names, ", "))
	}

	byName := make(map[string]NodeDriver, len(response.Items))
	for index, item := range response.Items {
		if item.Kind != "DriverItem" {
			return NodeDriverList{}, shapeErrorf("list node drivers: item %d has unexpected kind %q", index, item.Kind)
		}
		if !validExactNodeName(item.Meta.Node) {
			return NodeDriverList{}, fmt.Errorf("list node drivers: item %d reports invalid node %q", index, item.Meta.Node)
		}
		if item.Meta.Node != node {
			return NodeDriverList{}, fmt.Errorf("list node drivers: item %d reports unexpected node %q", index, item.Meta.Node)
		}
		name := item.Data.Name
		if name == "" || len([]rune(name)) > maxNodeDriverNameRunes || strings.ContainsAny(name, "\r\n\x00") {
			return NodeDriverList{}, fmt.Errorf("list node drivers: item %d has an empty, oversized, or control-containing name", index)
		}
		if _, seen := byName[name]; seen {
			continue
		}
		driver := NodeDriver{
			Name: name, Group: item.Data.Group, Type: item.Data.Type, Doc: item.Data.Doc,
			Features: item.Data.Features, Selects: item.Data.Selects, AliasOf: item.Data.AliasOf,
			Available: item.Data.Available, Grant: item.Data.Grant, ProvisionGrant: item.Data.ProvisionGrant,
		}
		if !described {
			// The group of an older daemon is the head of the name.
			driver.Group, _, _ = strings.Cut(name, ".")
		}
		if !nodeDriverMatches(driver, options) {
			continue
		}
		byName[name] = driver
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	start := 0
	if cursor != "" {
		index := sort.SearchStrings(names, cursor)
		if index == len(names) || names[index] != cursor {
			return NodeDriverList{}, fmt.Errorf("node driver cursor is no longer present in the result")
		}
		start = index + 1
	}

	items := make([]NodeDriver, 0, min(limit, len(names)-start))
	textRunes := 0
	end := start
	for end < len(names) && len(items) < limit {
		driver := byName[names[end]]
		runes := nodeDriverTextRunes(driver)
		if len(items) > 0 && textRunes+runes > maxNodeDriverPageTextRunes {
			break
		}
		if !described {
			// Unknown, not empty: the names are all an older daemon gives.
			driver.Group = ""
		}
		items = append(items, driver)
		textRunes += runes
		end++
	}
	result := NodeDriverList{
		Provenance:    s.newProvenance(),
		Node:          node,
		Described:     described,
		Features:      vocabulary,
		ReportedTotal: len(response.Items),
		Total:         len(names),
		Count:         len(items),
		Drivers:       items,
		Truncated:     end < len(names),
	}
	if result.Truncated {
		result.NextCursor = items[len(items)-1].Name
	}
	return result, nil
}

// nodeDriverMatches tells whether a driver passes the filters.
func nodeDriverMatches(driver NodeDriver, options ListNodeDriversOptions) bool {
	if options.Group != "" && driver.Group != options.Group {
		return false
	}
	if options.Feature != "" && !slices.Contains(driver.Features, options.Feature) {
		return false
	}
	if options.Available != nil && (driver.Available == nil || *driver.Available != *options.Available) {
		return false
	}
	return true
}

// nodeDriverTextRunes counts the text of a driver the page bound holds.
func nodeDriverTextRunes(driver NodeDriver) int {
	runes := len([]rune(driver.Name)) + len([]rune(driver.Group)) + len([]rune(driver.Type)) + len([]rune(driver.Doc)) +
		len([]rune(driver.AliasOf)) + len([]rune(driver.Grant)) + len([]rune(driver.ProvisionGrant))
	for _, word := range driver.Features {
		runes += len([]rune(word))
	}
	for _, name := range driver.Selects {
		runes += len([]rune(name))
	}
	return runes
}

func validateNodeDriverOptions(options ListNodeDriversOptions) (string, int, string, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return "", 0, "", err
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultNodeDriverLimit
	}
	if limit < 1 || limit > maxNodeDriverLimit {
		return "", 0, "", fmt.Errorf("node driver limit must be between 1 and %d", maxNodeDriverLimit)
	}
	if len(options.Cursor) > maxNodeDriverCursorLength || strings.ContainsAny(options.Cursor, "\r\n\x00") {
		return "", 0, "", fmt.Errorf("node driver cursor exceeds %d characters or contains control characters", maxNodeDriverCursorLength)
	}
	if options.Group != "" && !nodeDriverWord.MatchString(options.Group) {
		return "", 0, "", fmt.Errorf("node driver group must be one lowercase word, such as fs or container")
	}
	if options.Feature != "" && !nodeDriverWord.MatchString(options.Feature) {
		return "", 0, "", fmt.Errorf("node driver feature must be one lowercase word, such as resize")
	}
	return node, limit, options.Cursor, nil
}
