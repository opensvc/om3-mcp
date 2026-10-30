package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxNodeStatusConfiguredPeers = 200

type NodeStatus struct {
	Provenance Provenance          `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node       string              `json:"node" jsonschema:"the exact OpenSVC node name selected from the cluster status"`
	Membership NodeMembershipFacts `json:"membership" jsonschema:"factual cluster configuration context for the selected node and its configured peers"`
	Status     NodeReportedStatus  `json:"status" jsonschema:"the node status last published by OpenSVC"`
	Monitor    NodeMonitorStatus   `json:"monitor" jsonschema:"the node monitor state last published by OpenSVC"`
	Stats      *NodeCapacityStats  `json:"stats" jsonschema:"published node capacity statistics, or null when unavailable"`
	Policy     *NodeCapacityPolicy `json:"policy" jsonschema:"configured memory and swap availability thresholds, or null when unavailable"`
	Config     *NodeConfigFacts    `json:"config" jsonschema:"the environment, labels and grace periods the node configuration sets, or null when unavailable"`
	Heartbeat  *HeartbeatFacts     `json:"heartbeat" jsonschema:"bounded heartbeat facts reported by OpenSVC, or null when unavailable"`
	Daemon     NodeDaemon          `json:"daemon" jsonschema:"the daemon process and subsystem facts the node last published"`
}

// NodeConfigFacts are node configuration values the node publishes in the
// cluster status: its environment, the labels object node selectors match,
// and the periods its daemon waits on.
type NodeConfigFacts struct {
	Env                      string            `json:"env" jsonschema:"the node environment, such as PRD or TST"`
	Labels                   map[string]string `json:"labels" jsonschema:"the node labels, matched by the node selectors of objects; at most 100, values limited to 1024 characters"`
	LabelsTruncated          bool              `json:"labels_truncated" jsonschema:"whether labels were omitted after 100 entries or values shortened"`
	MaintenanceGracePeriodNS int64             `json:"maintenance_grace_period_ns" jsonschema:"how long a daemon keeps the data of a peer in maintenance, and does not take over its instances, in nanoseconds"`
	RejoinGracePeriodNS      int64             `json:"rejoin_grace_period_ns" jsonschema:"how long a starting daemon stays in rejoin state, with orchestration not allowed, waiting for a heartbeat from every peer, in nanoseconds"`
	ReadyPeriodNS            int64             `json:"ready_period_ns" jsonschema:"how long the daemon waits before starting an instance in ready state, during which a peer can preempt the start, in nanoseconds"`
}

type NodeMembershipFacts struct {
	IsConfigured    bool         `json:"is_configured" jsonschema:"whether the selected node name is present in the cluster configuration"`
	ConfiguredPeers NodeNameList `json:"configured_peers" jsonschema:"bounded distinct configured node names other than the selected node"`
}

type NodeNameList struct {
	Total     int      `json:"total" jsonschema:"number of distinct configured peer node names before limiting"`
	Count     int      `json:"count" jsonschema:"number of configured peer node names returned"`
	Items     []string `json:"items" jsonschema:"configured peer node names sorted by exact name"`
	Truncated bool     `json:"truncated" jsonschema:"whether configured peer node names were omitted after the 200-entry limit"`
}

type NodeReportedStatus struct {
	AgentVersion string `json:"agent_version" jsonschema:"the OpenSVC agent version reported by this node"`
	APIVersion   int    `json:"api_version" jsonschema:"the node daemon API compatibility version"`
	Compat       int    `json:"compat_version" jsonschema:"the node daemon compatibility version"`
	IsLeader     bool   `json:"is_leader" jsonschema:"whether the node reports itself as cluster leader"`
	IsOverloaded bool   `json:"is_overloaded" jsonschema:"whether the node reports overload"`
	BootedAt     string `json:"booted_at" jsonschema:"the node boot timestamp reported by OpenSVC"`
	FrozenAt     string `json:"frozen_at" jsonschema:"the node freeze timestamp reported by OpenSVC"`
}

type NodeMonitorStatus struct {
	State               string `json:"state" jsonschema:"the current node monitor state reported by OpenSVC"`
	GlobalExpect        string `json:"global_expect" jsonschema:"the cluster-wide target state for this node"`
	LocalExpect         string `json:"local_expect" jsonschema:"the local target state for this node"`
	OrchestrationID     string `json:"orchestration_id" jsonschema:"the current node orchestration identifier, if any"`
	OrchestrationIsDone bool   `json:"orchestration_is_done" jsonschema:"whether the node orchestration reports completion"`
	UpdatedAt           string `json:"updated_at" jsonschema:"the node monitor update timestamp reported by OpenSVC"`
}

type NodeCapacityStats struct {
	Load15M      float64 `json:"load_15m" jsonschema:"published fifteen-minute system load"`
	MemAvailPct  int     `json:"mem_available_pct" jsonschema:"available physical memory as a percentage"`
	MemTotalMB   uint64  `json:"mem_total_mb" jsonschema:"total physical memory in megabytes"`
	Score        int     `json:"score" jsonschema:"OpenSVC node capacity score"`
	SwapAvailPct int     `json:"swap_available_pct" jsonschema:"available swap as a percentage"`
	SwapTotalMB  uint64  `json:"swap_total_mb" jsonschema:"total swap capacity in megabytes"`
}

type NodeCapacityPolicy struct {
	MinAvailMemPct  int `json:"min_avail_mem_pct" jsonschema:"configured minimum available physical memory percentage; zero disables this check"`
	MinAvailSwapPct int `json:"min_avail_swap_pct" jsonschema:"configured minimum available swap percentage; zero disables this check"`
}

type GetNodeLogsOptions struct {
	Node            string
	Lines           int
	Component       string
	ExecID          string
	SessionID       string
	OrchestrationID string
}

type NodeLogList struct {
	Provenance Provenance     `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node       string         `json:"node" jsonschema:"the exact OpenSVC node whose journal was queried"`
	Component  string         `json:"component,omitempty" jsonschema:"the exact OpenSVC component filter when requested"`
	IDs        NodeLogIDs     `json:"ids" jsonschema:"the daemon execution, session and orchestration id filters applied"`
	Lines      int            `json:"lines" jsonschema:"the requested maximum number of recent log entries"`
	Count      int            `json:"count" jsonschema:"the number of bounded log entries returned"`
	Entries    []NodeLogEntry `json:"entries" jsonschema:"recent OpenSVC node log entries in chronological order"`
	Truncated  bool           `json:"truncated" jsonschema:"whether older entries or message content were omitted by response bounds"`
}

