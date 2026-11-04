package core

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestListObjectConfigKeywordsFiltersSortsAndPaginates(t *testing.T) {
	docker := validDaemonObjectConfigKeywordDefinition()
	docker.Types = []string{"docker"}
	docker.Scopable = true
	docker.Candidates = []string{"alpine:latest"}
	docker.Depends = []string{"container#0.type=docker"}
	docker.Kind = []string{"svc", "vol"}
	docker.Aliases = []string{"container_image"}
	docker.Default = "alpine:latest"
	docker.DefaultText = "the image configured by the site policy"
	docker.Example = "redis:7-alpine"
	docker.Since = "v3.0.0"
	docker.Deprecated = "use registry_image"
	docker.ReplacedBy = "registry_image"
	docker.RedactSecret = true
	docker.Recorded = true
	docker.Arithmetic = true
	docker.Required = true
	docker.Minimal = true
	podman := validDaemonObjectConfigKeywordDefinition()
	podman.Types = []string{"podman"}

	payload := marshalObjectConfigKeywordResponse(t, []daemonObjectConfigKeywordDefinition{podman, docker, docker})
	client := &recordingJSONGetter{
		t: t, path: "/api/object/path/example/svc/cache/config/keywords",
		query: url.Values{"option": {"image"}}, payload: payload,
	}
	service := New(client)
	options := ListObjectConfigKeywordsOptions{Path: " example/svc/cache ", Option: "image", Limit: 2}

	first, err := service.ListObjectConfigKeywords(context.Background(), options)
	if err != nil {
		t.Fatalf("list first object config keyword page: %v", err)
	}
	if first.Object.Path != "example/svc/cache" || first.Filters.Option != "image" || first.Total != 3 || first.Count != 2 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	if first.Definitions[0].Types[0] != "docker" || !reflect.DeepEqual(first.Definitions[0], first.Definitions[1]) {
		t.Fatalf("definitions were not sorted or duplicate was lost: %#v", first.Definitions)
	}
	definition := first.Definitions[0]
	if !definition.Scopable || definition.Default != "alpine:latest" || definition.DefaultText == "" || definition.Example != "redis:7-alpine" ||
		!reflect.DeepEqual(definition.Candidates, []string{"alpine:latest"}) || !reflect.DeepEqual(definition.Depends, []string{"container#0.type=docker"}) ||
		!reflect.DeepEqual(definition.Kinds, []string{"svc", "vol"}) || !reflect.DeepEqual(definition.Aliases, []string{"container_image"}) ||
		definition.Since != "v3.0.0" || definition.Deprecated == "" || definition.ReplacedBy != "registry_image" ||
		!definition.RedactSecret || !definition.Recorded || !definition.Arithmetic || !definition.Required || !definition.Minimal {
		t.Errorf("definition fields were not projected: %#v", definition)
	}

	options.Cursor = first.NextCursor
	second, err := service.ListObjectConfigKeywords(context.Background(), options)
	if err != nil {
		t.Fatalf("list second object config keyword page: %v", err)
	}
	if second.Count != 1 || second.Truncated || second.NextCursor != "" || second.Definitions[0].Types[0] != "podman" {
		t.Errorf("unexpected second page: %#v", second)
	}
	if client.calls != 2 {
		t.Errorf("got %d daemon calls, want 2", client.calls)
	}
}

func TestListObjectConfigKeywordsSendsDriverAndSectionFilters(t *testing.T) {
	for name, options := range map[string]ListObjectConfigKeywordsOptions{
		"driver":  {Path: "example/svc/cache", Driver: "container.docker", Option: "image"},
		"section": {Path: "example/svc/cache", Section: "container#redis", Option: "image"},
	} {
		t.Run(name, func(t *testing.T) {
			query := url.Values{"option": {"image"}}
			if options.Driver != "" {
				query.Set("driver", options.Driver)
			} else {
				query.Set("section", options.Section)
			}
			client := &recordingJSONGetter{
				t: t, path: "/api/object/path/example/svc/cache/config/keywords", query: query,
				payload: marshalObjectConfigKeywordResponse(t, []daemonObjectConfigKeywordDefinition{}),
			}
			result, err := New(client).ListObjectConfigKeywords(context.Background(), options)
			if err != nil {
				t.Fatalf("list keywords: %v", err)
			}
			if result.Count != 0 || result.Total != 0 || result.Definitions == nil || result.Truncated {
				t.Errorf("unexpected empty result: %#v", result)
			}
		})
	}
}

