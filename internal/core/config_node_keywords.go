package core

import (
	"context"
	"fmt"
	"net/url"
	"slices"
)

type GetNodeConfigKeywordsOptions struct {
	Node     string
	Keywords []string
	Limit    int
}

type NodeConfigKeywords struct {
	Provenance      Provenance            `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node            string                `json:"node" jsonschema:"the node whose configuration was read"`
	KeywordFilter   []string              `json:"keyword_filter,omitempty" jsonschema:"the sorted exact keyword filters sent to the daemon"`
	Total           int                   `json:"total" jsonschema:"the number of keyword records returned by the daemon, secrets left out"`
	Count           int                   `json:"count" jsonschema:"the number of keyword records returned by this tool call"`
	Items           []ObjectConfigKeyword `json:"items" jsonschema:"the sorted raw non-evaluated keyword records: the keys node.conf sets, or the keywords asked, an unset one with an empty value"`
	Truncated       bool                  `json:"truncated" jsonschema:"whether keyword records were omitted by the item limit"`
	ValuesTruncated int                   `json:"values_truncated" jsonschema:"the number of returned values shortened by per-value or aggregate output bounds"`
	SecretsOmitted  int                   `json:"secrets_omitted,omitempty" jsonschema:"the number of secret keywords left out, node.uuid: set, its value is never returned"`
}

type ListNodeConfigKeywordsOptions struct {
	Node    string
	Driver  string
	Section string
	Option  string
	Options []string
	Limit   int
	Cursor  string
}

type NodeConfigKeywordDefinitionList struct {
	Provenance     Provenance                      `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node           string                          `json:"node" jsonschema:"the node the catalog was asked of, empty for the daemon answering; the catalog is the same on every node"`
	Filters        ObjectConfigKeywordFilters      `json:"filters" jsonschema:"the exact daemon-side definition filters applied to this result"`
	Total          int                             `json:"total" jsonschema:"number of definitions matching these filters before MCP pagination"`
	Count          int                             `json:"count" jsonschema:"number of definitions returned in this page"`
	Definitions    []ObjectConfigKeywordDefinition `json:"definitions" jsonschema:"supported node configuration keyword definitions in deterministic order"`
	NextCursor     string                          `json:"next_cursor,omitempty" jsonschema:"opaque cursor to pass unchanged for the next page with the same node and filters"`
	Truncated      bool                            `json:"truncated" jsonschema:"whether definitions remain after this page"`
	MissingOptions []string                        `json:"missing_options,omitempty" jsonschema:"the options asked that no definition of these filters holds, by name or alias"`
}

type UpdateNodeConfigOptions struct {
	Node   string
	Set    []string
	Unset  []string
	Delete []string
}

