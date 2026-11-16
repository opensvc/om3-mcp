package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
)

const (
	defaultGetObjectConfigLimit    = 100
	maxGetObjectConfigLimit        = 200
	maxObjectConfigKeywordFilters  = 50
	maxObjectConfigKeywordLength   = 255
	maxObjectConfigValueRunes      = 4096
	maxObjectConfigTotalValueRunes = 64 << 10
)

type GetObjectConfigOptions struct {
	Path     string
	Keywords []string
	Limit    int
}

type ObjectConfig struct {
	Provenance      Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object          ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference"`
	KeywordFilter   []string               `json:"keyword_filter,omitempty" jsonschema:"the sorted exact keyword filters sent to the daemon"`
	Total           int                    `json:"total" jsonschema:"the number of matching configuration keywords returned by the daemon"`
	Count           int                    `json:"count" jsonschema:"the number of configuration keyword records returned by this tool call"`
	Items           []ObjectConfigKeyword  `json:"items" jsonschema:"the sorted raw non-evaluated configuration keyword records"`
	Truncated       bool                   `json:"truncated" jsonschema:"whether additional keyword records were omitted by the configured item limit"`
	ValuesTruncated int                    `json:"values_truncated" jsonschema:"the number of returned values shortened by per-value or aggregate output bounds"`
	// DataKeysOmitted counts the data keys of a sec or usr left out: their
	// values are secrets, encrypted, which the model has no use for.
	DataKeysOmitted int    `json:"data_keys_omitted,omitempty" jsonschema:"for a sec or usr object, the number of data.* keywords left out: the keys of a secret are never returned; list_object_data_keys gives their names"`
	DataKeysNote    string `json:"data_keys_note,omitempty" jsonschema:"why the data keys were left out, and where their names are"`
	// SecretsOmitted counts the secret keywords left out, set but not
	// returned: an empty list does not mean they are not set.
	SecretsOmitted int `json:"secrets_omitted,omitempty" jsonschema:"the number of secret keywords left out, cluster.secret and node.uuid: they are set, their values are never returned"`
}

// secretKeywords are the keywords om3 declares secret, RedactSecret: the
// daemon returns them in clear to root through the keyword reads, which
// take no redact-secrets parameter. They are left out of every keyword read.
var secretKeywords = []string{"cluster.secret", "node.uuid"}

// isSecretKeyword says whether a keyword is a secret, whatever its case and
// its scope: cluster.secret is scopable, and the daemon gives a value scoped
// to a node in the keyword, cluster.secret@n1, as the redaction of a
// configuration file compares its base option.
func isSecretKeyword(keyword string) bool {
	base, _, _ := strings.Cut(keyword, "@")
	return slices.Contains(secretKeywords, strings.ToLower(base))
}

// omitSecretKeywords leaves the secret keywords out of items, and counts
// them. A read calls it before its sort and its bound, so that total and
// truncated count what is returned.
func omitSecretKeywords(items []daemonObjectConfigItem) ([]daemonObjectConfigItem, int) {
	kept := items[:0]
	omitted := 0
	for _, item := range items {
		if isSecretKeyword(item.Keyword) {
			omitted++
			continue
		}
		kept = append(kept, item)
	}
	return kept, omitted
}

// secretDataKeysNote tells why the keys of a sec or usr are left out.
const secretDataKeysNote = "the keys of a sec or usr object are secrets, never returned here; list_object_data_keys gives their names"

// secretDataKeys says whether the data keys of an object of kind are
// secrets: those of a sec or a usr are, those of a cfg are plain text.
func secretDataKeys(kind string) bool {
	return kind == "sec" || kind == "usr"
}

// isDataKeyword says whether a keyword is a data key, data.<name>.
func isDataKeyword(keyword string) bool {
	return strings.HasPrefix(strings.ToLower(keyword), "data.")
}

type ObjectConfigKeyword struct {
	Keyword        string `json:"keyword" jsonschema:"the OpenSVC configuration keyword in section.option form"`
	Value          string `json:"value" jsonschema:"the bounded raw non-evaluated configuration value"`
	Node           string `json:"node" jsonschema:"the node scope reported by OpenSVC, or an empty string for an unscoped value"`
	ValueTruncated bool   `json:"value_truncated" jsonschema:"whether the raw value was shortened to keep the tool response bounded"`
}

type daemonObjectConfig struct {
	Kind  string                   `json:"kind"`
	Items []daemonObjectConfigItem `json:"items"`
}

type daemonObjectConfigItem struct {
	Object      string          `json:"object"`
	Node        string          `json:"node"`
	Keyword     string          `json:"keyword"`
	Value       string          `json:"value"`
	EvaluatedAs string          `json:"evaluated_as"`
	Evaluated   json.RawMessage `json:"evaluated"`
}

