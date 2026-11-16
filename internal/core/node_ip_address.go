package core

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

const (
	maxNodeIPAddressItems     = 4096
	maxNodeIPAddressTextRunes = 255
)

type ListNodeIPAddressesOptions struct {
	Node        string
	Interface   string
	Family      string
	UnicastOnly bool
}

type NodeIPAddressList struct {
	Provenance      Provenance      `json:"provenance" jsonschema:"API source and MCP collection time of this result; the addresses come from a cache whose date the API does not give"`
	Node            string          `json:"node" jsonschema:"the exact requested OpenSVC node name, reported by every entry"`
	InterfaceFilter string          `json:"interface_filter,omitempty" jsonschema:"the optional exact interface filter applied"`
	FamilyFilter    string          `json:"family_filter,omitempty" jsonschema:"the optional address family filter applied"`
	UnicastOnly     bool            `json:"unicast_only" jsonschema:"whether multicast group memberships and link-local addresses were left out"`
	ReportedTotal   int             `json:"reported_total" jsonschema:"the number of addresses in the cache before filtering"`
	Total           int             `json:"total" jsonschema:"the number of addresses matching the filters"`
	Addresses       []NodeIPAddress `json:"addresses" jsonschema:"the cached addresses sorted by interface then address"`
}

type NodeIPAddress struct {
	Interface  string `json:"interface" jsonschema:"the network interface holding the address when the cache was written"`
	Address    string `json:"address" jsonschema:"the address"`
	PrefixLen  string `json:"prefix_length" jsonschema:"the prefix length as reported, such as 24 or 64; empty for a multicast group membership"`
	Family     string `json:"family" jsonschema:"ipv4 or ipv6, as reported"`
	MAC        string `json:"mac" jsonschema:"the hardware address of the interface"`
	Deprecated bool   `json:"deprecated" jsonschema:"whether the address was flagged deprecated, such as an IPv6 address past its preferred lifetime"`
}

type daemonNodeIPAddressList struct {
	Kind  string                    `json:"kind"`
	Items []daemonNodeIPAddressItem `json:"items"`
}

type daemonNodeIPAddressItem struct {
	Kind string `json:"kind"`
	Meta struct {
		Node string `json:"node"`
	} `json:"meta"`
	Data struct {
		Address        string `json:"address"`
		FlagDeprecated bool   `json:"flagdeprecated"`
		Intf           string `json:"intf"`
		MAC            string `json:"mac"`
		Mask           string `json:"mask"`
		Type           string `json:"type"`
	} `json:"data"`
}

// ListNodeIPAddresses reads the addresses of the node interfaces from the
// system cache the node writes on its push asset schedule. It is a snapshot:
// the live placement of an OpenSVC address is in its resource status.
func (s *Service) ListNodeIPAddresses(ctx context.Context, options ListNodeIPAddressesOptions) (NodeIPAddressList, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return NodeIPAddressList{}, err
	}
	intf := options.Interface
	if intf != "" && (len(intf) > maxNodeIPAddressTextRunes || strings.TrimSpace(intf) != intf || strings.ContainsAny(intf, "*?[]/ ") || containsControl(intf)) {
		return NodeIPAddressList{}, fmt.Errorf("interface must be one exact interface name of at most %d characters, without wildcard", maxNodeIPAddressTextRunes)
	}
	family := strings.ToLower(options.Family)
	if family != "" && family != "ipv4" && family != "ipv6" {
		return NodeIPAddressList{}, fmt.Errorf("family must be ipv4 or ipv6")
	}

	var response daemonNodeIPAddressList
	if err := s.client.GetJSON(ctx, fmt.Sprintf("/api/node/name/%s/system/ipaddress", node), url.Values{}, &response); err != nil {
		return NodeIPAddressList{}, fmt.Errorf("list node IP addresses: %w", err)
	}
	if response.Kind != "IPAddressList" {
		return NodeIPAddressList{}, shapeErrorf("list node IP addresses: unexpected response kind %q", response.Kind)
	}
	if len(response.Items) > maxNodeIPAddressItems {
		return NodeIPAddressList{}, fmt.Errorf("list node IP addresses: response contains %d items, limit is %d", len(response.Items), maxNodeIPAddressItems)
	}

	type entry struct {
		address NodeIPAddress
		parsed  netip.Addr
	}
	entries := make([]entry, 0, len(response.Items))
	for index, raw := range response.Items {
		if raw.Kind != "IPAddressItem" {
			return NodeIPAddressList{}, shapeErrorf("list node IP addresses: item %d has unexpected kind %q", index, raw.Kind)
		}
		if raw.Meta.Node != node {
			return NodeIPAddressList{}, fmt.Errorf("list node IP addresses: item %d reports unexpected node %q", index, boundedNodeIPText(raw.Meta.Node))
		}
		parsed, err := netip.ParseAddr(raw.Data.Address)
		if err != nil {
			return NodeIPAddressList{}, fmt.Errorf("list node IP addresses: item %d has invalid address %q", index, boundedNodeIPText(raw.Data.Address))
		}
		if intf != "" && raw.Data.Intf != intf || family != "" && raw.Data.Type != family ||
			options.UnicastOnly && (parsed.IsMulticast() || parsed.IsLinkLocalUnicast()) {
			continue
		}
		entries = append(entries, entry{
			address: NodeIPAddress{
				Interface:  boundedNodeIPText(raw.Data.Intf),
				Address:    parsed.String(),
				PrefixLen:  boundedNodeIPText(raw.Data.Mask),
				Family:     boundedNodeIPText(raw.Data.Type),
				MAC:        boundedNodeIPText(raw.Data.MAC),
				Deprecated: raw.Data.FlagDeprecated,
			},
			parsed: parsed,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].address.Interface != entries[j].address.Interface {
			return entries[i].address.Interface < entries[j].address.Interface
		}
		return entries[i].parsed.Less(entries[j].parsed)
	})
	addresses := make([]NodeIPAddress, 0, len(entries))
	for _, entry := range entries {
		addresses = append(addresses, entry.address)
	}
	return NodeIPAddressList{
		Provenance:      s.newProvenance(),
		Node:            node,
		InterfaceFilter: intf,
		FamilyFilter:    family,
		UnicastOnly:     options.UnicastOnly,
		ReportedTotal:   len(response.Items),
		Total:           len(addresses),
		Addresses:       addresses,
	}, nil
}

func boundedNodeIPText(value string) string {
	bounded, _ := boundedRunes(value, maxNodeIPAddressTextRunes)
	return bounded
}