type NodeConfigUpdate struct {
	Provenance Provenance `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node       string     `json:"node" jsonschema:"the node whose configuration was written"`
	Outcome    string     `json:"outcome" jsonschema:"written when the configuration changed, unchanged when the operations left it as it was"`
	IsChanged  bool       `json:"is_changed" jsonschema:"whether the daemon wrote a changed configuration"`
	Warnings   []string   `json:"warnings,omitempty" jsonschema:"what the daemon leaves to look at, the write being done"`
	// Keywords are the keywords the operations set or unset, read again.
	Keywords       []ObjectConfigKeyword `json:"keywords" jsonschema:"the keywords the set and unset operations name, read again after the write: an unset keyword reads back with an empty value, as one never set"`
	KeywordsError  string                `json:"keywords_error,omitempty" jsonschema:"why the keywords could not be read again; the write itself is done"`
	SecretsOmitted int                   `json:"secrets_omitted,omitempty" jsonschema:"the number of secret keywords the operations name, node.uuid, left out of the keywords read again: written, its value is never returned"`
}

// GetNodeConfigKeywords reads keywords of the configuration of one node,
// through the daemon, which relays the read to the node: the keys node.conf
// sets, or the keywords asked. node.uuid is left out and counted.
func (s *Service) GetNodeConfigKeywords(ctx context.Context, options GetNodeConfigKeywordsOptions) (NodeConfigKeywords, error) {
	if err := validateNodeTarget(options.Node); err != nil {
		return NodeConfigKeywords{}, err
	}
	keywords, err := normalizeObjectConfigKeywords(options.Keywords, "")
	if err != nil {
		return NodeConfigKeywords{}, err
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultGetObjectConfigLimit
	}
	if limit < 1 || limit > maxGetObjectConfigLimit {
		return NodeConfigKeywords{}, fmt.Errorf("node config limit must be between 1 and %d", maxGetObjectConfigLimit)
	}
	query := url.Values{"evaluate": {"false"}}
	if len(keywords) > 0 {
		query["kw"] = keywords
	}
	var response daemonObjectConfig
	if err := s.client.GetJSON(ctx, fmt.Sprintf("/api/node/name/%s/config", options.Node), query, &response); err != nil {
		return NodeConfigKeywords{}, fmt.Errorf("get node config: %w", err)
	}
	if response.Kind != "KeywordList" {
		return NodeConfigKeywords{}, shapeErrorf("node config returned unexpected kind %q", response.Kind)
	}
	for _, item := range response.Items {
		if item.Object != "" || (item.Node != "" && item.Node != options.Node) {
			return NodeConfigKeywords{}, fmt.Errorf("node config returned a keyword of another object or node")
		}
		if item.Keyword == "" {
			return NodeConfigKeywords{}, fmt.Errorf("node config returned an empty keyword")
		}
	}
	read := s.configKeywordsOf(response.Items, "", limit)
	return NodeConfigKeywords{
		Provenance: read.Provenance, Node: options.Node, KeywordFilter: keywords, Total: read.Total, Count: read.Count, Items: read.Items,
		Truncated: read.Truncated, ValuesTruncated: read.ValuesTruncated, SecretsOmitted: read.SecretsOmitted,
	}, nil
}

// ListNodeConfigKeywords reads one page of the catalog of the keywords of
// the node configuration, the same on every node: without a node, the
// daemon answering serves it, through the _ alias, which the input refuses
// as every node tool does.
func (s *Service) ListNodeConfigKeywords(ctx context.Context, options ListNodeConfigKeywordsOptions) (NodeConfigKeywordDefinitionList, error) {
	target := localDaemonNodeAlias
	if options.Node != "" {
		if err := validateNodeTarget(options.Node); err != nil {
			return NodeConfigKeywordDefinitionList{}, err
		}
		target = options.Node
	}
	filters, limit, cursor, err := validateConfigKeywordFilters(ListObjectConfigKeywordsOptions{
		Driver: options.Driver, Section: options.Section, Option: options.Option, Options: options.Options, Limit: options.Limit, Cursor: options.Cursor,
	})
	if err != nil {
		return NodeConfigKeywordDefinitionList{}, err
	}
	page, err := s.listConfigKeywordDefinitions(ctx, fmt.Sprintf("/api/node/name/%s/config/keywords", target), "node:"+options.Node, filters, limit, cursor)
	if err != nil {
		return NodeConfigKeywordDefinitionList{}, fmt.Errorf("list node config keywords: %w", err)
	}
	return NodeConfigKeywordDefinitionList{
		Provenance: page.Provenance, Node: options.Node, Filters: page.Filters, Total: page.Total, Count: page.Count, Definitions: page.Definitions,
		NextCursor: page.NextCursor, Truncated: page.Truncated, MissingOptions: page.MissingOptions,
	}, nil
}

// UpdateNodeConfig deletes sections, unsets keywords and applies set
// operations to the configuration of one node, as the daemon does for an
// object, then reads the keywords named again. A node configuration does
// not propagate: the daemon takes no wait and gives no OM-Last-Modified.
func (s *Service) UpdateNodeConfig(ctx context.Context, options UpdateNodeConfigOptions) (NodeConfigUpdate, error) {
	if err := validateNodeTarget(options.Node); err != nil {
		return NodeConfigUpdate{}, err
	}
	if len(options.Set)+len(options.Unset)+len(options.Delete) == 0 {
		return NodeConfigUpdate{}, fmt.Errorf("at least one set, unset or delete operation is required")
	}
	if len(options.Set)+len(options.Unset)+len(options.Delete) > maxConfigOperations {
		return NodeConfigUpdate{}, fmt.Errorf("at most %d operations are accepted in one update", maxConfigOperations)
	}
	sets, setKeys, err := validateConfigSets(options.Set)
	if err != nil {
		return NodeConfigUpdate{}, err
	}
	for _, key := range options.Unset {
		if err := validateConfigKey(key); err != nil {
			return NodeConfigUpdate{}, fmt.Errorf("unset %q: %w", key, err)
		}
	}
	for _, section := range options.Delete {
		if err := validateConfigSection(section); err != nil {
			return NodeConfigUpdate{}, fmt.Errorf("delete %q: %w", section, err)
		}
	}
	patcher, ok := s.client.(JSONPatcher)
	if !ok {
		return NodeConfigUpdate{}, fmt.Errorf("OpenSVC daemon client does not support PATCH requests")
	}
	query := url.Values{}
	for _, section := range options.Delete {
		query.Add("delete", section)
	}
	for _, key := range options.Unset {
		query.Add("unset", key)
	}
	for _, operation := range sets {
		query.Add("set", operation)
	}
	var committed daemonCommitted
	if _, err := patcher.PatchJSON(ctx, fmt.Sprintf("/api/node/name/%s/config", options.Node), query, &committed); err != nil {
		return NodeConfigUpdate{}, fmt.Errorf("update node config: %w", err)
	}
	result := NodeConfigUpdate{Node: options.Node, Outcome: "written", IsChanged: committed.IsChanged, Keywords: []ObjectConfigKeyword{}}
	if !committed.IsChanged {
		result.Outcome = "unchanged"
	}
	if committed.Warnings != nil {
		result.Warnings = *committed.Warnings
	}
	if keys := slices.Concat(setKeys, options.Unset); len(keys) > 0 {
		read, err := s.GetNodeConfigKeywords(ctx, GetNodeConfigKeywordsOptions{Node: options.Node, Keywords: keys, Limit: maxGetObjectConfigLimit})
		if err != nil {
			result.KeywordsError = err.Error()
		} else {
			result.Keywords, result.SecretsOmitted = read.Items, read.SecretsOmitted
		}
	}
	result.Provenance = s.newProvenance()
	return result, nil
}
