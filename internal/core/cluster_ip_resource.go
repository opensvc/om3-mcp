package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const clusterIPResourcePathSelector = "*/svc/*,*/vol/*"

type ListClusterIPResourcesOptions struct {
	Path   string
	Node   string
	Limit  int
	Cursor string
}

type ClusterIPResourceList struct {
	Provenance Provenance          `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	PathFilter string              `json:"path_filter,omitempty" jsonschema:"the optional exact OpenSVC object path used for the daemon request"`
	NodeFilter string              `json:"node_filter,omitempty" jsonschema:"the optional exact OpenSVC node name used for the daemon request"`
	Total      int                 `json:"total" jsonschema:"the current number of visible IP resources matching the filters"`
	Count      int                 `json:"count" jsonschema:"the number of IP resources returned in this page"`
	Resources  []ClusterIPResource `json:"resources" jsonschema:"the IP resource records sorted by node, object path, encapsulated node, and resource id"`
	NextCursor string              `json:"next_cursor,omitempty" jsonschema:"the opaque cursor to pass to retrieve the next page"`
	Truncated  bool                `json:"truncated" jsonschema:"whether more matching IP resources remain after this page"`
}

type ClusterIPResource struct {
	Object    ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference owning this IP resource"`
	Node      string                 `json:"node" jsonschema:"the instance node name reported by OpenSVC"`
	EncapNode string                 `json:"encap_node,omitempty" jsonschema:"the encapsulated node name when reported by OpenSVC"`
	RID       string                 `json:"rid" jsonschema:"the OpenSVC IP resource identifier"`
	Type      string                 `json:"type" jsonschema:"the OpenSVC IP resource driver type"`
	Label     string                 `json:"label" jsonschema:"the resource label reported by OpenSVC"`
	Status    string                 `json:"status" jsonschema:"the last-known resource availability status reported by OpenSVC"`
	Info      IPResourceInfo         `json:"info" jsonschema:"the IP driver facts reported by OpenSVC without diagnostic interpretation"`
}

type IPResourceInfo struct {
	IPAddr   string   `json:"ipaddr,omitempty" jsonschema:"the IP address reported by the OpenSVC IP driver"`
	Dev      string   `json:"dev,omitempty" jsonschema:"the network device reported by the OpenSVC IP driver"`
	Netmask  *int     `json:"netmask,omitempty" jsonschema:"the network prefix length reported by the OpenSVC IP driver"`
	Expose   []string `json:"expose" jsonschema:"the exposure declarations reported by the OpenSVC IP driver"`
	Hostname string   `json:"hostname,omitempty" jsonschema:"the hostname reported by the OpenSVC IP driver"`
}

func (s *Service) ListClusterIPResources(ctx context.Context, options ListClusterIPResourcesOptions) (ClusterIPResourceList, error) {
	path := strings.TrimSpace(options.Path)
	if path != "" {
		if hasSelectorCharacters(path) {
			return ClusterIPResourceList{}, fmt.Errorf("path must be one exact canonical OpenSVC object path")
		}
		reference, err := validateExactObjectPath(path)
		if err != nil {
			return ClusterIPResourceList{}, err
		}
		// The daemon names the object of a resource in the canonical form.
		path = reference.Path
	}
	node := strings.TrimSpace(options.Node)
	if node != "" {
		if err := validateNodeTarget(node); err != nil {
			return ClusterIPResourceList{}, err
		}
	}

	limit := options.Limit
	if limit == 0 {
		limit = defaultListObjectResourcesLimit
	}
	if limit < 1 || limit > maxListObjectResourcesLimit {
		return ClusterIPResourceList{}, fmt.Errorf("IP resource list limit must be between 1 and %d", maxListObjectResourcesLimit)
	}
	cursorKey, err := decodeResourceCursor(options.Cursor)
	if err != nil {
		return ClusterIPResourceList{}, err
	}

	query := url.Values{
		"path":     {clusterIPResourcePathSelector},
		"resource": {"ip#*"},
	}
	if path != "" {
		query.Set("path", path)
	}
	if node != "" {
		query.Set("node", node)
	}
	var response daemonResourceList
	if err := s.client.GetJSON(ctx, "/api/resource", query, &response); err != nil {
		return ClusterIPResourceList{}, fmt.Errorf("list cluster IP resources: %w", err)
	}

	resources := make([]ClusterIPResource, 0, len(response.Items))
	for _, item := range response.Items {
		if item.Data.Status == nil || !strings.HasPrefix(item.Data.Status.Type, "ip.") {
			continue
		}
		if path != "" && item.Meta.Object != path {
			continue
		}
		if node != "" && item.Meta.Node != node {
			continue
		}
		object, err := parseClusterObjectReference(item.Meta.Object)
		if err != nil {
			return ClusterIPResourceList{}, fmt.Errorf("parse IP resource object path %q: %w", item.Meta.Object, err)
		}
		info := item.Data.Status.Info
		if info.Expose == nil {
			info.Expose = []string{}
		}
		resources = append(resources, ClusterIPResource{
			Object:    object,
			Node:      item.Meta.Node,
			EncapNode: item.Meta.EncapNode,
			RID:       item.Meta.RID,
			Type:      item.Data.Status.Type,
			Label:     item.Data.Status.Label,
			Status:    item.Data.Status.Status,
			Info:      info,
		})
	}

	sort.Slice(resources, func(i, j int) bool {
		return clusterIPResourceSortKey(resources[i]) < clusterIPResourceSortKey(resources[j])
	})
	start := sort.Search(len(resources), func(i int) bool {
		return clusterIPResourceSortKey(resources[i]) > cursorKey
	})
	end := min(start+limit, len(resources))
	page := append([]ClusterIPResource{}, resources[start:end]...)
	if page == nil {
		page = []ClusterIPResource{}
	}
	result := ClusterIPResourceList{
		PathFilter: path,
		NodeFilter: node,
		Total:      len(resources),
		Count:      len(page),
		Resources:  page,
		Truncated:  end < len(resources),
	}
	if result.Truncated {
		result.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(clusterIPResourceSortKey(page[len(page)-1])))
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

func clusterIPResourceSortKey(resource ClusterIPResource) string {
	return resource.Node + "\x00" + resource.Object.Path + "\x00" + resource.EncapNode + "\x00" + resource.RID
}