func (s *Service) GetObjectConfig(ctx context.Context, options GetObjectConfigOptions) (ObjectConfig, error) {
	reference, err := validateExactObjectPath(options.Path)
	if err != nil {
		return ObjectConfig{}, err
	}
	keywords, err := normalizeObjectConfigKeywords(options.Keywords, reference.Kind)
	if err != nil {
		return ObjectConfig{}, err
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultGetObjectConfigLimit
	}
	if limit < 1 || limit > maxGetObjectConfigLimit {
		return ObjectConfig{}, fmt.Errorf("object config limit must be between 1 and %d", maxGetObjectConfigLimit)
	}

	query := url.Values{"evaluate": {"false"}}
	if len(keywords) > 0 {
		query["kw"] = keywords
	}
	endpoint := fmt.Sprintf(
		"/api/object/path/%s/%s/%s/config",
		reference.Namespace,
		reference.Kind,
		reference.Name,
	)
	var response daemonObjectConfig
	if err := s.client.GetJSON(ctx, endpoint, query, &response); err != nil {
		return ObjectConfig{}, fmt.Errorf("get object config: %w", err)
	}
	if response.Kind != "KeywordList" {
		return ObjectConfig{}, shapeErrorf("object config returned unexpected kind %q", response.Kind)
	}
	for _, item := range response.Items {
		if item.Object != reference.Path {
			return ObjectConfig{}, fmt.Errorf("object config returned keyword for unexpected object %q", item.Object)
		}
		if strings.TrimSpace(item.Keyword) == "" {
			return ObjectConfig{}, fmt.Errorf("object config returned an empty keyword")
		}
		if item.EvaluatedAs != "" || (len(item.Evaluated) > 0 && !bytes.Equal(item.Evaluated, []byte("null"))) {
			return ObjectConfig{}, fmt.Errorf("object config unexpectedly returned evaluated data for keyword %q", item.Keyword)
		}
	}
	result := s.configKeywordsOf(response.Items, reference.Kind, limit)
	result.Object = reference
	result.KeywordFilter = keywords
	return result, nil
}

func normalizeObjectConfigKeywords(input []string, kind string) ([]string, error) {
	if len(input) > maxObjectConfigKeywordFilters {
		return nil, fmt.Errorf("object config accepts at most %d keyword filters", maxObjectConfigKeywordFilters)
	}
	keywords := make([]string, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, inputKeyword := range input {
		keyword := strings.TrimSpace(inputKeyword)
		if keyword == "" {
			return nil, fmt.Errorf("object config keyword filter is empty")
		}
		if len(keyword) > maxObjectConfigKeywordLength {
			return nil, fmt.Errorf("object config keyword filter exceeds %d characters", maxObjectConfigKeywordLength)
		}
		// A filter naming a data key would bring its value back.
		if secretDataKeys(kind) && isDataKeyword(keyword) {
			return nil, fmt.Errorf("keyword %q: %s", keyword, secretDataKeysNote)
		}
		if _, ok := seen[keyword]; ok {
			continue
		}
		seen[keyword] = struct{}{}
		keywords = append(keywords, keyword)
	}
	sort.Strings(keywords)
	return keywords, nil
}

func boundObjectConfigValue(value string, remainingRunes int) (string, bool, int) {
	runes := []rune(value)
	allowed := min(len(runes), maxObjectConfigValueRunes, max(remainingRunes, 0))
	if allowed == len(runes) {
		return value, false, allowed
	}
	if allowed == 0 {
		return "", len(runes) > 0, 0
	}
	return string(runes[:allowed-1]) + "…", true, allowed
}

// configKeywordsOf leaves the secrets out of the keyword records of a read,
// of an object of kind or of a node, kind empty, then sorts and bounds
// them. The secrets are left out before the sort and the bound, so that
// total and truncated count what is returned.
func (s *Service) configKeywordsOf(records []daemonObjectConfigItem, kind string, limit int) ObjectConfig {
	var secrets int
	records, secrets = omitSecretKeywords(records)
	omitted := 0
	if secretDataKeys(kind) {
		kept := records[:0]
		for _, item := range records {
			if isDataKeyword(item.Keyword) {
				omitted++
				continue
			}
			kept = append(kept, item)
		}
		records = kept
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Keyword == records[j].Keyword {
			return records[i].Node < records[j].Node
		}
		return records[i].Keyword < records[j].Keyword
	})

	end := min(limit, len(records))
	items := make([]ObjectConfigKeyword, 0, end)
	remainingValueRunes := maxObjectConfigTotalValueRunes
	valuesTruncated := 0
	for _, item := range records[:end] {
		value, valueTruncated, usedRunes := boundObjectConfigValue(item.Value, remainingValueRunes)
		remainingValueRunes -= usedRunes
		if valueTruncated {
			valuesTruncated++
		}
		items = append(items, ObjectConfigKeyword{
			Keyword:        item.Keyword,
			Value:          value,
			Node:           item.Node,
			ValueTruncated: valueTruncated,
		})
	}

	note := ""
	if omitted > 0 {
		note = secretDataKeysNote
	}
	return ObjectConfig{
		Provenance:      s.newProvenance(),
		Total:           len(records),
		Count:           len(items),
		Items:           items,
		Truncated:       end < len(records),
		ValuesTruncated: valuesTruncated,
		DataKeysOmitted: omitted,
		DataKeysNote:    note,
		SecretsOmitted:  secrets,
	}
}
