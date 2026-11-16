package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"sort"
)

const (
	maxNetworks             = 512
	maxNetworkErrors        = 10
	maxNetworkErrorRunes    = 512
	maxNetworkTextRunes     = 1024
	maxNetworkNameRunes     = 255
	maxNetworkCountDigits   = 64
	networkSizeUnknownValue = ""
)

type ListNetworksOptions struct {
	Name string
}

type NetworkList struct {
	Provenance Provenance `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	NameFilter string     `json:"name_filter,omitempty" jsonschema:"the optional exact network name used for the daemon request"`
	Total      int        `json:"total" jsonschema:"the number of networks returned by the daemon"`
	Count      int        `json:"count" jsonschema:"the number of networks included in this result"`
	Networks   []Network  `json:"networks" jsonschema:"the cluster backend networks sorted by name"`
	Truncated  bool       `json:"truncated" jsonschema:"whether networks were omitted after the 512-network limit"`
}

type Network struct {
	Name            string   `json:"name" jsonschema:"the network name"`
	Type            string   `json:"type" jsonschema:"the network driver type reported by OpenSVC, such as bridge, routed_bridge or lo"`
	Network         string   `json:"network" jsonschema:"the network address range in CIDR notation"`
	Used            int64    `json:"used" jsonschema:"the number of node, object and resource entries reporting an address in the range; an address held by the instances of several nodes counts once per node"`
	SizeAddresses   string   `json:"size_addresses" jsonschema:"the number of addresses in the range as a decimal string, network and broadcast addresses included; empty when the daemon cannot compute it"`
	FreeAddresses   string   `json:"free_addresses" jsonschema:"size_addresses minus used, as a decimal string; empty when the daemon cannot compute it"`
	Errors          []string `json:"errors" jsonschema:"the network errors reported by OpenSVC, each limited to 512 characters"`
	ErrorsTruncated bool     `json:"errors_truncated" jsonschema:"whether errors were omitted after 10 entries or shortened"`
}

type daemonNetworkList struct {
	Kind  string          `json:"kind"`
	Items []daemonNetwork `json:"items"`
}

type daemonNetwork struct {
	Name    string      `json:"name"`
	Type    string      `json:"type"`
	Network string      `json:"network"`
	Used    json.Number `json:"used"`
	Size    json.Number `json:"size"`
	Free    json.Number `json:"free"`
	Errors  []string    `json:"errors"`
}

// ListNetworks reads the cluster backend networks the daemon receiving the
// request knows, with the addresses the cluster instances report in each
// range. It reports facts only.
func (s *Service) ListNetworks(ctx context.Context, options ListNetworksOptions) (NetworkList, error) {
	name, err := validateNetworkName(options.Name)
	if err != nil {
		return NetworkList{}, err
	}
	query := url.Values{}
	if name != "" {
		query.Set("name", name)
	}
	var response daemonNetworkList
	if err := s.client.GetJSON(ctx, "/api/network", query, &response); err != nil {
		return NetworkList{}, fmt.Errorf("list networks: %w", err)
	}
	if response.Kind != "NetworkList" {
		return NetworkList{}, shapeErrorf("list networks: unexpected response kind %q", response.Kind)
	}

	networks := make([]Network, 0, min(len(response.Items), maxNetworks))
	for index, item := range response.Items {
		network, err := projectNetwork(item)
		if err != nil {
			return NetworkList{}, fmt.Errorf("list networks: item %d: %w", index, err)
		}
		networks = append(networks, network)
	}
	sort.SliceStable(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })
	total := len(networks)
	if total > maxNetworks {
		networks = networks[:maxNetworks]
	}
	return NetworkList{
		Provenance: s.newProvenance(),
		NameFilter: name,
		Total:      total,
		Count:      len(networks),
		Networks:   networks,
		Truncated:  total > len(networks),
	}, nil
}

func projectNetwork(item daemonNetwork) (Network, error) {
	used, err := networkCount(item.Used, "used")
	if err != nil {
		return Network{}, err
	}
	if !used.IsInt64() {
		return Network{}, fmt.Errorf("used exceeds the int64 range")
	}
	size, err := networkCount(item.Size, "size")
	if err != nil {
		return Network{}, err
	}
	free, err := networkCount(item.Free, "free")
	if err != nil {
		return Network{}, err
	}
	errors, errorsTruncated := boundedStrings(item.Errors, maxNetworkErrors)
	for i, message := range errors {
		var shortened bool
		errors[i], shortened = boundedRunes(message, maxNetworkErrorRunes)
		errorsTruncated = errorsTruncated || shortened
	}
	return Network{
		Name:            boundedNetworkText(item.Name),
		Type:            boundedNetworkText(item.Type),
		Network:         boundedNetworkText(item.Network),
		Used:            used.Int64(),
		SizeAddresses:   networkCountText(size),
		FreeAddresses:   networkCountText(free),
		Errors:          errors,
		ErrorsTruncated: errorsTruncated,
	}, nil
}

// networkCount parses an address count. The daemon encodes it as an
// arbitrary precision integer: an IPv6 range exceeds the int64 range. An
// absent count, which the daemon reports when it cannot parse the range,
// is nil.
func networkCount(value json.Number, field string) (*big.Int, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > maxNetworkCountDigits {
		return nil, fmt.Errorf("%s exceeds %d digits", field, maxNetworkCountDigits)
	}
	count, ok := new(big.Int).SetString(value.String(), 10)
	if !ok || count.Sign() < 0 {
		return nil, fmt.Errorf("%s is not a non-negative integer", field)
	}
	return count, nil
}

func networkCountText(count *big.Int) string {
	if count == nil {
		return networkSizeUnknownValue
	}
	return count.String()
}

// boundedNetworkText keeps a daemon text field within the network text limit.
func boundedNetworkText(value string) string {
	bounded, _ := boundedRunes(value, maxNetworkTextRunes)
	return bounded
}

// validateNetworkName accepts an empty name or one exact network name: the
// daemon matches the name as given.
func validateNetworkName(value string) (string, error) {
	name, err := validateExactName(value, maxNetworkNameRunes)
	if err != nil {
		return "", fmt.Errorf("network name must be one exact OpenSVC network name of at most %d characters, without wildcard or selector", maxNetworkNameRunes)
	}
	return name, nil
}