func TestListObjectConfigKeywordsRejectsInvalidInputsBeforeDaemon(t *testing.T) {
	tests := map[string]ListObjectConfigKeywordsOptions{
		"missing path":        {},
		"conflicting filters": {Path: "example/svc/cache", Driver: "container.docker", Section: "container#redis"},
		"spaced driver":       {Path: "example/svc/cache", Driver: " container.docker"},
		"control section":     {Path: "example/svc/cache", Section: "container#redis\n"},
		"long option":         {Path: "example/svc/cache", Option: strings.Repeat("x", maxObjectConfigKeywordFilterRunes+1)},
		"invalid limit":       {Path: "example/svc/cache", Limit: maxObjectConfigKeywordLimit + 1},
		"control cursor":      {Path: "example/svc/cache", Cursor: "bad\ncursor"},
		"long cursor":         {Path: "example/svc/cache", Cursor: strings.Repeat("x", maxObjectConfigKeywordCursorRunes+1)},
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			client := &recordingJSONGetter{t: t}
			if _, err := New(client).ListObjectConfigKeywords(context.Background(), options); err == nil {
				t.Fatal("expected validation error")
			}
			if client.calls != 0 {
				t.Errorf("daemon was called %d times", client.calls)
			}
		})
	}
}

func TestListObjectConfigKeywordsRejectsMalformedDaemonData(t *testing.T) {
	valid := validDaemonObjectConfigKeywordDefinition()
	nullDepends := valid
	nullDepends.Depends = nil
	emptyOption := valid
	emptyOption.Option = ""
	longText := valid
	longText.Text = strings.Repeat("x", maxObjectConfigKeywordTextRunes+1)
	tests := map[string]string{
		"unexpected kind": `{"kind":"OtherList","items":[]}`,
		"null items":      `{"kind":"KeywordDefinitionList","items":null}`,
		"null array":      marshalObjectConfigKeywordResponse(t, []daemonObjectConfigKeywordDefinition{nullDepends}),
		"empty option":    marshalObjectConfigKeywordResponse(t, []daemonObjectConfigKeywordDefinition{emptyOption}),
		"long text":       marshalObjectConfigKeywordResponse(t, []daemonObjectConfigKeywordDefinition{longText}),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			client := &recordingJSONGetter{
				t: t, path: "/api/object/path/example/svc/cache/config/keywords", query: url.Values{}, payload: payload,
			}
			if _, err := New(client).ListObjectConfigKeywords(context.Background(), ListObjectConfigKeywordsOptions{Path: "example/svc/cache"}); err == nil {
				t.Fatal("expected malformed daemon response error")
			}
		})
	}
}

func TestListObjectConfigKeywordsPreservesGenericEmptySection(t *testing.T) {
	generic := validDaemonObjectConfigKeywordDefinition()
	generic.Section = ""
	generic.Option = "comment"
	client := &recordingJSONGetter{
		t: t, path: "/api/object/path/example/svc/cache/config/keywords", query: url.Values{},
		payload: marshalObjectConfigKeywordResponse(t, []daemonObjectConfigKeywordDefinition{generic}),
	}
	result, err := New(client).ListObjectConfigKeywords(context.Background(), ListObjectConfigKeywordsOptions{Path: "example/svc/cache"})
	if err != nil {
		t.Fatalf("list generic object config keyword: %v", err)
	}
	if result.Count != 1 || result.Definitions[0].Section != "" || result.Definitions[0].Option != "comment" {
		t.Errorf("generic keyword definition was not preserved: %#v", result)
	}
}

func TestListObjectConfigKeywordsAppliesAggregatePageBudget(t *testing.T) {
	items := make([]daemonObjectConfigKeywordDefinition, 5)
	for index := range items {
		items[index] = validDaemonObjectConfigKeywordDefinition()
		items[index].Option = string(rune('a' + index))
		items[index].Text = strings.Repeat("x", maxObjectConfigKeywordTextRunes)
	}
	client := &recordingJSONGetter{
		t: t, path: "/api/object/path/example/svc/cache/config/keywords", query: url.Values{},
		payload: marshalObjectConfigKeywordResponse(t, items),
	}
	result, err := New(client).ListObjectConfigKeywords(context.Background(), ListObjectConfigKeywordsOptions{Path: "example/svc/cache", Limit: 100})
	if err != nil {
		t.Fatalf("list object config keywords: %v", err)
	}
	if result.Total != 5 || result.Count >= result.Total || result.Count == 0 || !result.Truncated || result.NextCursor == "" {
		t.Errorf("aggregate page budget was not applied: %#v", result)
	}
}

func TestListObjectConfigKeywordsRejectsStaleCursor(t *testing.T) {
	client := &recordingJSONGetter{
		t: t, path: "/api/object/path/example/svc/cache/config/keywords", query: url.Values{},
		payload: marshalObjectConfigKeywordResponse(t, []daemonObjectConfigKeywordDefinition{validDaemonObjectConfigKeywordDefinition()}),
	}
	_, err := New(client).ListObjectConfigKeywords(context.Background(), ListObjectConfigKeywordsOptions{Path: "example/svc/cache", Cursor: "missing.0"})
	if err == nil || !strings.Contains(err.Error(), "no longer present") {
		t.Fatalf("got stale cursor error %v", err)
	}
}

