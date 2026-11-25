package core

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

type ObjectStatus struct {
	Provenance       Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object           ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference"`
	Availability     string                 `json:"availability,omitempty" jsonschema:"the aggregate object availability reported by OpenSVC"`
	Overall          string                 `json:"overall,omitempty" jsonschema:"the aggregate object overall status reported by OpenSVC"`
	Provisioned      string                 `json:"provisioned,omitempty" jsonschema:"the aggregate object provisioned state reported by OpenSVC"`
	Frozen           string                 `json:"frozen,omitempty" jsonschema:"the aggregate object freeze state reported by OpenSVC"`
	PlacementState   string                 `json:"placement_state,omitempty" jsonschema:"the aggregate object placement state reported by OpenSVC"`
	PlacementPolicy  string                 `json:"placement_policy,omitempty" jsonschema:"the configured object placement policy"`
	Orchestrate      string                 `json:"orchestrate,omitempty" jsonschema:"the configured object orchestration mode"`
	Topology         string                 `json:"topology,omitempty" jsonschema:"the configured object topology"`
	Priority         int                    `json:"priority" jsonschema:"the object orchestration priority"`
	Scope            []string               `json:"scope" jsonschema:"the sorted node names in the object scope"`
	UpdatedAt        string                 `json:"updated_at" jsonschema:"the last object status update timestamp reported by OpenSVC; use it to assess freshness"`
	UpInstancesCount int                    `json:"up_instances_count" jsonschema:"the aggregate number of up instances reported by OpenSVC"`
	InstanceCount    int                    `json:"instance_count" jsonschema:"the number of instances included in the object status"`
	InstanceNodes    []string               `json:"instance_nodes" jsonschema:"the sorted node names with an object instance"`
}

type daemonObjectList struct {
	Items []daemonObjectItem `json:"items"`
}

type daemonObjectItem struct {
	Meta struct {
		Object string `json:"object"`
	} `json:"meta"`
	Data daemonObjectData `json:"data"`
}

type daemonObjectData struct {
	Availability     *string                   `json:"avail"`
	Overall          string                    `json:"overall"`
	Provisioned      string                    `json:"provisioned"`
	Frozen           string                    `json:"frozen"`
	PlacementState   string                    `json:"placement_state"`
	PlacementPolicy  string                    `json:"placement_policy"`
	Orchestrate      string                    `json:"orchestrate"`
	Topology         string                    `json:"topology"`
	Priority         int                       `json:"priority"`
	Scope            []string                  `json:"scope"`
	UpdatedAt        string                    `json:"updated_at"`
	UpInstancesCount int                       `json:"up_instances_count"`
	Instances        map[string]daemonInstance `json:"instances"`
}

func (s *Service) GetObjectStatus(ctx context.Context, path string) (ObjectStatus, error) {
	reference, err := validateExactObjectPath(path)
	if err != nil {
		return ObjectStatus{}, err
	}

	var response daemonObjectList
	if err := s.client.GetJSON(ctx, "/api/object", url.Values{"path": {reference.Path}}, &response); err != nil {
		return ObjectStatus{}, fmt.Errorf("get object status: %w", err)
	}
	if len(response.Items) == 0 {
		return ObjectStatus{}, fmt.Errorf("object %q was not found or is not visible to the caller", reference.Path)
	}
	if len(response.Items) != 1 || response.Items[0].Meta.Object != reference.Path {
		return ObjectStatus{}, fmt.Errorf("object status returned an unexpected selection for %q", reference.Path)
	}

	data := response.Items[0].Data
	nodes := make([]string, 0, len(data.Instances))
	for node := range data.Instances {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	scope := append([]string{}, data.Scope...)
	sort.Strings(scope)

	result := ObjectStatus{
		Object:           reference,
		Overall:          data.Overall,
		Provisioned:      data.Provisioned,
		Frozen:           data.Frozen,
		PlacementState:   data.PlacementState,
		PlacementPolicy:  data.PlacementPolicy,
		Orchestrate:      data.Orchestrate,
		Topology:         data.Topology,
		Priority:         data.Priority,
		Scope:            scope,
		UpdatedAt:        data.UpdatedAt,
		UpInstancesCount: data.UpInstancesCount,
		InstanceCount:    len(nodes),
		InstanceNodes:    nodes,
	}
	if data.Availability != nil {
		result.Availability = *data.Availability
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

func validateExactObjectPath(path string) (ClusterObjectReference, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ClusterObjectReference{}, fmt.Errorf("object path is required")
	}
	if len(path) > maxObjectSelectorLength {
		return ClusterObjectReference{}, fmt.Errorf("object path exceeds %d characters", maxObjectSelectorLength)
	}
	if hasSelectorCharacters(path) {
		return ClusterObjectReference{}, fmt.Errorf("object path %q holds characters of a selector: give one exact path; list_cluster_objects selects several", path)
	}
	reference, err := parseClusterObjectReference(path)
	if err != nil {
		return ClusterObjectReference{}, fmt.Errorf("invalid object path %q: %w", path, err)
	}
	return reference, nil
}

// objectSelectorCharacters are those a selector holds and an object path
// never does: a pattern, an alternative, a conjunction, a negation, a
// configuration term or a space. The daemon expands the path of a request as
// a selector, configuration terms included, which compare values the MCP
// never returns: an exact path holding one is refused before the call.
const objectSelectorCharacters = "*?[],\\+!=:<> \t"

// hasSelectorCharacters says whether path holds a character of a selector.
func hasSelectorCharacters(path string) bool {
	return strings.ContainsAny(path, objectSelectorCharacters)
}
