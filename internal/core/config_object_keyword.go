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
	"unicode"
)

const (
	defaultObjectConfigKeywordLimit   = 25
	maxObjectConfigKeywordLimit       = 100
	maxObjectConfigKeywordFilterRunes = 255
	maxObjectConfigKeywordItems       = 10000
	maxObjectConfigKeywordArrayItems  = 256
	maxObjectConfigKeywordFieldRunes  = 4096
	maxObjectConfigKeywordTextRunes   = 32 << 10
	maxObjectConfigKeywordPageRunes   = 128 << 10
	maxObjectConfigKeywordCursorRunes = 128
	maxObjectConfigKeywordOptions     = 30
)

type ListObjectConfigKeywordsOptions struct {
	Path    string
	Driver  string
	Section string
	Option  string
	// Options keeps the definitions of these options only, by name or
	// alias, filtered by the MCP.
	Options []string
	Limit   int
	Cursor  string
}

type ObjectConfigKeywordDefinitionList struct {
	Provenance  Provenance                      `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object      ClusterObjectReference          `json:"object" jsonschema:"the canonical OpenSVC object reference used to resolve supported definitions"`
	Filters     ObjectConfigKeywordFilters      `json:"filters" jsonschema:"the exact daemon-side definition filters applied to this result"`
	Total       int                             `json:"total" jsonschema:"number of definitions matching these filters before MCP pagination"`
	Count       int                             `json:"count" jsonschema:"number of definitions returned in this page"`
	Definitions []ObjectConfigKeywordDefinition `json:"definitions" jsonschema:"supported configuration keyword definitions in deterministic order"`
	NextCursor  string                          `json:"next_cursor,omitempty" jsonschema:"opaque cursor to pass unchanged for the next page with the same path and filters"`
	Truncated   bool                            `json:"truncated" jsonschema:"whether definitions remain after this page"`
	// MissingOptions are the options asked that no definition holds.
	MissingOptions []string `json:"missing_options,omitempty" jsonschema:"the options asked that no definition of these filters holds, by name or alias: not keywords of this driver or section, which the daemon would write with a warning it does not return"`
}

type ObjectConfigKeywordFilters struct {
	Driver  string   `json:"driver,omitempty" jsonschema:"exact OpenSVC driver filter sent to the daemon, such as container.docker"`
	Section string   `json:"section,omitempty" jsonschema:"exact configured section filter sent to the daemon, such as container redis expressed as container#redis"`
	Option  string   `json:"option,omitempty" jsonschema:"exact option-name filter sent to the daemon"`
	Options []string `json:"options,omitempty" jsonschema:"exact option names the MCP kept the definitions of, by name or alias"`
}

type ObjectConfigKeywordDefinition struct {
	Section       string   `json:"section" jsonschema:"configuration section family such as container or app; empty identifies a generic object keyword"`
	Option        string   `json:"option" jsonschema:"configuration option name without the section prefix"`
	Scopable      bool     `json:"scopable" jsonschema:"whether the option supports OpenSVC scoping expressions"`
	Converter     string   `json:"converter" jsonschema:"OpenSVC value converter name, or an empty string when none is declared"`
	Text          string   `json:"text" jsonschema:"reference documentation for this option"`
	DefaultText   string   `json:"default_text" jsonschema:"documentation for the default behavior when no literal default is declared"`
	Example       string   `json:"example" jsonschema:"example value supplied by OpenSVC, or an empty string"`
	Default       string   `json:"default" jsonschema:"literal default value supplied by OpenSVC, or an empty string"`
	DefaultOption string   `json:"default_option" jsonschema:"related option used to determine the default, or an empty string"`
	Candidates    []string `json:"candidates" jsonschema:"enumerated candidate values; an empty array means OpenSVC declares none"`
	Depends       []string `json:"depends" jsonschema:"configuration dependencies declared by OpenSVC; an empty array means none"`
	Kinds         []string `json:"kinds" jsonschema:"object kinds to which this definition applies; an empty array means no additional kind restriction"`
	Provisioning  bool     `json:"provisioning" jsonschema:"whether this option participates in provisioning behavior"`
	Types         []string `json:"types" jsonschema:"driver types to which this definition applies; an empty array means no additional type restriction"`
	Aliases       []string `json:"aliases" jsonschema:"legacy or alternate option names; an empty array means none"`
	Inherit       string   `json:"inherit" jsonschema:"OpenSVC inheritance policy for this option"`
	Since         string   `json:"since" jsonschema:"release that introduced the option, or an empty string when unspecified"`
	Deprecated    string   `json:"deprecated" jsonschema:"deprecation information, or an empty string when the option is not marked deprecated"`
	ReplacedBy    string   `json:"replaced_by" jsonschema:"replacement option name for a deprecated definition, or an empty string"`
	RedactSecret  bool     `json:"redact_secret" jsonschema:"whether OpenSVC treats the configured value as secret when redaction is requested"`
	Recorded      bool     `json:"recorded" jsonschema:"whether a created runtime identifier is recorded in configuration and reset on clone"`
	Arithmetic    bool     `json:"arithmetic" jsonschema:"whether arithmetic expressions in the configured value are evaluated"`
	Required      bool     `json:"required" jsonschema:"whether this option is required"`
	Minimal       bool     `json:"minimal" jsonschema:"whether this option belongs to the minimal configuration reference"`
}

type daemonObjectConfigKeywordDefinitionList struct {
	Kind  string                                `json:"kind"`
	Items []daemonObjectConfigKeywordDefinition `json:"items"`
}

type daemonObjectConfigKeywordDefinition struct {
	Section       string   `json:"section"`
	Option        string   `json:"option"`
	Scopable      bool     `json:"scopable"`
	Converter     string   `json:"converter"`
	Text          string   `json:"text"`
	DefaultText   string   `json:"defaultText"`
	Example       string   `json:"example"`
	Default       string   `json:"default"`
	DefaultOption string   `json:"defaultOption"`
	Candidates    []string `json:"candidates"`
	Depends       []string `json:"depends"`
	Kind          []string `json:"kind"`
	Provisioning  bool     `json:"provisioning"`
	Types         []string `json:"types"`
	Aliases       []string `json:"aliases"`
	Inherit       string   `json:"inherit"`
	Since         string   `json:"since"`
	Deprecated    string   `json:"deprecated"`
	ReplacedBy    string   `json:"replacedBy"`
	RedactSecret  bool     `json:"redactSecret"`
	Recorded      bool     `json:"recorded"`
	Arithmetic    bool     `json:"arithmetic"`
	Required      bool     `json:"required"`
	Minimal       bool     `json:"minimal"`
}

type objectConfigKeywordRecord struct {
	definition ObjectConfigKeywordDefinition
	payload    string
	cursor     string
}

func (s *Service) ListObjectConfigKeywords(ctx context.Context, options ListObjectConfigKeywordsOptions) (ObjectConfigKeywordDefinitionList, error) {
	reference, filters, limit, cursor, err := validateObjectConfigKeywordOptions(options)
	if err != nil {
		return ObjectConfigKeywordDefinitionList{}, err
	}
	endpoint := fmt.Sprintf("/api/object/path/%s/%s/%s/config/keywords", reference.Namespace, reference.Kind, reference.Name)
	result, err := s.listConfigKeywordDefinitions(ctx, endpoint, reference.Path, filters, limit, cursor)
	if err != nil {
		return ObjectConfigKeywordDefinitionList{}, fmt.Errorf("list object config keywords: %w", err)
	}
	result.Object = reference
	return result, nil
}

// listConfigKeywordDefinitions reads one page of a keyword catalog, of an
// object or of a node, the cursor bound to scope and the filters.
func (s *Service) listConfigKeywordDefinitions(ctx context.Context, endpoint string, scope string, filters ObjectConfigKeywordFilters, limit int, cursor string) (ObjectConfigKeywordDefinitionList, error) {
	query := url.Values{}
	if filters.Driver != "" {
		query.Set("driver", filters.Driver)
	}
	if filters.Section != "" {
		query.Set("section", filters.Section)
	}
	if filters.Option != "" {
		query.Set("option", filters.Option)
	}
	var response daemonObjectConfigKeywordDefinitionList
	if err := s.client.GetJSON(ctx, endpoint, query, &response); err != nil {
		return ObjectConfigKeywordDefinitionList{}, fmt.Errorf("read the catalog: %w", err)
	}
	if response.Kind != "KeywordDefinitionList" {
		return ObjectConfigKeywordDefinitionList{}, fmt.Errorf("unexpected response kind %q", response.Kind)
	}
	if response.Items == nil {
		return ObjectConfigKeywordDefinitionList{}, fmt.Errorf("response items must be an array")
	}
	if len(response.Items) > maxObjectConfigKeywordItems {
		return ObjectConfigKeywordDefinitionList{}, fmt.Errorf("response contains %d items, limit is %d", len(response.Items), maxObjectConfigKeywordItems)
	}

	records := make([]objectConfigKeywordRecord, 0, len(response.Items))
	for index, raw := range response.Items {
		definition, err := projectObjectConfigKeywordDefinition(raw)
		if err != nil {
			return ObjectConfigKeywordDefinitionList{}, fmt.Errorf("item %d: %w", index, err)
		}
		payload, err := json.Marshal(definition)
		if err != nil {
			return ObjectConfigKeywordDefinitionList{}, fmt.Errorf("encode item %d: %w", index, err)
		}
		records = append(records, objectConfigKeywordRecord{definition: definition, payload: string(payload)})
	}
	sort.SliceStable(records, func(i, j int) bool {
		return compareObjectConfigKeywordRecords(records[i], records[j]) < 0
	})
	var missing []string
	if len(filters.Options) > 0 {
		records, missing = keepObjectConfigKeywordOptions(records, filters.Options)
	}

	queryPrefix, err := objectConfigKeywordQueryFingerprint(scope, filters)
	if err != nil {
		return ObjectConfigKeywordDefinitionList{}, fmt.Errorf("encode cursor scope: %w", err)
	}
	occurrences := make(map[string]int, len(records))
	for index := range records {
		sum := sha256.Sum256([]byte(records[index].payload))
		fingerprint := base64.RawURLEncoding.EncodeToString(sum[:])
		occurrence := occurrences[fingerprint]
		records[index].cursor = queryPrefix + "." + fingerprint + "." + strconv.Itoa(occurrence)
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
			return ObjectConfigKeywordDefinitionList{}, fmt.Errorf("object config keyword cursor is no longer present for this path and filters")
		}
	}

	definitions := make([]ObjectConfigKeywordDefinition, 0, min(limit, len(records)-start))
	textRunes := 0
	end := start
	for end < len(records) && len(definitions) < limit {
		itemRunes := objectConfigKeywordTextRunes(records[end].definition)
		if len(definitions) > 0 && textRunes+itemRunes > maxObjectConfigKeywordPageRunes {
			break
		}
		definitions = append(definitions, records[end].definition)
		textRunes += itemRunes
		end++
	}
	if definitions == nil {
		definitions = []ObjectConfigKeywordDefinition{}
	}
	result := ObjectConfigKeywordDefinitionList{
		Provenance: s.newProvenance(), Filters: filters,
		Total: len(records), Count: len(definitions), Definitions: definitions, Truncated: end < len(records),
		MissingOptions: missing,
	}
	if result.Truncated {
		result.NextCursor = records[end-1].cursor
	}
	return result, nil
}

func validateObjectConfigKeywordOptions(options ListObjectConfigKeywordsOptions) (ClusterObjectReference, ObjectConfigKeywordFilters, int, string, error) {
	reference, err := validateExactObjectPath(options.Path)
	if err != nil {
		return ClusterObjectReference{}, ObjectConfigKeywordFilters{}, 0, "", err
	}
	filters, limit, cursor, err := validateConfigKeywordFilters(options)
	return reference, filters, limit, cursor, err
}

// validateConfigKeywordFilters checks the filters, the limit and the cursor
// of a keyword catalog read, of an object or of a node.
func validateConfigKeywordFilters(options ListObjectConfigKeywordsOptions) (ObjectConfigKeywordFilters, int, string, error) {
	filters := ObjectConfigKeywordFilters{Driver: options.Driver, Section: options.Section, Option: options.Option}
	if len(options.Options) > maxObjectConfigKeywordOptions {
		return ObjectConfigKeywordFilters{}, 0, "", fmt.Errorf("object config keyword options accept at most %d names", maxObjectConfigKeywordOptions)
	}
	if len(options.Options) > 0 && options.Option != "" {
		return ObjectConfigKeywordFilters{}, 0, "", fmt.Errorf("object config keyword option and options filters are mutually exclusive")
	}
	seen := map[string]bool{}
	for _, value := range options.Options {
		if value == "" || value != strings.TrimSpace(value) || len([]rune(value)) > maxObjectConfigKeywordFilterRunes || containsControl(value) {
			return ObjectConfigKeywordFilters{}, 0, "", fmt.Errorf("object config keyword options must each hold at most %d non-control characters without surrounding whitespace", maxObjectConfigKeywordFilterRunes)
		}
		if !seen[value] {
			seen[value] = true
			filters.Options = append(filters.Options, value)
		}
	}
	sort.Strings(filters.Options)
	// No option name holds a dot: one written with it is a keyword,
	// <section>.<option>, such as ai.agent_url, which no option matches.
	if section, option, ok := keywordParts(filters.Option); ok {
		return ObjectConfigKeywordFilters{}, 0, "", fmt.Errorf("option %q is a keyword, <section>.<option>, and no option name holds a dot: call again with section %q and option %q", filters.Option, section, option)
	}
	for _, value := range filters.Options {
		if section, option, ok := keywordParts(value); ok {
			return ObjectConfigKeywordFilters{}, 0, "", fmt.Errorf("options %q is a keyword, <section>.<option>, and no option name holds a dot: call again with section %q and options [%q], one call per section", value, section, option)
		}
	}
	for name, value := range map[string]string{"driver": filters.Driver, "section": filters.Section, "option": filters.Option} {
		if value == "" {
			continue
		}
		if value != strings.TrimSpace(value) || len([]rune(value)) > maxObjectConfigKeywordFilterRunes || containsControl(value) {
			return ObjectConfigKeywordFilters{}, 0, "", fmt.Errorf("object config keyword %s filter must contain at most %d non-control characters without surrounding whitespace", name, maxObjectConfigKeywordFilterRunes)
		}
	}
	if filters.Driver != "" && filters.Section != "" {
		return ObjectConfigKeywordFilters{}, 0, "", fmt.Errorf("object config keyword driver and section filters are mutually exclusive")
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultObjectConfigKeywordLimit
	}
	if limit < 1 || limit > maxObjectConfigKeywordLimit {
		return ObjectConfigKeywordFilters{}, 0, "", fmt.Errorf("object config keyword limit must be between 1 and %d", maxObjectConfigKeywordLimit)
	}
	if len([]rune(options.Cursor)) > maxObjectConfigKeywordCursorRunes || containsControl(options.Cursor) {
		return ObjectConfigKeywordFilters{}, 0, "", fmt.Errorf("object config keyword cursor exceeds %d characters or contains control characters", maxObjectConfigKeywordCursorRunes)
	}
	return filters, limit, options.Cursor, nil
}

// keywordParts cuts a keyword, <section>.<option>, at its last dot: hb#1.timeout
// gives hb#1 and timeout. ok is false for a name without a dot, or with an
// empty part.
func keywordParts(name string) (section, option string, ok bool) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

func projectObjectConfigKeywordDefinition(raw daemonObjectConfigKeywordDefinition) (ObjectConfigKeywordDefinition, error) {
	if err := validateObjectConfigKeywordIdentity("section", raw.Section, true); err != nil {
		return ObjectConfigKeywordDefinition{}, err
	}
	if err := validateObjectConfigKeywordIdentity("option", raw.Option, false); err != nil {
		return ObjectConfigKeywordDefinition{}, err
	}
	for name, value := range map[string]string{
		"converter": raw.Converter, "default": raw.Default, "default option": raw.DefaultOption,
		"inherit": raw.Inherit, "since": raw.Since, "deprecated": raw.Deprecated, "replaced by": raw.ReplacedBy,
	} {
		if err := validateObjectConfigKeywordString(name, value, maxObjectConfigKeywordFieldRunes); err != nil {
			return ObjectConfigKeywordDefinition{}, err
		}
	}
	for name, value := range map[string]string{"text": raw.Text, "default text": raw.DefaultText, "example": raw.Example} {
		if err := validateObjectConfigKeywordString(name, value, maxObjectConfigKeywordTextRunes); err != nil {
			return ObjectConfigKeywordDefinition{}, err
		}
	}
	for name, values := range map[string][]string{
		"aliases": raw.Aliases, "candidates": raw.Candidates, "depends": raw.Depends, "kind": raw.Kind, "types": raw.Types,
	} {
		if err := validateObjectConfigKeywordArray(name, values); err != nil {
			return ObjectConfigKeywordDefinition{}, err
		}
	}
	return ObjectConfigKeywordDefinition{
		Section: raw.Section, Option: raw.Option, Scopable: raw.Scopable, Converter: raw.Converter,
		Text: raw.Text, DefaultText: raw.DefaultText, Example: raw.Example, Default: raw.Default,
		DefaultOption: raw.DefaultOption, Candidates: append([]string{}, raw.Candidates...),
		Depends: append([]string{}, raw.Depends...), Kinds: append([]string{}, raw.Kind...),
		Provisioning: raw.Provisioning, Types: append([]string{}, raw.Types...), Aliases: append([]string{}, raw.Aliases...),
		Inherit: raw.Inherit, Since: raw.Since, Deprecated: raw.Deprecated, ReplacedBy: raw.ReplacedBy,
		RedactSecret: raw.RedactSecret, Recorded: raw.Recorded, Arithmetic: raw.Arithmetic,
		Required: raw.Required, Minimal: raw.Minimal,
	}, nil
}

func validateObjectConfigKeywordIdentity(name, value string, allowEmpty bool) error {
	if (!allowEmpty && value == "") || value != strings.TrimSpace(value) || len([]rune(value)) > maxObjectConfigKeywordFilterRunes || containsControl(value) {
		return fmt.Errorf("%s is empty, oversized, contains control characters, or has surrounding whitespace", name)
	}
	return nil
}

func validateObjectConfigKeywordString(name, value string, maxRunes int) error {
	if len([]rune(value)) > maxRunes || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s exceeds %d characters or contains a null character", name, maxRunes)
	}
	return nil
}

func validateObjectConfigKeywordArray(name string, values []string) error {
	if values == nil {
		return fmt.Errorf("%s must be an array, not null", name)
	}
	if len(values) > maxObjectConfigKeywordArrayItems {
		return fmt.Errorf("%s contains %d entries, limit is %d", name, len(values), maxObjectConfigKeywordArrayItems)
	}
	for index, value := range values {
		if err := validateObjectConfigKeywordString(fmt.Sprintf("%s item %d", name, index), value, maxObjectConfigKeywordFieldRunes); err != nil {
			return err
		}
	}
	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func compareObjectConfigKeywordRecords(left, right objectConfigKeywordRecord) int {
	leftFields := [...]string{left.definition.Section, left.definition.Option, strings.Join(left.definition.Types, "\x00"), strings.Join(left.definition.Kinds, "\x00"), left.payload}
	rightFields := [...]string{right.definition.Section, right.definition.Option, strings.Join(right.definition.Types, "\x00"), strings.Join(right.definition.Kinds, "\x00"), right.payload}
	for index := range leftFields {
		if comparison := strings.Compare(leftFields[index], rightFields[index]); comparison != 0 {
			return comparison
		}
	}
	return 0
}

func objectConfigKeywordQueryFingerprint(path string, filters ObjectConfigKeywordFilters) (string, error) {
	payload, err := json.Marshal([5]string{path, filters.Driver, filters.Section, filters.Option, strings.Join(filters.Options, "\x00")})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(sum[:12]), nil
}

func objectConfigKeywordTextRunes(item ObjectConfigKeywordDefinition) int {
	total := len([]rune(item.Section)) + len([]rune(item.Option)) + len([]rune(item.Converter)) +
		len([]rune(item.Text)) + len([]rune(item.DefaultText)) + len([]rune(item.Example)) +
		len([]rune(item.Default)) + len([]rune(item.DefaultOption)) + len([]rune(item.Inherit)) +
		len([]rune(item.Since)) + len([]rune(item.Deprecated)) + len([]rune(item.ReplacedBy))
	for _, values := range [][]string{item.Candidates, item.Depends, item.Kinds, item.Types, item.Aliases} {
		for _, value := range values {
			total += len([]rune(value))
		}
	}
	return total
}

// keepObjectConfigKeywordOptions keeps the records of the options asked, by
// name or alias, and names the options no record holds.
func keepObjectConfigKeywordOptions(records []objectConfigKeywordRecord, options []string) ([]objectConfigKeywordRecord, []string) {
	found := make(map[string]bool, len(options))
	kept := make([]objectConfigKeywordRecord, 0, len(options))
	for _, record := range records {
		names := append([]string{record.definition.Option}, record.definition.Aliases...)
		matched := false
		for _, option := range options {
			for _, name := range names {
				if name == option {
					found[option], matched = true, true
				}
			}
		}
		if matched {
			kept = append(kept, record)
		}
	}
	var missing []string
	for _, option := range options {
		if !found[option] {
			missing = append(missing, option)
		}
	}
	return kept, missing
}