type NodeLogEntry struct {
	Timestamp        string `json:"timestamp" jsonschema:"the OpenSVC log timestamp, or journald timestamp when absent"`
	Level            string `json:"level,omitempty" jsonschema:"the OpenSVC log level when provided"`
	Priority         string `json:"priority,omitempty" jsonschema:"the journald syslog priority when provided; 0 is most severe and 7 is debug"`
	Message          string `json:"message" jsonschema:"the bounded log message without raw journald metadata"`
	MessageTruncated bool   `json:"message_truncated" jsonschema:"whether this log message was shortened by MCP output bounds"`
	Component        string `json:"component,omitempty" jsonschema:"the OpenSVC package or component that emitted the entry"`
	SystemdUnit      string `json:"systemd_unit,omitempty" jsonschema:"the systemd unit recorded by journald when present"`
	ObjectPath       string `json:"object_path,omitempty" jsonschema:"the related OpenSVC object path when present"`
	ResourceID       string `json:"resource_id,omitempty" jsonschema:"the related OpenSVC resource id when present"`
	SessionID        string `json:"session_id,omitempty" jsonschema:"the related OpenSVC session id when present"`
	ExecID           string `json:"exec_id,omitempty" jsonschema:"the related OpenSVC daemon execution id when present"`
	EventID          string `json:"event_id,omitempty" jsonschema:"the related OpenSVC event id when present"`
	RequestID        string `json:"request_id,omitempty" jsonschema:"the related daemon API request id when present"`
	OrchestrationID  string `json:"orchestration_id,omitempty" jsonschema:"the related OpenSVC orchestration id when present"`
}

// NodeLogIDs are the ids the daemon stamps on what it runs: the execution, the
// session it belongs to, and the orchestration the session is a step of.
type NodeLogIDs struct {
	ExecID          string `json:"exec_id,omitempty" jsonschema:"the daemon execution id filter, in lower case"`
	SessionID       string `json:"session_id,omitempty" jsonschema:"the session id filter, in lower case"`
	OrchestrationID string `json:"orchestration_id,omitempty" jsonschema:"the orchestration id filter, in lower case"`
}

type daemonNodeLogEnvelope struct {
	daemonInstanceLogEnvelope
	Priority    string `json:"PRIORITY"`
	SystemdUnit string `json:"_SYSTEMD_UNIT"`
}