func validDaemonObjectConfigKeywordDefinition() daemonObjectConfigKeywordDefinition {
	return daemonObjectConfigKeywordDefinition{
		Section: "container", Option: "image", Converter: "string", Text: "Container image reference.",
		Candidates: []string{}, Depends: []string{}, Kind: []string{}, Types: []string{}, Aliases: []string{},
		Inherit: "leaf2head",
	}
}

func marshalObjectConfigKeywordResponse(t *testing.T, items []daemonObjectConfigKeywordDefinition) string {
	t.Helper()
	payload, err := json.Marshal(daemonObjectConfigKeywordDefinitionList{Kind: "KeywordDefinitionList", Items: items})
	if err != nil {
		t.Fatalf("marshal keyword definition response: %v", err)
	}
	return string(payload)
}

// The options filter keeps the definitions of the options asked, by name or
// alias, the driver filter going to the daemon, and names the options no
// definition holds: one call checks the keywords of a resource.
func TestListObjectConfigKeywordsKeepsTheOptionsAsked(t *testing.T) {
	definition := func(option string, aliases ...string) daemonObjectConfigKeywordDefinition {
		item := validDaemonObjectConfigKeywordDefinition()
		item.Section, item.Option, item.Types = "ip", option, []string{"host"}
		if aliases != nil {
			item.Aliases = aliases
		}
		return item
	}
	payload := marshalObjectConfigKeywordResponse(t, []daemonObjectConfigKeywordDefinition{
		definition("name", "ipname"), definition("netmask"), definition("dev", "ipdev"), definition("gateway"),
	})
	client := &recordingJSONGetter{t: t, path: "/api/object/path/test/svc/web/config/keywords", query: url.Values{"driver": {"ip.host"}}, payload: payload}
	result, err := New(client).ListObjectConfigKeywords(context.Background(), ListObjectConfigKeywordsOptions{
		Path: "test/svc/web", Driver: "ip.host", Options: []string{"netmask", "ipname", "netmaskx", "netmask"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var options []string
	for _, item := range result.Definitions {
		options = append(options, item.Option)
	}
	if strings.Join(options, ",") != "name,netmask" || result.Total != 2 || strings.Join(result.MissingOptions, ",") != "netmaskx" ||
		strings.Join(result.Filters.Options, ",") != "ipname,netmask,netmaskx" {
		t.Fatalf("result %+v", result)
	}
	for name, options := range map[string]ListObjectConfigKeywordsOptions{
		"with option": {Path: "test/svc/web", Option: "dev", Options: []string{"dev"}},
		"empty":       {Path: "test/svc/web", Options: []string{""}},
		"padded":      {Path: "test/svc/web", Options: []string{" dev"}},
		"too many":    {Path: "test/svc/web", Options: strings.Fields(strings.Repeat("a ", 31))},
	} {
		if _, err := New(&recordingJSONGetter{t: t}).ListObjectConfigKeywords(context.Background(), options); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestConfigKeywordFiltersRefuseAKeywordAsOption refuses a keyword,
// <section>.<option>, given as an option, before the daemon is called, with
// the section and the option to give instead: no option name holds a dot,
// and the daemon would answer no definition.
func TestConfigKeywordFiltersRefuseAKeywordAsOption(t *testing.T) {
	tests := map[string]struct {
		object ListObjectConfigKeywordsOptions
		node   ListNodeConfigKeywordsOptions
		says   string
	}{
		"option": {
			object: ListObjectConfigKeywordsOptions{Path: "cluster", Option: "ai.agent_url"},
			node:   ListNodeConfigKeywordsOptions{Option: "ai.agent_url"},
			says:   `call again with section "ai" and option "agent_url"`,
		},
		"options": {
			object: ListObjectConfigKeywordsOptions{Path: "cluster", Options: []string{"quorum", "hb#1.timeout"}},
			node:   ListNodeConfigKeywordsOptions{Options: []string{"quorum", "hb#1.timeout"}},
			says:   `call again with section "hb#1" and options ["timeout"], one call per section`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			client := &recordingJSONGetter{t: t}
			_, objectErr := New(client).ListObjectConfigKeywords(context.Background(), test.object)
			_, nodeErr := New(client).ListNodeConfigKeywords(context.Background(), test.node)
			for _, err := range []error{objectErr, nodeErr} {
				if err == nil || !strings.Contains(err.Error(), test.says) {
					t.Errorf("error %v, want %q", err, test.says)
				}
			}
			if client.calls != 0 {
				t.Errorf("daemon was called %d times", client.calls)
			}
		})
	}
	for _, name := range []string{"agent_url", ".hidden", "trailing."} {
		if _, _, ok := keywordParts(name); ok {
			t.Errorf("keywordParts(%q) is a keyword", name)
		}
	}
}
