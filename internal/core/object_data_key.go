package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
)

const (
	maxObjectDataKeys         = 10000
	maxObjectDataKeyNameRunes = 1024
)

type ListObjectDataKeysOptions struct {
	Path string
	Name string
}

type ObjectDataKeyList struct {
	Provenance Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object     ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference"`
	Node       string                 `json:"node,omitempty" jsonschema:"the node whose copy of the object was read; empty when the object has no key"`
	NameFilter string                 `json:"name_filter,omitempty" jsonschema:"the optional exact key name filter applied"`
	Total      int                    `json:"total" jsonschema:"the number of keys matching the filter"`
	Keys       []ObjectDataKey        `json:"keys" jsonschema:"the data keys sorted by name; values are never returned"`
}

type ObjectDataKey struct {
	Name            string `json:"name" jsonschema:"the key name"`
	StoredSizeBytes int    `json:"stored_size_bytes" jsonschema:"the length of the value as stored; encrypted and encoded for a sec or usr object, so not the length of the value itself"`
}

type daemonObjectDataKeyList struct {
	Kind  string `json:"kind"`
	Items []struct {
		Name   string `json:"name"`
		Node   string `json:"node"`
		Object string `json:"object"`
		Size   int    `json:"size"`
	} `json:"items"`
}

// ListObjectDataKeys reads the names of the keys a cfg, sec or usr object
// stores, never their values. The daemon reads the copy of the object on
// itself when it has one, else on one node holding it.
func (s *Service) ListObjectDataKeys(ctx context.Context, options ListObjectDataKeysOptions) (ObjectDataKeyList, error) {
	if hasSelectorCharacters(options.Path) {
		return ObjectDataKeyList{}, fmt.Errorf("path must be one exact OpenSVC object path, without wildcard or selector")
	}
	reference, err := validateExactObjectPath(options.Path)
	if err != nil {
		return ObjectDataKeyList{}, err
	}
	if reference.Kind != "cfg" && reference.Kind != "sec" && reference.Kind != "usr" {
		return ObjectDataKeyList{}, fmt.Errorf("data keys are stored by cfg, sec and usr objects only")
	}
	name := options.Name
	if name != "" && (len(name) > maxObjectDataKeyNameRunes || strings.TrimSpace(name) != name || containsControl(name)) {
		return ObjectDataKeyList{}, fmt.Errorf("name must be one exact key name of at most %d characters, without surrounding whitespace or control characters", maxObjectDataKeyNameRunes)
	}

	var response daemonObjectDataKeyList
	endpoint := fmt.Sprintf("/api/object/path/%s/%s/%s/data/keys", reference.Namespace, reference.Kind, reference.Name)
	if err := s.client.GetJSON(ctx, endpoint, url.Values{}, &response); err != nil {
		// The daemon answers an object no node holds with an empty body.
		if errors.Is(err, io.EOF) {
			return ObjectDataKeyList{}, fmt.Errorf("list object data keys: object %s not found", reference.Path)
		}
		return ObjectDataKeyList{}, fmt.Errorf("list object data keys: %w", err)
	}
	if response.Kind != "DataKeyList" {
		return ObjectDataKeyList{}, shapeErrorf("list object data keys: unexpected response kind %q", response.Kind)
	}
	if len(response.Items) > maxObjectDataKeys {
		return ObjectDataKeyList{}, fmt.Errorf("list object data keys: response contains %d keys, limit is %d", len(response.Items), maxObjectDataKeys)
	}

	node := ""
	keys := make([]ObjectDataKey, 0, len(response.Items))
	for index, item := range response.Items {
		object, err := parseClusterObjectReference(item.Object)
		if err != nil || !sameObject(object, reference) {
			return ObjectDataKeyList{}, fmt.Errorf("list object data keys: key %d reports unexpected object %q", index, boundedObjectDataKeyText(item.Object))
		}
		if !validExactNodeName(item.Node) || node != "" && item.Node != node {
			return ObjectDataKeyList{}, fmt.Errorf("list object data keys: key %d reports unexpected node %q", index, boundedObjectDataKeyText(item.Node))
		}
		node = item.Node
		if item.Name == "" || len(item.Name) > maxObjectDataKeyNameRunes {
			return ObjectDataKeyList{}, fmt.Errorf("list object data keys: key %d has an empty or oversized name", index)
		}
		if name != "" && item.Name != name {
			continue
		}
		keys = append(keys, ObjectDataKey{Name: item.Name, StoredSizeBytes: item.Size})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return ObjectDataKeyList{
		Provenance: s.newProvenance(),
		Object:     reference,
		Node:       node,
		NameFilter: name,
		Total:      len(keys),
		Keys:       keys,
	}, nil
}

func boundedObjectDataKeyText(value string) string {
	bounded, _ := boundedRunes(value, maxObjectDataKeyNameRunes)
	return bounded
}