func (s *Service) GetNodeStatus(ctx context.Context, nodeName string) (NodeStatus, error) {
	if err := validateNodeTarget(nodeName); err != nil {
		return NodeStatus{}, err
	}

	clusterStatus, err := s.getClusterStatus(ctx)
	if err != nil {
		return NodeStatus{}, fmt.Errorf("get node status: %w", err)
	}
	node, reported := clusterStatus.Cluster.Node[nodeName]
	if !reported {
		if slices.Contains(clusterStatus.Cluster.Config.Nodes, nodeName) {
			return NodeStatus{}, fmt.Errorf("configured node %q has no published status data", nodeName)
		}
		return NodeStatus{}, fmt.Errorf("node %q is not present in the cluster status: get_cluster_status lists the nodes of the cluster", nodeName)
	}

	result := NodeStatus{
		Node:       nodeName,
		Membership: nodeMembershipFacts(nodeName, clusterStatus.Cluster.Config.Nodes),
		Status: NodeReportedStatus{
			AgentVersion: node.Status.Agent,
			APIVersion:   node.Status.API,
			Compat:       node.Status.Compat,
			IsLeader:     node.Status.IsLeader,
			IsOverloaded: node.Status.IsOverloaded,
			BootedAt:     node.Status.BootedAt,
			FrozenAt:     node.Status.FrozenAt,
		},
		Monitor: NodeMonitorStatus{
			State:               node.Monitor.State,
			GlobalExpect:        node.Monitor.GlobalExpect,
			LocalExpect:         node.Monitor.LocalExpect,
			OrchestrationID:     node.Monitor.OrchestrationID,
			OrchestrationIsDone: node.Monitor.OrchestrationIsDone,
			UpdatedAt:           node.Monitor.UpdatedAt,
		},
		Heartbeat: heartbeatFacts(node.Daemon.Heartbeat),
		Daemon:    NodeDaemon{PID: node.Daemon.PID, StartedAt: node.Daemon.StartedAt, Subsystems: daemonSubsystems(node.Daemon)},
	}
	if node.Stats != nil {
		result.Stats = &NodeCapacityStats{
			Load15M:      node.Stats.Load15M,
			MemAvailPct:  node.Stats.MemAvailPct,
			MemTotalMB:   node.Stats.MemTotalMB,
			Score:        node.Stats.Score,
			SwapAvailPct: node.Stats.SwapAvailPct,
			SwapTotalMB:  node.Stats.SwapTotalMB,
		}
	}
	if node.Config != nil {
		result.Policy = &NodeCapacityPolicy{
			MinAvailMemPct:  node.Config.MinAvailMemPct,
			MinAvailSwapPct: node.Config.MinAvailSwapPct,
		}
		result.Config = nodeConfigFacts(*node.Config)
	}
	result.Provenance = s.newProvenance()
	result.Provenance.ServedBy = servedBy(clusterStatus)
	return result, nil
}

func nodeMembershipFacts(nodeName string, configuredNodes []string) NodeMembershipFacts {
	peers := make(map[string]struct{}, len(configuredNodes))
	isConfigured := false
	for _, name := range configuredNodes {
		if name == nodeName {
			isConfigured = true
			continue
		}
		peers[name] = struct{}{}
	}
	names := sortedSetKeys(peers)
	end := min(len(names), maxNodeStatusConfiguredPeers)
	items := append([]string{}, names[:end]...)
	return NodeMembershipFacts{
		IsConfigured: isConfigured,
		ConfiguredPeers: NodeNameList{
			Total: len(names), Count: len(items), Items: items, Truncated: end < len(names),
		},
	}
}

// localDaemonNodeAlias is the daemon alias of the node receiving the request.
const localDaemonNodeAlias = "_"

// errNodeTarget is the error of every tool given an invalid node.
// A question on the cluster names no node: the error names where the nodes
// are, rather than leaving the model to ask the user for one.
var errNodeTarget = errors.New("node must be one exact OpenSVC node name of at most 255 characters, without wildcard, selector or the _ alias: get_cluster_status lists the nodes of the cluster; for a question on the cluster, call this tool for each of them")

// unknownNodeTitle is the title of the problem the daemon answers, with a
// 404, for a node it has no status of: a name that is no node of the
// cluster, or a node whose daemon publishes nothing.
const unknownNodeTitle = "node status data not found"

