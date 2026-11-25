package core

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	defaultListClusterObjectsLimit = 100
	maxListClusterObjectsLimit     = 200
	maxObjectSelectorLength        = 512
	maxObjectCursorLength          = 1024
)

type ListClusterObjectsOptions struct {
	Selector string
	Limit    int
	Cursor   string
}

type ClusterObjectList struct {
	Provenance Provenance               `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Selector   string                   `json:"selector" jsonschema:"the OpenSVC object selector used for this page"`
	Total      int                      `json:"total" jsonschema:"the total number of objects matching the selector and caller grants; for a configuration term, among the objects whose configuration the answering node holds"`
	Count      int                      `json:"count" jsonschema:"the number of objects returned in this page"`
	Objects    []ClusterObjectReference `json:"objects" jsonschema:"sorted references to matching OpenSVC objects"`
	NextCursor string                   `json:"next_cursor,omitempty" jsonschema:"the cursor to pass to retrieve the next page"`
	Truncated  bool                     `json:"truncated" jsonschema:"whether more matching objects remain after this page"`
	TermNote   string                   `json:"term_note,omitempty" jsonschema:"set when the selector holds a configuration term: what the daemon could not see"`
}

type ClusterObjectReference struct {
	Path      string `json:"path" jsonschema:"the canonical OpenSVC object path"`
	Namespace string `json:"namespace" jsonschema:"the OpenSVC object namespace"`
	Kind      string `json:"kind" jsonschema:"the OpenSVC object kind"`
	Name      string `json:"name" jsonschema:"the OpenSVC object name"`
}

func (s *Service) ListClusterObjects(ctx context.Context, options ListClusterObjectsOptions) (ClusterObjectList, error) {
	selector := strings.TrimSpace(options.Selector)
	if selector == "" {
		selector = "**"
	}
	if len(selector) > maxObjectSelectorLength {
		return ClusterObjectList{}, fmt.Errorf("object selector exceeds %d characters", maxObjectSelectorLength)
	}
	if len(options.Cursor) > maxObjectCursorLength {
		return ClusterObjectList{}, fmt.Errorf("object cursor exceeds %d characters", maxObjectCursorLength)
	}
	configuration, err := checkSelectorTerms(selector)
	if err != nil {
		return ClusterObjectList{}, err
	}

	limit := options.Limit
	if limit == 0 {
		limit = defaultListClusterObjectsLimit
	}
	if limit < 1 || limit > maxListClusterObjectsLimit {
		return ClusterObjectList{}, fmt.Errorf("object list limit must be between 1 and %d", maxListClusterObjectsLimit)
	}

	var paths daemonItems[string]
	if err := s.client.GetJSON(ctx, "/api/object/path", url.Values{"path": {selector}}, &paths); err != nil {
		return ClusterObjectList{}, fmt.Errorf("list cluster object paths: %w", err)
	}

	references := make([]ClusterObjectReference, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		reference, err := parseClusterObjectReference(path)
		if err != nil {
			return ClusterObjectList{}, fmt.Errorf("parse cluster object path %q: %w", path, err)
		}
		seen[path] = struct{}{}
		references = append(references, reference)
	}
	sort.Slice(references, func(i, j int) bool {
		return references[i].Path < references[j].Path
	})

	start := 0
	if options.Cursor != "" {
		start = sort.Search(len(references), func(i int) bool {
			return references[i].Path > options.Cursor
		})
	}
	end := min(start+limit, len(references))
	objects := append([]ClusterObjectReference(nil), references[start:end]...)
	if objects == nil {
		objects = []ClusterObjectReference{}
	}

	result := ClusterObjectList{
		Selector:  selector,
		Total:     len(references),
		Count:     len(objects),
		Objects:   objects,
		Truncated: end < len(references),
	}
	if result.Truncated {
		result.NextCursor = objects[len(objects)-1].Path
	}
	if configuration {
		result.TermNote = objectSelectorTermNote
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

func parseClusterObjectReference(path string) (ClusterObjectReference, error) {
	if path == "" {
		return ClusterObjectReference{}, fmt.Errorf("path is empty")
	}
	if path == "cluster" {
		return ClusterObjectReference{Path: path, Namespace: "root", Kind: "ccfg", Name: "cluster"}, nil
	}
	if path == "/" {
		return ClusterObjectReference{Path: path, Namespace: "root", Kind: "nscfg", Name: "namespace"}, nil
	}

	parts := strings.Split(path, "/")
	reference := ClusterObjectReference{Path: path}
	switch len(parts) {
	case 1:
		reference.Namespace = "root"
		reference.Kind = "svc"
		reference.Name = parts[0]
	case 2:
		if parts[0] == "" {
			return ClusterObjectReference{}, fmt.Errorf("namespace is empty")
		}
		if parts[1] == "" {
			reference.Namespace = parts[0]
			reference.Kind = "nscfg"
			reference.Name = "namespace"
		} else {
			reference.Namespace = "root"
			reference.Kind = parts[0]
			reference.Name = parts[1]
		}
	case 3:
		if parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return ClusterObjectReference{}, fmt.Errorf("path contains an empty component")
		}
		reference.Namespace = parts[0]
		reference.Kind = parts[1]
		reference.Name = parts[2]
	default:
		return ClusterObjectReference{}, fmt.Errorf("path has %d components", len(parts))
	}
	reference.Path = canonicalObjectPath(reference)
	return reference, nil
}

// canonicalObjectPath is the form the daemon gives the path of an object, as
// om3 naming.Path.String writes it: ns/kind/name, the root namespace left
// out, and there the svc and ccfg kinds too; a namespace configuration is
// ns/, or / for root. The daemon answers with it whatever form the request
// used, so the MCP compares its answers to this form.
func canonicalObjectPath(reference ClusterObjectReference) string {
	switch {
	case reference.Kind == "nscfg" && reference.Namespace == "root":
		return "/"
	case reference.Kind == "nscfg":
		return reference.Namespace + "/"
	case reference.Namespace != "root":
		return reference.Namespace + "/" + reference.Kind + "/" + reference.Name
	case reference.Kind == "svc" || reference.Kind == "ccfg":
		return reference.Name
	default:
		return reference.Kind + "/" + reference.Name
	}
}

// objectSelectorTermNote tells, beside a result of configuration terms, what
// the daemon could not see.
const objectSelectorTermNote = "a configuration term reads only the configurations the answering node holds: an object whose nodes do not include it is missing from this result, which does not prove that none exists"

// checkSelectorTerms refuses the configuration terms of a selector that
// would disclose a value the MCP never returns, or select by a comparison
// the daemon evaluates wrong, and says whether the selector holds one. The
// daemon matches a term on any keyword, with = != < > <= >=, and the count
// of the objects matching answers one comparison: a term on a secret keyword
// or a data key, which no read returns, would give its value one comparison
// at a time. An order is not offered: om3 converts the values of a typed
// keyword, and compares those of the others as text. A negation of a term
// selects the objects whose configuration the answering node does not hold,
// as not matching. The selector is split as om3 splits it, on the commas,
// then on the +, a ! before a term negating it; om3 reads a term holding
// * ? [ or ] as a path pattern, and the operator of a term as its leftmost.
func checkSelectorTerms(selector string) (bool, error) {
	configuration := false
	for _, group := range strings.Split(selector, ",") {
		for _, term := range strings.Split(group, "+") {
			if strings.TrimSpace(term) == "" {
				return false, fmt.Errorf("object selector %q holds an empty term, around a + or a comma", selector)
			}
			if strings.TrimSpace(term) != term {
				return false, fmt.Errorf("object selector term %q begins or ends with a space: write keyword=value, with no space around a + or a comma", term)
			}
			positive := strings.TrimLeft(term, "!")
			if strings.ContainsAny(positive, "*?[]") {
				continue
			}
			at := strings.IndexAny(positive, "=<>")
			exists := at < 0 && strings.HasSuffix(positive, ":")
			if at < 0 && !exists {
				continue
			}
			configuration = true
			keyword := strings.TrimSuffix(positive, ":")
			if !exists {
				keyword = strings.TrimRight(positive[:at], "!-|^")
			}
			if strings.TrimSpace(keyword) != keyword || (!exists && strings.HasPrefix(positive[at+1:], " ")) {
				return false, fmt.Errorf("object selector term %q holds a space around its keyword or its operator: write keyword=value", term)
			}
			if positive != term {
				return false, fmt.Errorf("object selector term %q negates a configuration term, which would select the objects whose configuration the answering node does not hold: use keyword!=value", term)
			}
			if !exists && strings.ContainsAny(positive[at:at+1], "<>") {
				return false, fmt.Errorf("object selector term %q compares an order, not offered: om3 converts the values of a typed keyword and compares the others as text; use keyword=value or keyword!=value", term)
			}
			if isSecretKeyword(keyword) {
				return false, fmt.Errorf("object selector term %q names a secret keyword, whose value no tool reads", term)
			}
			if isDataKeyword(keyword) {
				return false, fmt.Errorf("object selector term %q names a data key: list_object_data_keys gives the keys of an object, get_object_data_key the value of a cfg key", term)
			}
			if base, scope, scoped := strings.Cut(keyword, "@"); scoped && !strings.Contains(base, ".") && strings.Contains(scope, ".") {
				return false, fmt.Errorf("object selector term %q scopes a keyword without its section: write the section, such as DEFAULT.%s", term, keyword)
			}
		}
	}
	return configuration, nil
}
