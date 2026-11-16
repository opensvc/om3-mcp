package core

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

const (
	defaultListNetworkIPsLimit = 100
	maxListNetworkIPsLimit     = 200
	maxNetworkIPItems          = 20000
)

type ListNetworkIPsOptions struct {
	Network    string
	Node       string
	Path       string
	SharedOnly bool
	Limit      int
	Cursor     string
}

type NetworkIPList struct {
	Provenance    Provenance  `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	NetworkFilter string      `json:"network_filter,omitempty" jsonschema:"the optional exact network name used for the daemon request"`
	NodeFilter    string      `json:"node_filter,omitempty" jsonschema:"the optional exact node name filter applied"`
	PathFilter    string      `json:"path_filter,omitempty" jsonschema:"the optional exact object path filter applied"`
	SharedOnly    bool        `json:"shared_only" jsonschema:"whether only the addresses reported by more than one object resource are listed"`
	ReportedTotal int         `json:"reported_total" jsonschema:"the number of address entries the daemon returned before filtering"`
	Total         int         `json:"total" jsonschema:"the number of address entries matching the filters"`
	Count         int         `json:"count" jsonschema:"the number of address entries returned in this page"`
	IPs           []NetworkIP `json:"ips" jsonschema:"the address entries sorted by network, address, object path, resource and node"`
	NextCursor    string      `json:"next_cursor,omitempty" jsonschema:"the opaque cursor to pass with the same filters for the next page"`
	Truncated     bool        `json:"truncated" jsonschema:"whether matching address entries remain after this page"`
}

type NetworkIP struct {
	IP             string           `json:"ip" jsonschema:"the address reported by the resource"`
	Node           string           `json:"node" jsonschema:"the node of the instance reporting the address; the instances of a failover object on several nodes each report it"`
	Path           string           `json:"path" jsonschema:"the OpenSVC object path"`
	RID            string           `json:"rid" jsonschema:"the resource identifier"`
	Network        NetworkReference `json:"network" jsonschema:"the network whose range holds the address"`
	OtherResources int              `json:"other_resources" jsonschema:"the number of other object and resource pairs reporting the same address in the daemon response; 0 when only this resource reports it, whatever the number of nodes"`
}

type NetworkReference struct {
	Name    string `json:"name" jsonschema:"the network name"`
	Type    string `json:"type" jsonschema:"the network driver type"`
	Network string `json:"network" jsonschema:"the network address range in CIDR notation"`
}

type daemonNetworkIPList struct {
	Kind  string            `json:"kind"`
	Items []daemonNetworkIP `json:"items"`
}

type daemonNetworkIP struct {
	IP      string           `json:"ip"`
	Node    string           `json:"node"`
	Path    string           `json:"path"`
	RID     string           `json:"rid"`
	Network NetworkReference `json:"network"`
}

type networkIPRecord struct {
	entry  NetworkIP
	object ClusterObjectReference
	key    string
}

// ListNetworkIPs reads the addresses the cluster instances report in the
// backend network ranges, and counts for each one the other object resources
// reporting it. It reports facts, not conflicts.
func (s *Service) ListNetworkIPs(ctx context.Context, options ListNetworkIPsOptions) (NetworkIPList, error) {
	network, err := validateNetworkName(options.Network)
	if err != nil {
		return NetworkIPList{}, err
	}
	node := options.Node
	if node != "" {
		if err := validateNodeTarget(node); err != nil {
			return NetworkIPList{}, err
		}
	}
	var object *ClusterObjectReference
	if options.Path != "" {
		if strings.ContainsAny(options.Path, "*?[],\\") {
			return NetworkIPList{}, fmt.Errorf("path must be one exact OpenSVC object path, without wildcard or selector")
		}
		reference, err := validateExactObjectPath(options.Path)
		if err != nil {
			return NetworkIPList{}, err
		}
		object = &reference
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultListNetworkIPsLimit
	}
	if limit < 1 || limit > maxListNetworkIPsLimit {
		return NetworkIPList{}, fmt.Errorf("network IP list limit must be between 1 and %d", maxListNetworkIPsLimit)
	}
	cursorKey, err := decodeNetworkIPCursor(options.Cursor)
	if err != nil {
		return NetworkIPList{}, err
	}

	query := url.Values{}
	if network != "" {
		query.Set("name", network)
	}
	var response daemonNetworkIPList
	if err := s.client.GetJSON(ctx, "/api/network/ip", query, &response); err != nil {
		return NetworkIPList{}, fmt.Errorf("list network IPs: %w", err)
	}
	if response.Kind != "NetworkIPList" {
		return NetworkIPList{}, shapeErrorf("list network IPs: unexpected response kind %q", response.Kind)
	}
	if len(response.Items) > maxNetworkIPItems {
		return NetworkIPList{}, fmt.Errorf("list network IPs: response contains %d items, limit is %d", len(response.Items), maxNetworkIPItems)
	}

	records := make([]networkIPRecord, 0, len(response.Items))
	resources := make(map[string]map[string]struct{})
	for index, item := range response.Items {
		record, err := projectNetworkIP(item)
		if err != nil {
			return NetworkIPList{}, fmt.Errorf("list network IPs: item %d: %w", index, err)
		}
		if resources[record.entry.IP] == nil {
			resources[record.entry.IP] = make(map[string]struct{})
		}
		resources[record.entry.IP][record.entry.Path+"\x00"+record.entry.RID] = struct{}{}
		records = append(records, record)
	}

	matching := make([]networkIPRecord, 0, len(records))
	for _, record := range records {
		record.entry.OtherResources = len(resources[record.entry.IP]) - 1
		if node != "" && record.entry.Node != node || object != nil && !sameObject(record.object, *object) || options.SharedOnly && record.entry.OtherResources == 0 {
			continue
		}
		matching = append(matching, record)
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].key < matching[j].key })
	start := sort.Search(len(matching), func(i int) bool { return matching[i].key > cursorKey })
	end := min(start+limit, len(matching))
	page := make([]NetworkIP, 0, end-start)
	for _, record := range matching[start:end] {
		page = append(page, record.entry)
	}
	result := NetworkIPList{
		NetworkFilter: network,
		NodeFilter:    node,
		PathFilter:    options.Path,
		SharedOnly:    options.SharedOnly,
		ReportedTotal: len(response.Items),
		Total:         len(matching),
		Count:         len(page),
		IPs:           page,
		Truncated:     end < len(matching),
	}
	if result.Truncated {
		result.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(matching[end-1].key))
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

func projectNetworkIP(item daemonNetworkIP) (networkIPRecord, error) {
	address, err := netip.ParseAddr(item.IP)
	if err != nil {
		return networkIPRecord{}, fmt.Errorf("invalid address %q", boundedNetworkText(item.IP))
	}
	if !validExactNodeName(item.Node) {
		return networkIPRecord{}, fmt.Errorf("invalid node %q", boundedNetworkText(item.Node))
	}
	reference, err := parseClusterObjectReference(item.Path)
	if err != nil {
		return networkIPRecord{}, fmt.Errorf("invalid object path %q", boundedNetworkText(item.Path))
	}
	entry := NetworkIP{
		IP:   address.String(),
		Node: item.Node,
		Path: reference.Path,
		RID:  boundedNetworkText(item.RID),
		Network: NetworkReference{
			Name:    boundedNetworkText(item.Network.Name),
			Type:    boundedNetworkText(item.Network.Type),
			Network: boundedNetworkText(item.Network.Network),
		},
	}
	// The address sorts by its 16-byte form, so that 10.0.0.9 precedes 10.0.0.10.
	bytes := address.As16()
	key := entry.Network.Name + "\x00" + hex.EncodeToString(bytes[:]) + "\x00" + entry.Path + "\x00" + entry.RID + "\x00" + entry.Node
	return networkIPRecord{entry: entry, object: reference, key: key}, nil
}

// sameObject compares two object references whatever the path spelling, so
// that svc1 and root/svc/svc1 name the same object.
func sameObject(a, b ClusterObjectReference) bool {
	return a.Namespace == b.Namespace && a.Kind == b.Kind && a.Name == b.Name
}

func decodeNetworkIPCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 4096 {
		return "", fmt.Errorf("network IP cursor exceeds 4096 characters")
	}
	value, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("invalid network IP cursor")
	}
	return string(value), nil
}