// ExplainUnknownNode names, in the refusal of a node the daemon does not
// know, where the nodes are. On 2026-10-30, with no node named in the
// conversation, a model called list_daemon_orchestrations with a made-up
// node, read the 404 of the daemon, and asked the user for a node.
func ExplainUnknownNode(err error) error {
	var problem interface {
		HTTPStatus() int
		ProblemTitle() string
	}
	if errors.As(err, &problem) && problem.HTTPStatus() == http.StatusNotFound && strings.EqualFold(problem.ProblemTitle(), unknownNodeTitle) {
		return fmt.Errorf("%w: no node of this name reports to the cluster; get_cluster_status lists the nodes of the cluster", err)
	}
	return err
}

// validateNodeTarget requires one exact node name. The daemon resolves the _
// alias to itself, which is the node carrying the cluster VIP: it would move
// on failover, so a node diagnostic always names its node.
func validateNodeTarget(node string) error {
	if node == localDaemonNodeAlias || !validExactNodeName(node) {
		return errNodeTarget
	}
	return nil
}

func (s *Service) GetNodeLogs(ctx context.Context, options GetNodeLogsOptions) (NodeLogList, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return NodeLogList{}, err
	}
	lines := options.Lines
	if lines == 0 {
		lines = defaultGetInstanceLogsLines
	}
	if lines < 1 || lines > maxGetInstanceLogsLines {
		return NodeLogList{}, fmt.Errorf("node log lines must be between 1 and %d", maxGetInstanceLogsLines)
	}
	component := strings.TrimSpace(options.Component)
	if len(component) > maxInstanceLogFieldRunes || strings.IndexFunc(component, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_./:#-", r))
	}) >= 0 {
		return NodeLogList{}, fmt.Errorf("component must be one exact OpenSVC component of at most 255 characters")
	}
	ids, err := validateNodeLogIDs(options)
	if err != nil {
		return NodeLogList{}, err
	}
	getter, ok := s.client.(SSEGetter)
	if !ok {
		return NodeLogList{}, fmt.Errorf("OpenSVC daemon client does not support SSE requests")
	}

	endpoint := fmt.Sprintf("/api/node/name/%s/log", node)
	query := url.Values{"follow": {"false"}, "lines": {strconv.Itoa(lines + 1)}}
	// The daemon reads each filter as a journal match: matches on distinct
	// fields must all hold.
	for _, match := range [][2]string{
		{"PKG", component}, {"EXEC_ID", ids.ExecID}, {"SESSION_ID", ids.SessionID}, {"ORCHESTRATION_ID", ids.OrchestrationID},
	} {
		if match[1] != "" {
			query.Add("filter", match[0]+"="+match[1])
		}
	}
	entries := make([]NodeLogEntry, 0, lines+1)
	err = getter.GetSSE(ctx, endpoint, query, func(event string, _ string, data []byte) error {
		if event != "" && event != "log" {
			return fmt.Errorf("unexpected node log SSE event %q", event)
		}
		entry, err := parseNodeLogEntry(data, node, component, ids)
		if err != nil {
			return err
		}
		entries = append(entries, entry)
		if len(entries) > lines+1 {
			return fmt.Errorf("node log endpoint returned more than %d requested events", lines+1)
		}
		return nil
	})
	if err != nil {
		return NodeLogList{}, fmt.Errorf("get node logs: %w", err)
	}

	truncated := len(entries) > lines
	if truncated {
		entries = entries[len(entries)-lines:]
	}
	entries, bounded := boundNodeLogEntries(entries)
	truncated = truncated || bounded
	if entries == nil {
		entries = []NodeLogEntry{}
	}
	return NodeLogList{
		Provenance: s.newProvenance(), Node: node, Component: component, IDs: ids,
		Lines: lines, Count: len(entries), Entries: entries, Truncated: truncated,
	}, nil
}

const (
	maxNodeLabels          = 100
	maxNodeLabelValueRunes = 1024
)

func nodeConfigFacts(config clusterNodeConfig) *NodeConfigFacts {
	names := make([]string, 0, len(config.Labels))
	for name := range config.Labels {
		names = append(names, name)
	}
	sort.Strings(names)
	labels := make(map[string]string, min(len(names), maxNodeLabels))
	truncated := len(names) > maxNodeLabels
	for _, name := range names[:min(len(names), maxNodeLabels)] {
		value, shortened := boundedRunes(config.Labels[name], maxNodeLabelValueRunes)
		label, labelShortened := boundedRunes(name, maxNodeLabelValueRunes)
		labels[label] = value
		truncated = truncated || shortened || labelShortened
	}
	env, _ := boundedRunes(config.Env, maxNodeLabelValueRunes)
	return &NodeConfigFacts{
		Env: env, Labels: labels, LabelsTruncated: truncated,
		MaintenanceGracePeriodNS: config.MaintenanceGracePeriod,
		RejoinGracePeriodNS:      config.RejoinGracePeriod,
		ReadyPeriodNS:            config.ReadyPeriod,
	}
}

