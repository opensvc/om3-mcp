package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	defaultNodeDiskLimit = 100
	maxNodeDiskLimit     = 200
	maxNodeDiskItems     = 20000
	maxNodeDiskRegions   = 64
	maxNodeDiskTextRunes = 1024
	maxNodeDiskTypeRunes = 64
)

type ListNodeDisksOptions struct {
	Node          string
	Type          string
	ClaimedOnly   bool
	UnclaimedOnly bool
	Limit         int
	Cursor        string
}

type NodeDiskList struct {
	Provenance    Provenance `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node          string     `json:"node" jsonschema:"the exact requested OpenSVC node name, reported by every entry"`
	TypeFilter    string     `json:"type_filter,omitempty" jsonschema:"the optional exact disk type filter applied"`
	ClaimedOnly   bool       `json:"claimed_only" jsonschema:"whether only the disks an OpenSVC object claims are listed"`
	UnclaimedOnly bool       `json:"unclaimed_only" jsonschema:"whether only the disks no OpenSVC object claims are listed"`
	ReportedTotal int        `json:"reported_total" jsonschema:"the number of disks in the node inventory before filtering"`
	Total         int        `json:"total" jsonschema:"the number of disks matching the filters"`
	Count         int        `json:"count" jsonschema:"the number of disks returned in this page"`
	Disks         []NodeDisk `json:"disks" jsonschema:"the cached disk inventory entries sorted by type then disk id"`
	NextCursor    string     `json:"next_cursor,omitempty" jsonschema:"the opaque cursor to pass with the same node and filters for the next page"`
	Truncated     bool       `json:"truncated" jsonschema:"whether matching disks remain after this page"`
}

type NodeDisk struct {
	ID               string       `json:"id" jsonschema:"the disk identifier, such as the WWID of a multipath LUN or the kernel name of a local disk"`
	DevPath          string       `json:"devpath" jsonschema:"the device path of the disk"`
	SizeBytes        int64        `json:"size_bytes" jsonschema:"the disk size in bytes"`
	Vendor           string       `json:"vendor" jsonschema:"the disk vendor reported by the inventory, unmodified"`
	Model            string       `json:"model" jsonschema:"the disk model reported by the inventory, unmodified"`
	Type             string       `json:"type" jsonschema:"the disk type reported by the inventory, such as mpath, disk or rom"`
	Claimed          bool         `json:"claimed" jsonschema:"whether an OpenSVC object claims a region of the disk"`
	Regions          []DiskRegion `json:"regions" jsonschema:"the regions of the disk, with the OpenSVC object claiming each one if any"`
	RegionsTruncated bool         `json:"regions_truncated" jsonschema:"whether regions were omitted after 64 entries"`
}

type DiskRegion struct {
	ID        string `json:"id" jsonschema:"the region identifier"`
	DevPath   string `json:"devpath" jsonschema:"the device path of the region"`
	Object    string `json:"object" jsonschema:"the OpenSVC object path claiming the region; empty when none does"`
	Group     string `json:"group" jsonschema:"the device group of the claiming object, reported unmodified"`
	SizeBytes int64  `json:"size_bytes" jsonschema:"the region size in bytes"`
}

type daemonNodeDiskList struct {
	Kind  string               `json:"kind"`
	Items []daemonNodeDiskItem `json:"items"`
}

type daemonNodeDiskItem struct {
	Kind string `json:"kind"`
	Meta struct {
		Node string `json:"node"`
	} `json:"meta"`
	Data struct {
		ID      string `json:"id"`
		DevPath string `json:"devpath"`
		Size    int64  `json:"size"`
		Vendor  string `json:"vendor"`
		Model   string `json:"model"`
		Type    string `json:"type"`
		Regions []struct {
			ID      string `json:"id"`
			DevPath string `json:"devpath"`
			Object  string `json:"object"`
			Group   string `json:"group"`
			Size    int64  `json:"size"`
		} `json:"regions"`
	} `json:"data"`
}

// ListNodeDisks reads the disk inventory the node caches on its push disks
// schedule, with the OpenSVC objects claiming disk regions. It reports
// presence and claims, not path or health states, which the inventory lacks.
func (s *Service) ListNodeDisks(ctx context.Context, options ListNodeDisksOptions) (NodeDiskList, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return NodeDiskList{}, err
	}
	diskType := options.Type
	if diskType != "" && (len(diskType) > maxNodeDiskTypeRunes || strings.TrimSpace(diskType) != diskType || containsControl(diskType)) {
		return NodeDiskList{}, fmt.Errorf("type must be one exact disk type of at most %d characters", maxNodeDiskTypeRunes)
	}
	if options.ClaimedOnly && options.UnclaimedOnly {
		return NodeDiskList{}, fmt.Errorf("claimed_only and unclaimed_only are mutually exclusive")
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultNodeDiskLimit
	}
	if limit < 1 || limit > maxNodeDiskLimit {
		return NodeDiskList{}, fmt.Errorf("node disk list limit must be between 1 and %d", maxNodeDiskLimit)
	}
	cursorKey, err := decodeNodeDiskCursor(options.Cursor)
	if err != nil {
		return NodeDiskList{}, err
	}

	var response daemonNodeDiskList
	if err := s.client.GetJSON(ctx, fmt.Sprintf("/api/node/name/%s/system/disk", node), url.Values{}, &response); err != nil {
		return NodeDiskList{}, fmt.Errorf("list node disks: %w", err)
	}
	if response.Kind != "DiskList" {
		return NodeDiskList{}, shapeErrorf("list node disks: unexpected response kind %q", response.Kind)
	}
	if len(response.Items) > maxNodeDiskItems {
		return NodeDiskList{}, fmt.Errorf("list node disks: response contains %d items, limit is %d", len(response.Items), maxNodeDiskItems)
	}

	disks := make([]NodeDisk, 0, len(response.Items))
	for index, raw := range response.Items {
		if raw.Kind != "DiskItem" {
			return NodeDiskList{}, shapeErrorf("list node disks: item %d has unexpected kind %q", index, raw.Kind)
		}
		if !validExactNodeName(raw.Meta.Node) {
			return NodeDiskList{}, fmt.Errorf("list node disks: item %d reports invalid node %q", index, raw.Meta.Node)
		}
		if raw.Meta.Node != node {
			return NodeDiskList{}, fmt.Errorf("list node disks: item %d reports unexpected node %q", index, raw.Meta.Node)
		}
		disk := projectNodeDisk(raw)
		if diskType != "" && disk.Type != diskType || options.ClaimedOnly && !disk.Claimed || options.UnclaimedOnly && disk.Claimed {
			continue
		}
		disks = append(disks, disk)
	}

	sort.Slice(disks, func(i, j int) bool { return nodeDiskSortKey(disks[i]) < nodeDiskSortKey(disks[j]) })
	start := sort.Search(len(disks), func(i int) bool { return nodeDiskSortKey(disks[i]) > cursorKey })
	end := min(start+limit, len(disks))
	page := append([]NodeDisk{}, disks[start:end]...)
	result := NodeDiskList{
		Node:          node,
		TypeFilter:    diskType,
		ClaimedOnly:   options.ClaimedOnly,
		UnclaimedOnly: options.UnclaimedOnly,
		ReportedTotal: len(response.Items),
		Total:         len(disks),
		Count:         len(page),
		Disks:         page,
		Truncated:     end < len(disks),
	}
	if result.Truncated {
		result.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(nodeDiskSortKey(page[len(page)-1])))
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

func projectNodeDisk(raw daemonNodeDiskItem) NodeDisk {
	text := func(value string) string {
		bounded, _ := boundedRunes(value, maxNodeDiskTextRunes)
		return bounded
	}
	disk := NodeDisk{
		ID:        text(raw.Data.ID),
		DevPath:   text(raw.Data.DevPath),
		SizeBytes: raw.Data.Size,
		Vendor:    text(raw.Data.Vendor),
		Model:     text(raw.Data.Model),
		Type:      text(raw.Data.Type),
		Regions:   make([]DiskRegion, 0, min(len(raw.Data.Regions), maxNodeDiskRegions)),
	}
	for _, region := range raw.Data.Regions {
		disk.Claimed = disk.Claimed || region.Object != ""
		if len(disk.Regions) == maxNodeDiskRegions {
			disk.RegionsTruncated = true
			continue
		}
		disk.Regions = append(disk.Regions, DiskRegion{
			ID: text(region.ID), DevPath: text(region.DevPath), Object: text(region.Object),
			Group: text(region.Group), SizeBytes: region.Size,
		})
	}
	return disk
}

func nodeDiskSortKey(disk NodeDisk) string {
	return disk.Type + "\x00" + disk.ID + "\x00" + disk.DevPath
}

func decodeNodeDiskCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 1024 {
		return "", fmt.Errorf("node disk cursor exceeds 1024 characters")
	}
	value, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("invalid node disk cursor")
	}
	return string(value), nil
}
