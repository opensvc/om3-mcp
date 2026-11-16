package core

import (
	"context"
	"fmt"
	"net/url"
	"sort"
)

const (
	maxNodeSANInitiators = 512
	maxNodeSANPaths      = 4096
	maxNodeSANTextRunes  = 1024
)

type GetNodeSANTopologyOptions struct {
	Node string
}

type NodeSANTopology struct {
	Provenance          Provenance     `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node                string         `json:"node" jsonschema:"the exact requested OpenSVC node name, reported by every entry"`
	Initiators          []SANInitiator `json:"initiators" jsonschema:"the SAN initiators of the node, such as iSCSI initiators or HBA ports, sorted by type then name"`
	InitiatorsTruncated bool           `json:"initiators_truncated" jsonschema:"whether initiators were omitted after 512 entries"`
	Paths               []SANPath      `json:"paths" jsonschema:"the initiator to target pairs the node sees, sorted by initiator then target"`
	PathsTruncated      bool           `json:"paths_truncated" jsonschema:"whether paths were omitted after 4096 entries"`
}

type SANInitiator struct {
	Name        string `json:"name" jsonschema:"the worldwide unique initiator identifier, such as an iSCSI IQN or a port WWN"`
	Type        string `json:"type" jsonschema:"the initiator type reported by the inventory, such as iscsi or fc"`
	TargetCount int    `json:"target_count" jsonschema:"the number of distinct targets this initiator reaches in the paths"`
}

type SANPath struct {
	Initiator SANEndpoint `json:"initiator" jsonschema:"the initiator end of the path"`
	Target    SANEndpoint `json:"target" jsonschema:"the target end of the path"`
}

type SANEndpoint struct {
	Name string `json:"name" jsonschema:"the worldwide unique endpoint identifier"`
	Type string `json:"type" jsonschema:"the endpoint type reported by the inventory"`
}

type daemonSANEndpoint struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type daemonSANInitiatorList struct {
	Kind  string `json:"kind"`
	Items []struct {
		Kind string `json:"kind"`
		Meta struct {
			Node string `json:"node"`
		} `json:"meta"`
		Data daemonSANEndpoint `json:"data"`
	} `json:"items"`
}

// The path items carry no kind nor node: they are the bare pairs.
type daemonSANPathList struct {
	Kind  string `json:"kind"`
	Items []struct {
		Initiator daemonSANEndpoint `json:"initiator"`
		Target    daemonSANEndpoint `json:"target"`
	} `json:"items"`
}

// GetNodeSANTopology reads the SAN initiators and the initiator to target
// pairs the node caches on its push asset schedule. The inventory holds the
// topology, not the state of the paths.
func (s *Service) GetNodeSANTopology(ctx context.Context, options GetNodeSANTopologyOptions) (NodeSANTopology, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return NodeSANTopology{}, err
	}

	var initiatorList daemonSANInitiatorList
	if err := s.client.GetJSON(ctx, fmt.Sprintf("/api/node/name/%s/system/san/initiator", node), url.Values{}, &initiatorList); err != nil {
		return NodeSANTopology{}, fmt.Errorf("get node SAN initiators: %w", err)
	}
	if initiatorList.Kind != "SANPathInitiatorList" {
		return NodeSANTopology{}, shapeErrorf("get node SAN initiators: unexpected response kind %q", initiatorList.Kind)
	}
	var pathList daemonSANPathList
	if err := s.client.GetJSON(ctx, fmt.Sprintf("/api/node/name/%s/system/san/path", node), url.Values{}, &pathList); err != nil {
		return NodeSANTopology{}, fmt.Errorf("get node SAN paths: %w", err)
	}
	if pathList.Kind != "SANPathList" {
		return NodeSANTopology{}, shapeErrorf("get node SAN paths: unexpected response kind %q", pathList.Kind)
	}

	initiators := make([]SANInitiator, 0, len(initiatorList.Items))
	for index, raw := range initiatorList.Items {
		if raw.Kind != "SANPathInitiatorItem" {
			return NodeSANTopology{}, shapeErrorf("get node SAN initiators: item %d has unexpected kind %q", index, raw.Kind)
		}
		if !validExactNodeName(raw.Meta.Node) {
			return NodeSANTopology{}, fmt.Errorf("get node SAN initiators: item %d reports invalid node %q", index, raw.Meta.Node)
		}
		if raw.Meta.Node != node {
			return NodeSANTopology{}, fmt.Errorf("get node SAN initiators: item %d reports unexpected node %q", index, raw.Meta.Node)
		}
		endpoint := projectSANEndpoint(raw.Data)
		initiators = append(initiators, SANInitiator{Name: endpoint.Name, Type: endpoint.Type})
	}

	paths := make([]SANPath, 0, len(pathList.Items))
	targets := make(map[string]map[string]struct{})
	for _, raw := range pathList.Items {
		path := SANPath{Initiator: projectSANEndpoint(raw.Initiator), Target: projectSANEndpoint(raw.Target)}
		paths = append(paths, path)
		if targets[path.Initiator.Name] == nil {
			targets[path.Initiator.Name] = make(map[string]struct{})
		}
		targets[path.Initiator.Name][path.Target.Name] = struct{}{}
	}
	for i := range initiators {
		initiators[i].TargetCount = len(targets[initiators[i].Name])
	}

	sort.Slice(initiators, func(i, j int) bool {
		if initiators[i].Type != initiators[j].Type {
			return initiators[i].Type < initiators[j].Type
		}
		return initiators[i].Name < initiators[j].Name
	})
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].Initiator.Name != paths[j].Initiator.Name {
			return paths[i].Initiator.Name < paths[j].Initiator.Name
		}
		return paths[i].Target.Name < paths[j].Target.Name
	})
	result := NodeSANTopology{
		Node:                node,
		Initiators:          initiators[:min(len(initiators), maxNodeSANInitiators)],
		InitiatorsTruncated: len(initiators) > maxNodeSANInitiators,
		Paths:               paths[:min(len(paths), maxNodeSANPaths)],
		PathsTruncated:      len(paths) > maxNodeSANPaths,
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

func projectSANEndpoint(raw daemonSANEndpoint) SANEndpoint {
	name, _ := boundedRunes(raw.Name, maxNodeSANTextRunes)
	kind, _ := boundedRunes(raw.Type, maxNodeSANTextRunes)
	return SANEndpoint{Name: name, Type: kind}
}