// validateNodeLogIDs accepts empty ids or canonical UUIDs, lowered as the
// daemon logs them: a journal match is case sensitive.
func validateNodeLogIDs(options GetNodeLogsOptions) (NodeLogIDs, error) {
	var ids NodeLogIDs
	for _, field := range []struct {
		name   string
		value  string
		target *string
	}{
		{"exec_id", options.ExecID, &ids.ExecID},
		{"session_id", options.SessionID, &ids.SessionID},
		{"orchestration_id", options.OrchestrationID, &ids.OrchestrationID},
	} {
		if field.value == "" {
			continue
		}
		if !daemonExecutionUUIDPattern.MatchString(field.value) {
			return NodeLogIDs{}, fmt.Errorf("node log %s must be a canonical UUID", field.name)
		}
		*field.target = strings.ToLower(field.value)
	}
	return ids, nil
}

func parseNodeLogEntry(data []byte, expectedNode string, expectedComponent string, expectedIDs NodeLogIDs) (NodeLogEntry, error) {
	var envelope daemonNodeLogEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return NodeLogEntry{}, fmt.Errorf("decode node log envelope: %w", err)
	}
	payload, err := envelope.daemonInstanceLogEnvelope.payload()
	if err != nil {
		return NodeLogEntry{}, fmt.Errorf("decode nested OpenSVC node log payload: %w", err)
	}
	if payload.Node != "" && payload.Node != expectedNode {
		return NodeLogEntry{}, fmt.Errorf("node log returned unexpected node %q", payload.Node)
	}
	if expectedComponent != "" && payload.Component != expectedComponent {
		return NodeLogEntry{}, fmt.Errorf("node log returned unexpected component %q", payload.Component)
	}
	for name, ids := range map[string][2]string{
		"execution":     {expectedIDs.ExecID, payload.ExecID},
		"session":       {expectedIDs.SessionID, payload.SessionID},
		"orchestration": {expectedIDs.OrchestrationID, payload.OrchestrationID},
	} {
		if ids[0] != "" && ids[1] != ids[0] {
			return NodeLogEntry{}, fmt.Errorf("node log returned unexpected %s id %q", name, boundInstanceLogField(ids[1]))
		}
	}
	message := normalizeInstanceLogText(payload.Message)
	if message == "" {
		return NodeLogEntry{}, fmt.Errorf("node log entry has no message")
	}
	timestamp := payload.Timestamp
	if envelope.JSON == "" || timestamp == envelope.Timestamp {
		if micros, err := strconv.ParseInt(timestamp, 10, 64); err == nil {
			timestamp = time.UnixMicro(micros).UTC().Format(time.RFC3339Nano)
		}
	}
	return NodeLogEntry{
		Timestamp:       boundInstanceLogField(timestamp),
		Level:           strings.ToLower(boundInstanceLogField(payload.Level)),
		Priority:        boundInstanceLogField(envelope.Priority),
		Message:         message,
		Component:       boundInstanceLogField(payload.Component),
		SystemdUnit:     boundInstanceLogField(envelope.SystemdUnit),
		ObjectPath:      boundInstanceLogField(payload.Object),
		ResourceID:      boundInstanceLogField(payload.ResourceID),
		SessionID:       boundInstanceLogField(payload.SessionID),
		ExecID:          boundInstanceLogField(payload.ExecID),
		EventID:         boundInstanceLogField(payload.EventID),
		RequestID:       boundInstanceLogField(payload.RequestID),
		OrchestrationID: boundInstanceLogField(payload.OrchestrationID),
	}, nil
}

func boundNodeLogEntries(entries []NodeLogEntry) ([]NodeLogEntry, bool) {
	remaining := maxInstanceLogsTotalMessageRunes
	selected := make([]NodeLogEntry, 0, len(entries))
	truncated := false
	for index := len(entries) - 1; index >= 0; index-- {
		if remaining == 0 {
			truncated = true
			break
		}
		entry := entries[index]
		message, shortened, used := boundInstanceLogMessage(entry.Message, remaining)
		entry.Message = message
		entry.MessageTruncated = shortened
		remaining -= used
		truncated = truncated || shortened
		selected = append(selected, entry)
	}
	slices.Reverse(selected)
	return selected, truncated
}
