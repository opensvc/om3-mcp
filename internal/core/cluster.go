package core

import (
	"context"
	"fmt"
	"sort"
)

const (
	defaultClusterStatusPageLimit = 100
	maxClusterStatusPageLimit     = 200
	maxClusterStatusCursorLength  = 1024
	maxClusterStatusIssues        = 50
	maxClusterStatusGenerations   = 200
	maxClusterStatusArbitrators   = 50
	// maxClusterStatusInstanceMonitors bounds the instances listed per
	// object in instance_monitors, and maxClusterStatusPageInstanceMonitors
	// those of a whole page: an orchestration of every instance at once
	// must not fill the context of a model.
	maxClusterStatusInstanceMonitors     = 50
	maxClusterStatusPageInstanceMonitors = 200
	// maxClusterStatusStateReasonRunes bounds the free text of a daemon
	// state_reason.
	maxClusterStatusStateReasonRunes = 500
	// idleMonitorState is the monitor state of an instance at rest, the one
	// instance_monitors leaves out.
	idleMonitorState = "idle"
)

type GetClusterStatusOptions struct {
	NodeLimit    int
	NodeCursor   string
	ObjectLimit  int
	ObjectCursor string
}

type ClusterStatusSnapshot struct {
	Provenance Provenance              `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Cluster    ClusterStatusFacts      `json:"cluster" jsonschema:"cluster identity, configuration flags, and daemon-reported cluster status"`
	Nodes      ClusterStatusNodePage   `json:"nodes" jsonschema:"bounded page of configured or reported nodes with factual status data"`
	Objects    ClusterStatusObjectPage `json:"objects" jsonschema:"bounded page and exact-value summaries of visible actor objects"`
}

type ClusterStatusFacts struct {
	ID                    string   `json:"id" jsonschema:"the OpenSVC cluster identifier"`
	Name                  string   `json:"name" jsonschema:"the OpenSVC cluster name"`
	QuorumEnabled         bool     `json:"quorum_enabled" jsonschema:"whether quorum is enabled in the cluster configuration"`
	IsCompatible          bool     `json:"is_compatible" jsonschema:"the compatibility flag reported by OpenSVC"`
	IsFrozen              bool     `json:"is_frozen" jsonschema:"true when at least one node is frozen, as the daemon computes it, not a freeze of the whole cluster: the frozen_at of each node says which"`
	ConfigIssuesTotal     int      `json:"config_issues_total" jsonschema:"number of cluster configuration issues reported by OpenSVC before limiting"`
	ConfigIssues          []string `json:"config_issues" jsonschema:"cluster configuration issues reported by OpenSVC, limited to 50"`
	ConfigIssuesTruncated bool     `json:"config_issues_truncated" jsonschema:"whether cluster configuration issues were omitted after the 50-entry limit"`
}

type ClusterStatusNodePage struct {
	ConfiguredTotal int                 `json:"configured_total" jsonschema:"number of distinct node names in the cluster configuration"`
	ReportedTotal   int                 `json:"reported_total" jsonschema:"number of node names with status data in the daemon response"`
	Total           int                 `json:"total" jsonschema:"number of distinct configured or reported node names"`
	Count           int                 `json:"count" jsonschema:"number of node records returned in this page"`
	Items           []ClusterStatusNode `json:"items" jsonschema:"node records sorted by exact node name"`
	NextCursor      string              `json:"next_cursor,omitempty" jsonschema:"the exact node name to pass as node_cursor for the next page"`
	Truncated       bool                `json:"truncated" jsonschema:"whether more node records remain after this page"`
}

type ClusterStatusNode struct {
	Name       string                     `json:"name" jsonschema:"the exact OpenSVC node name"`
	Configured bool                       `json:"configured" jsonschema:"whether the node name is present in the cluster configuration"`
	Reported   bool                       `json:"reported" jsonschema:"whether the daemon response contains status data for this node"`
	Status     *ClusterStatusNodeReported `json:"status" jsonschema:"status fields reported by OpenSVC, or null when status data is absent"`
	Monitor    *ClusterStatusNodeMonitor  `json:"monitor" jsonschema:"monitor fields reported by OpenSVC, or null when status data is absent"`
	Stats      *NodeCapacityStats         `json:"stats" jsonschema:"published node capacity statistics, or null when unavailable"`
	Policy     *ClusterStatusNodePolicy   `json:"policy" jsonschema:"selected node configuration facts relevant to capacity, or null when unavailable"`
	Daemon     *ClusterStatusNodeDaemon   `json:"daemon" jsonschema:"daemon process facts, or null when node status is absent"`
	Heartbeat  *HeartbeatFacts            `json:"heartbeat" jsonschema:"bounded heartbeat facts reported by OpenSVC, or null when unavailable"`
}

type ClusterStatusNodeReported struct {
	AgentVersion string                      `json:"agent_version" jsonschema:"the OpenSVC agent version reported by this node"`
	APIVersion   int                         `json:"api_version" jsonschema:"the daemon API compatibility version reported by this node"`
	Compat       int                         `json:"compat_version" jsonschema:"the daemon compatibility version reported by this node"`
	IsLeader     bool                        `json:"is_leader" jsonschema:"whether this node reports itself as cluster leader"`
	IsOverloaded bool                        `json:"is_overloaded" jsonschema:"the overload flag reported by OpenSVC without MCP interpretation"`
	BootedAt     string                      `json:"booted_at" jsonschema:"the node boot timestamp reported by OpenSVC"`
	FrozenAt     string                      `json:"frozen_at" jsonschema:"the node freeze timestamp reported by OpenSVC"`
	LeftAt       string                      `json:"left_at" jsonschema:"the last node leave timestamp reported by OpenSVC"`
	RejoinedAt   string                      `json:"rejoined_at" jsonschema:"the last node rejoin timestamp reported by OpenSVC"`
	Generations  ClusterStatusGenerationList `json:"generations" jsonschema:"bounded generation vector reported by OpenSVC"`
	Arbitrators  ClusterStatusArbitratorList `json:"arbitrators" jsonschema:"bounded arbitrator statuses reported by OpenSVC"`
}

type ClusterStatusGenerationList struct {
	Total     int                       `json:"total" jsonschema:"number of generation entries before limiting"`
	Count     int                       `json:"count" jsonschema:"number of generation entries returned"`
	Items     []ClusterStatusGeneration `json:"items" jsonschema:"generation entries sorted by node name"`
	Truncated bool                      `json:"truncated" jsonschema:"whether generation entries were omitted after the 200-entry limit"`
}

type ClusterStatusGeneration struct {
	Node  string `json:"node" jsonschema:"the node name associated with this generation value"`
	Value uint64 `json:"value" jsonschema:"the exact generation value reported by OpenSVC"`
}

type ClusterStatusArbitratorList struct {
	Total     int                       `json:"total" jsonschema:"number of arbitrator entries before limiting"`
	Count     int                       `json:"count" jsonschema:"number of arbitrator entries returned"`
	Items     []ClusterStatusArbitrator `json:"items" jsonschema:"arbitrator entries sorted by name"`
	Truncated bool                      `json:"truncated" jsonschema:"whether arbitrator entries were omitted after the 50-entry limit"`
}

type ClusterStatusArbitrator struct {
	Name   string `json:"name" jsonschema:"the arbitrator name reported by OpenSVC"`
	URL    string `json:"url" jsonschema:"the arbitrator URL reported by OpenSVC"`
	Status string `json:"status" jsonschema:"the exact arbitrator status reported by OpenSVC"`
	Weight int    `json:"weight" jsonschema:"the arbitrator vote weight reported by OpenSVC"`
}

type ClusterStatusNodeMonitor struct {
	State                 string `json:"state" jsonschema:"the exact node monitor state reported by OpenSVC"`
	StateUpdatedAt        string `json:"state_updated_at" jsonschema:"the monitor state update timestamp reported by OpenSVC"`
	GlobalExpect          string `json:"global_expect" jsonschema:"the exact global target state reported by OpenSVC"`
	GlobalExpectUpdatedAt string `json:"global_expect_updated_at" jsonschema:"the global target update timestamp reported by OpenSVC"`
	LocalExpect           string `json:"local_expect" jsonschema:"the exact local target state reported by OpenSVC"`
	LocalExpectUpdatedAt  string `json:"local_expect_updated_at" jsonschema:"the local target update timestamp reported by OpenSVC"`
	OrchestrationID       string `json:"orchestration_id" jsonschema:"the orchestration identifier reported by OpenSVC"`
	OrchestrationIsDone   bool   `json:"orchestration_is_done" jsonschema:"the orchestration completion flag reported by OpenSVC"`
	SessionID             string `json:"session_id" jsonschema:"the monitor session identifier reported by OpenSVC"`
	UpdatedAt             string `json:"updated_at" jsonschema:"the monitor publication timestamp reported by OpenSVC"`
}

type ClusterStatusNodePolicy struct {
	MinAvailMemPct  int      `json:"min_avail_mem_pct" jsonschema:"the configured minimum available physical memory percentage; zero disables the daemon check"`
	MinAvailSwapPct int      `json:"min_avail_swap_pct" jsonschema:"the configured minimum available swap percentage; zero disables the daemon check"`
	IssuesTotal     int      `json:"issues_total" jsonschema:"number of node configuration issues reported by OpenSVC before limiting"`
	Issues          []string `json:"issues" jsonschema:"node configuration issues reported by OpenSVC, limited to 50"`
	IssuesTruncated bool     `json:"issues_truncated" jsonschema:"whether node configuration issues were omitted after the 50-entry limit"`
}

type ClusterStatusNodeDaemon struct {
	PID       int    `json:"pid" jsonschema:"the OpenSVC daemon process identifier reported for this node"`
	StartedAt string `json:"started_at" jsonschema:"the daemon start timestamp reported for this node"`
}

type ClusterStatusObjectPage struct {
	ReportedTotal int                            `json:"reported_total" jsonschema:"number of visible entries in the daemon object map, including non-actors"`
	ActorTotal    int                            `json:"actor_total" jsonschema:"number of visible objects with a daemon-reported availability field"`
	Count         int                            `json:"count" jsonschema:"number of actor object records returned in this page"`
	Items         []ClusterStatusObject          `json:"items" jsonschema:"visible actor objects sorted by canonical path"`
	StateCounts   ClusterStatusObjectStateCounts `json:"state_counts" jsonschema:"counts grouped by exact daemon-reported values across all visible actor objects"`
	NextCursor    string                         `json:"next_cursor,omitempty" jsonschema:"the exact object path to pass as object_cursor for the next page"`
	Truncated     bool                           `json:"truncated" jsonschema:"whether more actor object records remain after this page"`
	// InstanceMonitorsTruncated tells that the page bound, not the one of an
	// object, cut instance_monitors.
	InstanceMonitorsTruncated bool `json:"instance_monitors_truncated" jsonschema:"whether the 200-entry limit of the page left instances out of the instance_monitors of its objects, in path order; each object's instance_monitors_total keeps its full count"`
}

type ClusterStatusObject struct {
	Path             string   `json:"path" jsonschema:"the canonical OpenSVC object path"`
	Availability     string   `json:"availability" jsonschema:"the exact aggregate availability reported by OpenSVC"`
	Overall          string   `json:"overall" jsonschema:"the exact aggregate overall status reported by OpenSVC"`
	Provisioned      string   `json:"provisioned" jsonschema:"the exact aggregate provisioned state reported by OpenSVC"`
	Frozen           string   `json:"frozen" jsonschema:"the exact aggregate freeze state reported by OpenSVC"`
	PlacementState   string   `json:"placement_state" jsonschema:"the exact aggregate placement state reported by OpenSVC"`
	PlacementPolicy  string   `json:"placement_policy" jsonschema:"the placement policy reported by OpenSVC"`
	Orchestrate      string   `json:"orchestrate" jsonschema:"the orchestration mode reported by OpenSVC"`
	Topology         string   `json:"topology" jsonschema:"the topology reported by OpenSVC"`
	Priority         int      `json:"priority" jsonschema:"the orchestration priority reported by OpenSVC"`
	UpInstancesCount int      `json:"up_instances_count" jsonschema:"the aggregate number of up instances reported by OpenSVC"`
	Scope            []string `json:"scope" jsonschema:"the sorted node names in the object scope"`
	UpdatedAt        string   `json:"updated_at" jsonschema:"the last object status update timestamp reported by OpenSVC"`
	// InstanceMonitors lists the instances whose monitor is not idle, for
	// size only: the MCP classifies no state.
	InstanceMonitors          []ClusterStatusInstanceMonitor `json:"instance_monitors,omitempty" jsonschema:"the instances of this object whose exact monitor state is not idle, sorted by node, at most 50 per object and 200 per page; absent when no instance of the object reports a monitor other than idle"`
	InstanceMonitorsTotal     int                            `json:"instance_monitors_total,omitempty" jsonschema:"the number of instances of this object whose exact monitor state is not idle, before the limits; absent when zero"`
	InstanceMonitorsTruncated bool                           `json:"instance_monitors_truncated,omitempty" jsonschema:"whether instances were left out of instance_monitors by the limit of the object or of the page"`
}

type ClusterStatusInstanceMonitor struct {
	Node           string `json:"node" jsonschema:"the node of the instance"`
	State          string `json:"state" jsonschema:"the exact instance monitor state reported by OpenSVC, such as provision failed or starting"`
	StateUpdatedAt string `json:"state_updated_at" jsonschema:"the monitor state update timestamp reported by OpenSVC"`
	// StateReason is absent from the daemons older than the field, and
	// when the state says enough.
	StateReason          string `json:"state_reason,omitempty" jsonschema:"why the instance is in a failure state, as the daemon words it when the state alone does not say it, at most 500 characters; absent otherwise"`
	StateReasonTruncated bool   `json:"state_reason_truncated,omitempty" jsonschema:"whether state_reason was cut after 500 characters"`
	GlobalExpect         string `json:"global_expect" jsonschema:"the exact global target state of the instance monitor reported by OpenSVC"`
}

type ClusterStatusObjectStateCounts struct {
	Availability   []ClusterStatusValueCount `json:"availability" jsonschema:"counts by exact availability value"`
	Overall        []ClusterStatusValueCount `json:"overall" jsonschema:"counts by exact overall value"`
	Provisioned    []ClusterStatusValueCount `json:"provisioned" jsonschema:"counts by exact provisioned value"`
	Frozen         []ClusterStatusValueCount `json:"frozen" jsonschema:"counts by exact freeze value"`
	PlacementState []ClusterStatusValueCount `json:"placement_state" jsonschema:"counts by exact placement state value"`
	// InstanceMonitorState counts instances, not objects.
	InstanceMonitorState []ClusterStatusValueCount `json:"instance_monitor_state" jsonschema:"counts of the instances of visible actor objects with a reported monitor, by exact monitor state value, idle included"`
}

type ClusterStatusValueCount struct {
	Value string `json:"value" jsonschema:"the exact value reported by OpenSVC, including an empty string"`
	Count int    `json:"count" jsonschema:"the number of entries reporting this exact value: visible actor objects, or instances for instance_monitor_state"`
}

func (s *Service) GetClusterStatus(ctx context.Context, options GetClusterStatusOptions) (ClusterStatusSnapshot, error) {
	nodeLimit, err := clusterStatusPageLimit(options.NodeLimit, "node")
	if err != nil {
		return ClusterStatusSnapshot{}, err
	}
	objectLimit, err := clusterStatusPageLimit(options.ObjectLimit, "object")
	if err != nil {
		return ClusterStatusSnapshot{}, err
	}
	if len(options.NodeCursor) > maxClusterStatusCursorLength {
		return ClusterStatusSnapshot{}, fmt.Errorf("node cursor exceeds %d characters", maxClusterStatusCursorLength)
	}
	if len(options.ObjectCursor) > maxClusterStatusCursorLength {
		return ClusterStatusSnapshot{}, fmt.Errorf("object cursor exceeds %d characters", maxClusterStatusCursorLength)
	}

	status, err := s.getClusterStatus(ctx)
	if err != nil {
		return ClusterStatusSnapshot{}, fmt.Errorf("get cluster status: %w", err)
	}
	result := clusterStatusSnapshot(status, options.NodeCursor, nodeLimit, options.ObjectCursor, objectLimit)
	result.Provenance = s.newProvenance()
	result.Provenance.ServedBy = servedBy(status)
	return result, nil
}

func clusterStatusPageLimit(value int, domain string) (int, error) {
	if value == 0 {
		return defaultClusterStatusPageLimit, nil
	}
	if value < 1 || value > maxClusterStatusPageLimit {
		return 0, fmt.Errorf("cluster status %s limit must be between 1 and %d", domain, maxClusterStatusPageLimit)
	}
	return value, nil
}

func clusterStatusSnapshot(status clusterStatusResponse, nodeCursor string, nodeLimit int, objectCursor string, objectLimit int) ClusterStatusSnapshot {
	clusterIssues, clusterIssuesTruncated := boundedStrings(status.Cluster.Config.Issues, maxClusterStatusIssues)
	result := ClusterStatusSnapshot{
		Cluster: ClusterStatusFacts{
			ID: status.Cluster.Config.ID, Name: status.Cluster.Config.Name,
			QuorumEnabled: status.Cluster.Config.Quorum, IsCompatible: status.Cluster.Status.IsCompatible,
			IsFrozen: status.Cluster.Status.IsFrozen, ConfigIssuesTotal: len(status.Cluster.Config.Issues),
			ConfigIssues: clusterIssues, ConfigIssuesTruncated: clusterIssuesTruncated,
		},
	}
	result.Nodes = clusterStatusNodes(status, nodeCursor, nodeLimit)
	result.Objects = clusterStatusObjects(status, objectCursor, objectLimit)
	return result
}

func clusterStatusNodes(status clusterStatusResponse, cursor string, limit int) ClusterStatusNodePage {
	configured := make(map[string]bool, len(status.Cluster.Config.Nodes))
	allNames := make(map[string]struct{}, len(status.Cluster.Config.Nodes)+len(status.Cluster.Node))
	for _, name := range status.Cluster.Config.Nodes {
		configured[name] = true
		allNames[name] = struct{}{}
	}
	for name := range status.Cluster.Node {
		allNames[name] = struct{}{}
	}
	names := sortedSetKeys(allNames)
	start := sort.Search(len(names), func(i int) bool { return names[i] > cursor })
	end := min(start+limit, len(names))
	items := make([]ClusterStatusNode, 0, end-start)
	for _, name := range names[start:end] {
		node, reported := status.Cluster.Node[name]
		item := ClusterStatusNode{Name: name, Configured: configured[name], Reported: reported}
		if reported {
			item.Status = clusterStatusNodeReported(node.Status.Agent, node.Status.API, node.Status.Compat, node.Status.IsLeader, node.Status.IsOverloaded, node.Status.BootedAt, node.Status.FrozenAt, node.Status.LeftAt, node.Status.RejoinedAt, node.Status.Generation, node.Status.Arbitrators)
			item.Monitor = &ClusterStatusNodeMonitor{
				State: node.Monitor.State, StateUpdatedAt: node.Monitor.StateUpdatedAt,
				GlobalExpect: node.Monitor.GlobalExpect, GlobalExpectUpdatedAt: node.Monitor.GlobalExpectUpdatedAt,
				LocalExpect: node.Monitor.LocalExpect, LocalExpectUpdatedAt: node.Monitor.LocalExpectUpdatedAt,
				OrchestrationID: node.Monitor.OrchestrationID, OrchestrationIsDone: node.Monitor.OrchestrationIsDone,
				SessionID: node.Monitor.SessionID, UpdatedAt: node.Monitor.UpdatedAt,
			}
			item.Daemon = &ClusterStatusNodeDaemon{PID: node.Daemon.PID, StartedAt: node.Daemon.StartedAt}
			if node.Stats != nil {
				item.Stats = &NodeCapacityStats{Load15M: node.Stats.Load15M, MemAvailPct: node.Stats.MemAvailPct, MemTotalMB: node.Stats.MemTotalMB, Score: node.Stats.Score, SwapAvailPct: node.Stats.SwapAvailPct, SwapTotalMB: node.Stats.SwapTotalMB}
			}
			if node.Config != nil {
				issues, truncated := boundedStrings(node.Config.Issues, maxClusterStatusIssues)
				item.Policy = &ClusterStatusNodePolicy{MinAvailMemPct: node.Config.MinAvailMemPct, MinAvailSwapPct: node.Config.MinAvailSwapPct, IssuesTotal: len(node.Config.Issues), Issues: issues, IssuesTruncated: truncated}
			}
			item.Heartbeat = heartbeatFacts(node.Daemon.Heartbeat)
		}
		items = append(items, item)
	}
	page := ClusterStatusNodePage{ConfiguredTotal: len(configured), ReportedTotal: len(status.Cluster.Node), Total: len(names), Count: len(items), Items: items, Truncated: end < len(names)}
	if page.Truncated {
		page.NextCursor = items[len(items)-1].Name
	}
	return page
}

func clusterStatusNodeReported(agent string, api int, compat int, leader bool, overloaded bool, bootedAt string, frozenAt string, leftAt string, rejoinedAt string, generations map[string]uint64, arbitrators map[string]clusterNodeArbitrator) *ClusterStatusNodeReported {
	return &ClusterStatusNodeReported{
		AgentVersion: agent, APIVersion: api, Compat: compat, IsLeader: leader, IsOverloaded: overloaded,
		BootedAt: bootedAt, FrozenAt: frozenAt, LeftAt: leftAt, RejoinedAt: rejoinedAt,
		Generations: clusterStatusGenerations(generations), Arbitrators: clusterStatusArbitrators(arbitrators),
	}
}

func clusterStatusGenerations(values map[string]uint64) ClusterStatusGenerationList {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	end := min(len(names), maxClusterStatusGenerations)
	items := make([]ClusterStatusGeneration, 0, end)
	for _, name := range names[:end] {
		items = append(items, ClusterStatusGeneration{Node: name, Value: values[name]})
	}
	return ClusterStatusGenerationList{Total: len(names), Count: len(items), Items: items, Truncated: end < len(names)}
}

func clusterStatusArbitrators(values map[string]clusterNodeArbitrator) ClusterStatusArbitratorList {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	end := min(len(names), maxClusterStatusArbitrators)
	items := make([]ClusterStatusArbitrator, 0, end)
	for _, name := range names[:end] {
		value := values[name]
		items = append(items, ClusterStatusArbitrator{Name: name, URL: value.URL, Status: value.Status, Weight: value.Weight})
	}
	return ClusterStatusArbitratorList{Total: len(names), Count: len(items), Items: items, Truncated: end < len(names)}
}

func clusterStatusObjects(status clusterStatusResponse, cursor string, limit int) ClusterStatusObjectPage {
	items := make([]ClusterStatusObject, 0, len(status.Cluster.Object))
	availability := make(map[string]int)
	overall := make(map[string]int)
	provisioned := make(map[string]int)
	frozen := make(map[string]int)
	placement := make(map[string]int)
	monitorStates := make(map[string]int)
	monitors := clusterStatusInstanceMonitors(status)
	for path, object := range status.Cluster.Object {
		if object.Availability == nil {
			continue
		}
		scope := append([]string{}, object.Scope...)
		sort.Strings(scope)
		item := ClusterStatusObject{Path: path, Availability: *object.Availability, Overall: object.Overall, Provisioned: object.Provisioned, Frozen: object.Frozen, PlacementState: object.PlacementState, PlacementPolicy: object.PlacementPolicy, Orchestrate: object.Orchestrate, Topology: object.Topology, Priority: object.Priority, UpInstancesCount: object.UpInstancesCount, Scope: scope, UpdatedAt: object.UpdatedAt}
		var notIdle []ClusterStatusInstanceMonitor
		for _, monitor := range monitors[path] {
			monitorStates[monitor.State]++
			if monitor.State != idleMonitorState {
				notIdle = append(notIdle, monitor)
			}
		}
		item.InstanceMonitors = notIdle
		item.InstanceMonitorsTotal = len(notIdle)
		items = append(items, item)
		availability[*object.Availability]++
		overall[object.Overall]++
		provisioned[object.Provisioned]++
		frozen[object.Frozen]++
		placement[object.PlacementState]++
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	start := sort.Search(len(items), func(i int) bool { return items[i].Path > cursor })
	end := min(start+limit, len(items))
	pageItems := append([]ClusterStatusObject{}, items[start:end]...)
	pageMonitorsTruncated := boundPageInstanceMonitors(pageItems)
	page := ClusterStatusObjectPage{
		ReportedTotal: len(status.Cluster.Object), ActorTotal: len(items), Count: len(pageItems), Items: pageItems,
		StateCounts: ClusterStatusObjectStateCounts{Availability: exactValueCounts(availability), Overall: exactValueCounts(overall), Provisioned: exactValueCounts(provisioned), Frozen: exactValueCounts(frozen), PlacementState: exactValueCounts(placement), InstanceMonitorState: exactValueCounts(monitorStates)},
		Truncated:   end < len(items), InstanceMonitorsTruncated: pageMonitorsTruncated,
	}
	if page.Truncated {
		page.NextCursor = pageItems[len(pageItems)-1].Path
	}
	return page
}

// boundPageInstanceMonitors applies the bounds of instance_monitors to the
// objects of a page, in their order, and tells whether the page bound cut
// any.
func boundPageInstanceMonitors(items []ClusterStatusObject) bool {
	left := maxClusterStatusPageInstanceMonitors
	pageTruncated := false
	for i := range items {
		monitors := items[i].InstanceMonitors
		end := min(len(monitors), maxClusterStatusInstanceMonitors)
		if end > left {
			end = left
			pageTruncated = true
		}
		left -= end
		if end == 0 {
			// Absent from the JSON, not an empty list.
			monitors = nil
		}
		items[i].InstanceMonitors = monitors[:end]
		items[i].InstanceMonitorsTruncated = end < items[i].InstanceMonitorsTotal
	}
	return pageTruncated
}

// clusterStatusInstanceMonitors gathers, by object path, the instances the
// nodes report with a monitor, sorted by node.
func clusterStatusInstanceMonitors(status clusterStatusResponse) map[string][]ClusterStatusInstanceMonitor {
	result := make(map[string][]ClusterStatusInstanceMonitor)
	for nodeName, node := range status.Cluster.Node {
		for path, instance := range node.Instance {
			if instance.Monitor == nil {
				continue
			}
			reason, reasonTruncated := boundedRunes(instance.Monitor.StateReason, maxClusterStatusStateReasonRunes)
			result[path] = append(result[path], ClusterStatusInstanceMonitor{
				Node: nodeName, State: instance.Monitor.State, StateUpdatedAt: instance.Monitor.StateUpdatedAt,
				StateReason: reason, StateReasonTruncated: reasonTruncated, GlobalExpect: instance.Monitor.GlobalExpect,
			})
		}
	}
	for _, monitors := range result {
		sort.Slice(monitors, func(i, j int) bool { return monitors[i].Node < monitors[j].Node })
	}
	return result
}

func exactValueCounts(values map[string]int) []ClusterStatusValueCount {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	result := make([]ClusterStatusValueCount, 0, len(keys))
	for _, value := range keys {
		result = append(result, ClusterStatusValueCount{Value: value, Count: values[value]})
	}
	return result
}

func boundedStrings(values []string, limit int) ([]string, bool) {
	end := min(len(values), limit)
	result := append([]string{}, values[:end]...)
	return result, end < len(values)
}

func boundedRunes(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]), true
}

func sortedSetKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
