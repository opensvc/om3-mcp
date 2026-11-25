package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxInstanceStatusOutputBytes = 256 << 10
	maxInstanceStatusTruncations = 256
	maxInstanceStatusJSONDepth   = 4
	maxInstanceStatusJSONNodes   = 128
	maxInstanceStatusEncapDepth  = 2
)

type GetInstanceStatusOptions struct {
	Path string
	Node string
}

type InstanceStatusSnapshot struct {
	Provenance  Provenance             `json:"provenance" jsonschema:"API source and MCP collection time; does not date the cached blocks"`
	Object      ClusterObjectReference `json:"object" jsonschema:"the canonical object reference"`
	Node        string                 `json:"node" jsonschema:"the exact instance node reported by the daemon"`
	Config      *InstanceConfigDetail  `json:"config" jsonschema:"cached published configuration descriptor; not the configuration file"`
	Monitor     *InstanceMonitorDetail `json:"monitor" jsonschema:"cached monitor facts, or null when not published"`
	Status      *InstanceStatusDetail  `json:"status" jsonschema:"cached instance and resource status, or null when not published"`
	Truncated   bool                   `json:"truncated" jsonschema:"whether nested collections or text were shortened"`
	Truncations []InstanceTruncation   `json:"truncations" jsonschema:"reductions sorted by JSON Pointer and kind; empty when nothing was shortened"`
	FailureNote string                 `json:"failure_note,omitempty" jsonschema:"set when the monitor state is a failure, such as start failed: where its cause is"`
}

// failedStateNote tells where the cause of a failed monitor state is: the
// status keeps the state, not the error of the resource that failed, which
// the logs of the instance hold.
const failedStateNote = "the monitor state %q keeps no error message: the cause, such as a command not found, a device missing or an image not pulled, is in the logs of this instance, get_instance_logs with this path and node"

type InstanceTruncation struct {
	Path          string `json:"path" jsonschema:"JSON Pointer to the shortened field"`
	Kind          string `json:"kind" jsonschema:"collection for omitted entries or text for shortened Unicode text"`
	OriginalCount int    `json:"original_count" jsonschema:"original number of elements, or Unicode code points for text"`
	ReturnedCount int    `json:"returned_count" jsonschema:"returned number of elements, or Unicode code points for text"`
}

type InstanceConfigDetail struct {
	Checksum         string                            `json:"csum" jsonschema:"published configuration checksum"`
	Priority         int64                             `json:"priority" jsonschema:"exact configured object priority"`
	Scope            []string                          `json:"scope" jsonschema:"configured nodes in original placement priority order"`
	UpdatedAt        *string                           `json:"updated_at" jsonschema:"configuration publication timestamp, or null when absent"`
	Labels           map[string]string                 `json:"labels,omitzero" jsonschema:"bounded configured labels"`
	App              *string                           `json:"app,omitempty" jsonschema:"configured application name when supplied"`
	Env              *string                           `json:"env,omitempty" jsonschema:"configured environment when supplied"`
	DRP              *bool                             `json:"drp,omitempty" jsonschema:"configured disaster recovery flag when supplied"`
	Children         []string                          `json:"children,omitzero" jsonschema:"child object relations sorted by exact path"`
	Parents          []string                          `json:"parents,omitzero" jsonschema:"parent object relations sorted by exact path"`
	MonitorAction    []string                          `json:"monitor_action,omitzero" jsonschema:"configured monitor actions in source order"`
	PreMonitorAction *string                           `json:"pre_monitor_action,omitempty" jsonschema:"configured pre-monitor action text"`
	Orchestrate      *string                           `json:"orchestrate,omitempty" jsonschema:"exact orchestration mode"`
	PlacementPolicy  *string                           `json:"placement_policy,omitempty" jsonschema:"exact placement policy; scope order is preserved"`
	Resources        map[string]InstanceResourceConfig `json:"resources,omitzero" jsonschema:"bounded published resource configuration keyed by exact RID"`
	Schedules        []InstanceScheduleConfig          `json:"schedules,omitzero" jsonschema:"schedule configurations sorted by key action expression and remaining fields"`
	Stonith          *bool                             `json:"stonith,omitempty" jsonschema:"configured fencing flag when supplied"`
	Subsets          map[string]InstanceSubsetConfig   `json:"subsets,omitzero" jsonschema:"bounded subset configuration keyed by subset name"`
	Topology         *string                           `json:"topology,omitempty" jsonschema:"exact configured topology"`
	Flex             *InstanceFlexConfig               `json:"flex,omitempty" jsonschema:"configured flex targets when supplied"`
	IsDisabled       *bool                             `json:"is_disabled,omitempty" jsonschema:"disabled flag from configuration, independent of resource status"`
	Claims           map[string]int64                  `json:"claims,omitzero" jsonschema:"compute claims; cpu in thousandths, memory in bytes, minus one means uncapped"`
	Pool             *string                           `json:"pool,omitempty" jsonschema:"configured volume pool when supplied"`
	Size             *int64                            `json:"size,omitempty" jsonschema:"configured volume size in bytes when supplied"`
	Charges          map[string]int64                  `json:"charges,omitzero" jsonschema:"volume charges keyed by pool name"`
}

type InstanceResourceConfig struct {
	IsDisabled   bool   `json:"is_disabled" jsonschema:"exact configuration disabled flag"`
	IsMonitored  bool   `json:"is_monitored" jsonschema:"exact configuration monitor flag"`
	IsStandby    bool   `json:"is_standby" jsonschema:"exact configuration standby flag"`
	Restart      *int64 `json:"restart,omitempty" jsonschema:"configured automatic restart count when supplied"`
	RestartDelay *int64 `json:"restart_delay,omitempty" jsonschema:"configured restart delay in nanoseconds when supplied"`
}

type InstanceScheduleConfig struct {
	Action             string  `json:"action" jsonschema:"configured schedule action"`
	Key                string  `json:"key" jsonschema:"configuration key owning this schedule"`
	Schedule           string  `json:"schedule" jsonschema:"raw schedule expression"`
	MaxParallel        int64   `json:"max_parallel" jsonschema:"configured maximum parallel runs"`
	Require            *string `json:"require,omitempty" jsonschema:"raw resource requirement when supplied"`
	RequireCollector   bool    `json:"require_collector" jsonschema:"whether the action requires the collector"`
	RequireProvisioned bool    `json:"require_provisioned" jsonschema:"whether the action requires provisioning"`
}

type InstanceSubsetConfig struct {
	Parallel *bool `json:"parallel,omitempty" jsonschema:"configured subset parallel flag when supplied"`
}

type InstanceFlexConfig struct {
	Min    *int64 `json:"min,omitempty" jsonschema:"configured minimum instances when supplied"`
	Max    *int64 `json:"max,omitempty" jsonschema:"configured maximum instances when supplied"`
	Target *int64 `json:"target,omitempty" jsonschema:"configured target instances when supplied"`
}

type InstanceMonitorDetail struct {
	GlobalExpect            string                             `json:"global_expect" jsonschema:"exact monitor global target"`
	GlobalExpectUpdatedAt   *string                            `json:"global_expect_updated_at" jsonschema:"global target timestamp, or null"`
	GlobalExpectOptions     *map[string]any                    `json:"global_expect_options" jsonschema:"bounded JSON options preserving source types, or null"`
	IsLeader                bool                               `json:"is_leader" jsonschema:"exact provisioning leader flag"`
	IsHALeader              bool                               `json:"is_ha_leader" jsonschema:"exact HA leader flag"`
	LocalExpect             string                             `json:"local_expect" jsonschema:"exact monitor local target"`
	LocalExpectUpdatedAt    *string                            `json:"local_expect_updated_at" jsonschema:"local target timestamp, or null"`
	OrchestrationID         string                             `json:"orchestration_id" jsonschema:"reported orchestration UUID, including the zero UUID"`
	OrchestrationIsDone     bool                               `json:"orchestration_is_done" jsonschema:"reported orchestration completion flag"`
	SessionID               string                             `json:"session_id" jsonschema:"reported session UUID, including the zero UUID"`
	State                   string                             `json:"state" jsonschema:"exact monitor state, including unknown values"`
	StateUpdatedAt          *string                            `json:"state_updated_at" jsonschema:"monitor state timestamp, or null"`
	MonitorActionExecutedAt *string                            `json:"monitor_action_executed_at" jsonschema:"last monitor action timestamp, or null"`
	Preserved               bool                               `json:"preserved" jsonschema:"exact monitor preserved flag"`
	UpdatedAt               *string                            `json:"updated_at" jsonschema:"monitor publication timestamp, or null"`
	Resources               map[string]InstanceResourceMonitor `json:"resources,omitzero" jsonschema:"bounded restart monitor facts keyed by RID"`
	Parents                 map[string]string                  `json:"parents,omitzero" jsonschema:"bounded exact statuses of parent objects"`
	Children                map[string]string                  `json:"children,omitzero" jsonschema:"bounded exact statuses of child objects"`
}

type InstanceResourceMonitor struct {
	Restart *InstanceResourceRestart `json:"restart,omitempty" jsonschema:"published automatic restart state when supplied"`
}

type InstanceResourceRestart struct {
	Remaining *int64  `json:"remaining,omitempty" jsonschema:"remaining restart attempts when supplied"`
	LastAt    *string `json:"last_at" jsonschema:"last automatic restart timestamp, or null"`
}

type InstanceStatusDetail struct {
	InstanceStatusFields
	Encap map[string]InstanceEncapStatusDetail `json:"encap,omitzero" jsonschema:"bounded encapsulated statuses, at most two levels"`
}

type InstanceEncapStatusDetail struct {
	InstanceStatusFields
	Encap map[string]InstanceEncapStatusLeaf `json:"encap,omitzero" jsonschema:"bounded second-level encapsulated statuses"`
}

type InstanceEncapStatusLeaf struct {
	InstanceStatusFields
	Encap map[string]struct{} `json:"encap,omitzero" jsonschema:"empty when supplied; further encapsulated statuses are omitted with truncation metadata"`
}

// The finite status hierarchy keeps the daemon's JSON layout while allowing
// the SDK to infer the output schema without recursive Go types.
type InstanceStatusFields struct {
	Availability  string                            `json:"avail" jsonschema:"exact instance availability"`
	Overall       string                            `json:"overall" jsonschema:"exact instance overall status"`
	Provisioned   string                            `json:"provisioned" jsonschema:"exact instance provisioned status"`
	FrozenAt      *string                           `json:"frozen_at" jsonschema:"instance freeze timestamp, or null"`
	LastStartedAt *string                           `json:"last_started_at" jsonschema:"last instance start timestamp, or null"`
	StoppedAt     *string                           `json:"stopped_at" jsonschema:"intentional stop timestamp, or null"`
	UpdatedAt     *string                           `json:"updated_at" jsonschema:"last cached status observation timestamp, or null"`
	Optional      *string                           `json:"optional,omitempty" jsonschema:"exact optional-resource aggregate status when supplied"`
	Resources     map[string]InstanceResourceDetail `json:"resources,omitzero" jsonschema:"bounded complete diagnostic resource statuses keyed by RID"`
	Running       []InstanceRunningInfo             `json:"running,omitzero" jsonschema:"running actions sorted by RID timestamp PID and session"`
	Hostname      *string                           `json:"hostname,omitempty" jsonschema:"reported encapsulated hostname when supplied"`
}

type InstanceResourceDetail struct {
	Type        string                      `json:"type" jsonschema:"exact resource driver type"`
	Label       string                      `json:"label" jsonschema:"bounded resource label"`
	Status      string                      `json:"status" jsonschema:"exact resource status"`
	Provisioned InstanceResourceProvisioned `json:"provisioned" jsonschema:"published resource provisioning state and timestamp"`
	Disable     *bool                       `json:"disable,omitempty" jsonschema:"disabled flag from status when supplied"`
	Monitor     *bool                       `json:"monitor,omitempty" jsonschema:"monitor flag from status when supplied"`
	Optional    *bool                       `json:"optional,omitempty" jsonschema:"optional flag from status when supplied"`
	Standby     *bool                       `json:"standby,omitempty" jsonschema:"standby flag from status when supplied"`
	Encap       *bool                       `json:"encap,omitempty" jsonschema:"encapsulation flag from status when supplied"`
	Stopped     *bool                       `json:"stopped,omitempty" jsonschema:"intentional stop flag from status when supplied"`
	Subset      *string                     `json:"subset,omitempty" jsonschema:"reported resource subset when supplied"`
	Tags        []string                    `json:"tags,omitzero" jsonschema:"bounded resource tags sorted by exact value"`
	Log         []ResourceLogEntry          `json:"log,omitzero" jsonschema:"bounded status messages in daemon-provided order"`
	Info        map[string]any              `json:"info,omitzero" jsonschema:"bounded driver facts preserving JSON value types"`
	Datastores  []string                    `json:"datastores,omitzero" jsonschema:"bounded datastore references sorted by exact path"`
	Files       []InstanceResourceFile      `json:"files,omitzero" jsonschema:"bounded replicated file metadata sorted by name checksum timestamp and ingest flag"`
}

type InstanceResourceProvisioned struct {
	State string  `json:"state" jsonschema:"exact provisioning state"`
	Mtime *string `json:"mtime" jsonschema:"provisioning timestamp, or null"`
}

type InstanceResourceFile struct {
	Name   string  `json:"name" jsonschema:"reported replicated file name; no content is read"`
	Csum   string  `json:"csum" jsonschema:"reported file checksum"`
	Mtime  *string `json:"mtime" jsonschema:"reported file timestamp, or null"`
	Ingest bool    `json:"ingest" jsonschema:"reported file ingestion flag"`
}

type InstanceRunningInfo struct {
	RID       string  `json:"rid" jsonschema:"resource owning the running action"`
	SessionID string  `json:"session_id" jsonschema:"reported running action session identifier"`
	PID       int64   `json:"pid" jsonschema:"reported running action process identifier"`
	At        *string `json:"at" jsonschema:"reported running action timestamp, or null"`
}

type daemonInstanceItem struct {
	Kind string `json:"kind"`
	Meta struct {
		Node   string `json:"node"`
		Object string `json:"object"`
	} `json:"meta"`
	Data json.RawMessage `json:"data"`
}

func (s *Service) GetInstanceStatus(ctx context.Context, options GetInstanceStatusOptions) (InstanceStatusSnapshot, error) {
	reference, err := validateExactObjectPath(options.Path)
	if err != nil {
		return InstanceStatusSnapshot{}, err
	}
	if err := validateNodeTarget(options.Node); err != nil {
		return InstanceStatusSnapshot{}, err
	}
	endpoint := fmt.Sprintf("/api/node/name/%s/instance/path/%s/%s/%s",
		url.PathEscape(options.Node), url.PathEscape(reference.Namespace), url.PathEscape(reference.Kind), url.PathEscape(reference.Name))
	var item daemonInstanceItem
	if err := s.client.GetJSON(ctx, endpoint, url.Values{}, &item); err != nil {
		return InstanceStatusSnapshot{}, fmt.Errorf("get instance status: %w", err)
	}
	// Another kind is an answer of another API; the instance of another node
	// or object, an inconsistency of the daemon, stays an error of the request.
	if item.Kind != "InstanceItem" {
		return InstanceStatusSnapshot{}, shapeErrorf("get instance status: unexpected instance kind")
	}
	if item.Meta.Node != options.Node || item.Meta.Object != reference.Path {
		return InstanceStatusSnapshot{}, fmt.Errorf("get instance status: unexpected instance identity")
	}
	decoder := json.NewDecoder(bytes.NewReader(item.Data))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil || data == nil {
		return InstanceStatusSnapshot{}, fmt.Errorf("get instance status: invalid instance data")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return InstanceStatusSnapshot{}, fmt.Errorf("get instance status: trailing instance data")
	}
	if data["config"] == nil {
		return InstanceStatusSnapshot{}, fmt.Errorf("get instance status: missing published configuration")
	}
	projection := instanceStatusProjection{truncations: []InstanceTruncation{}}
	shapes := instanceStatusShapes()
	blocks := make(map[string]any, 3)
	for _, name := range []string{"config", "monitor", "status"} {
		value := data[name]
		if value == nil && name != "config" {
			blocks[name] = nil
			continue
		}
		normalized, err := projection.normalize(value, shapes[name], "/"+name, 0)
		if err != nil {
			return InstanceStatusSnapshot{}, fmt.Errorf("get instance status: %w", err)
		}
		blocks[name] = normalized
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return InstanceStatusSnapshot{}, fmt.Errorf("get instance status: encode projection: %w", err)
	}
	var result InstanceStatusSnapshot
	outputDecoder := json.NewDecoder(bytes.NewReader(encoded))
	outputDecoder.UseNumber()
	if err := outputDecoder.Decode(&result); err != nil {
		return InstanceStatusSnapshot{}, fmt.Errorf("get instance status: decode typed projection: %w", err)
	}
	result.Object = reference
	result.Node = item.Meta.Node
	result.Truncated = len(projection.truncations) > 0
	sort.Slice(projection.truncations, func(i, j int) bool {
		left, right := projection.truncations[i], projection.truncations[j]
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		return left.Kind < right.Kind
	})
	result.Truncations = projection.truncations
	if result.Monitor != nil && strings.HasSuffix(strings.TrimSpace(result.Monitor.State), "failed") {
		result.FailureNote = fmt.Sprintf(failedStateNote, result.Monitor.State)
	}
	result.Provenance = s.newProvenance()
	encoded, err = json.Marshal(result)
	if err != nil {
		return InstanceStatusSnapshot{}, fmt.Errorf("get instance status: encode result: %w", err)
	}
	if len(encoded) > maxInstanceStatusOutputBytes {
		return InstanceStatusSnapshot{}, fmt.Errorf("get instance status: result exceeds %d bytes; use the specialized resource or schedule tools", maxInstanceStatusOutputBytes)
	}
	return result, nil
}

// The private shapes describe only this endpoint's diagnostic projection.
// They validate and bound source data before converting it to the public types.
type instanceStatusShape struct {
	kind     string
	fields   map[string]*instanceStatusShape
	required []string
	element  *instanceStatusShape
	limit    int
	order    []string
	encap    bool
}

func instanceStatusShapes() map[string]*instanceStatusShape {
	identity := &instanceStatusShape{kind: "identity", limit: 512}
	text := &instanceStatusShape{kind: "text", limit: 4096}
	date := &instanceStatusShape{kind: "date"}
	integer := &instanceStatusShape{kind: "integer"}
	boolean := &instanceStatusShape{kind: "boolean"}
	variable := &instanceStatusShape{kind: "json"}
	object := func(fields map[string]*instanceStatusShape, required ...string) *instanceStatusShape {
		return &instanceStatusShape{kind: "object", fields: fields, required: required}
	}
	mapOf := func(element *instanceStatusShape, limit int) *instanceStatusShape {
		return &instanceStatusShape{kind: "map", element: element, limit: limit}
	}
	listOf := func(element *instanceStatusShape, limit int, order ...string) *instanceStatusShape {
		return &instanceStatusShape{kind: "list", element: element, limit: limit, order: order}
	}
	resourceConfig := object(map[string]*instanceStatusShape{
		"is_disabled": boolean, "is_monitored": boolean, "is_standby": boolean,
		"restart": integer, "restart_delay": integer,
	}, "is_disabled", "is_monitored", "is_standby")
	schedule := object(map[string]*instanceStatusShape{
		"key": identity, "action": identity, "schedule": text, "max_parallel": integer,
		"require": text, "require_collector": boolean, "require_provisioned": boolean,
	}, "key", "action", "schedule", "max_parallel", "require_collector", "require_provisioned")
	config := object(map[string]*instanceStatusShape{
		"csum": identity, "priority": integer, "scope": listOf(identity, 200), "updated_at": date,
		"labels": mapOf(text, 100), "app": text, "env": text, "drp": boolean,
		"children": listOf(identity, 200, ""), "parents": listOf(identity, 200, ""),
		"monitor_action": listOf(identity, 20), "pre_monitor_action": text,
		"orchestrate": identity, "placement_policy": identity, "resources": mapOf(resourceConfig, 200),
		"schedules": listOf(schedule, 200, "key", "action", "schedule"), "stonith": boolean,
		"subsets":  mapOf(object(map[string]*instanceStatusShape{"parallel": boolean}), 200),
		"topology": identity, "flex": object(map[string]*instanceStatusShape{"min": integer, "max": integer, "target": integer}),
		"is_disabled": boolean, "claims": mapOf(integer, 100), "pool": identity,
		"size": integer, "charges": mapOf(integer, 100),
	}, "csum", "priority", "scope")
	monitor := object(map[string]*instanceStatusShape{
		"global_expect": identity, "global_expect_updated_at": date,
		"global_expect_options": variable, "is_leader": boolean, "is_ha_leader": boolean,
		"local_expect": identity, "local_expect_updated_at": date, "orchestration_id": identity,
		"orchestration_is_done": boolean, "session_id": identity, "state": identity,
		"state_updated_at": date, "monitor_action_executed_at": date, "preserved": boolean, "updated_at": date,
		"resources": mapOf(object(map[string]*instanceStatusShape{
			"restart": object(map[string]*instanceStatusShape{"remaining": integer, "last_at": date}),
		}), 200),
		"parents": mapOf(identity, 200), "children": mapOf(identity, 200),
	}, "global_expect", "local_expect", "is_leader", "is_ha_leader", "orchestration_id", "orchestration_is_done", "session_id", "state", "preserved")
	resourceStatus := object(map[string]*instanceStatusShape{
		"type": identity, "label": text, "status": identity,
		"provisioned": object(map[string]*instanceStatusShape{"state": identity, "mtime": date}, "state"),
		"disable":     boolean, "monitor": boolean, "optional": boolean, "standby": boolean,
		"encap": boolean, "stopped": boolean, "subset": identity,
		"tags": listOf(identity, 100, ""), "datastores": listOf(identity, 100, ""),
		"log": listOf(object(map[string]*instanceStatusShape{
			"level": identity, "message": {kind: "text", limit: 2048},
		}, "level", "message"), 20),
		"info": mapOf(variable, 100),
		"files": listOf(object(map[string]*instanceStatusShape{
			"name": identity, "csum": identity, "mtime": date, "ingest": boolean,
		}, "name", "csum", "ingest"), 100, "name", "csum", "mtime", "ingest"),
	}, "type", "label", "status", "provisioned")
	status := object(map[string]*instanceStatusShape{
		"avail": identity, "overall": identity, "provisioned": identity,
		"frozen_at": date, "last_started_at": date, "stopped_at": date, "updated_at": date,
		"optional": identity, "hostname": identity, "resources": mapOf(resourceStatus, 200),
		"running": listOf(object(map[string]*instanceStatusShape{
			"rid": identity, "session_id": identity, "pid": integer, "at": date,
		}, "rid", "session_id", "pid"), 200, "rid", "at", "pid", "session_id"),
	}, "avail", "overall", "provisioned")
	status.fields["encap"] = &instanceStatusShape{kind: "map", element: status, limit: 32, encap: true}
	return map[string]*instanceStatusShape{"config": config, "monitor": monitor, "status": status}
}

type instanceStatusProjection struct {
	truncations []InstanceTruncation
}

type instanceStatusOmitted struct{}

func (p *instanceStatusProjection) reduction(path, kind string, total, count int) error {
	if len(p.truncations) >= maxInstanceStatusTruncations {
		return fmt.Errorf("too many instance truncations")
	}
	p.truncations = append(p.truncations, InstanceTruncation{Path: path, Kind: kind, OriginalCount: total, ReturnedCount: count})
	return nil
}

func (p *instanceStatusProjection) normalize(value any, shape *instanceStatusShape, path string, encapDepth int) (any, error) {
	invalid := func() (any, error) { return nil, fmt.Errorf("invalid %s at %s", shape.kind, path) }
	switch shape.kind {
	case "identity", "text":
		text, ok := value.(string)
		if !ok || !utf8.ValidString(text) {
			return invalid()
		}
		length := utf8.RuneCountInString(text)
		if length > shape.limit {
			if shape.kind == "identity" {
				return nil, fmt.Errorf("identity exceeds %d Unicode code points at %s", shape.limit, path)
			}
			bounded := string([]rune(text)[:shape.limit-1]) + "…"
			return bounded, p.reduction(path, "text", length, shape.limit)
		}
		return text, nil
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return invalid()
		}
		if _, err := number.Int64(); err != nil {
			return invalid()
		}
		return number, nil
	case "boolean":
		if _, ok := value.(bool); !ok {
			return invalid()
		}
		return value, nil
	case "date":
		if value == nil {
			return nil, nil
		}
		text, ok := value.(string)
		if !ok || len(text) > 128 {
			return invalid()
		}
		parsed, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return invalid()
		}
		if parsed.IsZero() {
			return nil, nil
		}
		return text, nil
	case "object":
		fields, ok := value.(map[string]any)
		if !ok || fields == nil {
			return invalid()
		}
		for _, name := range shape.required {
			if fields[name] == nil {
				return nil, fmt.Errorf("missing required field at %s", instancePointer(path, name))
			}
		}
		result := make(map[string]any)
		for _, name := range instanceSortedKeys(shape.fields) {
			childShape := shape.fields[name]
			child, exists := fields[name]
			if !exists && childShape.kind != "date" && name != "global_expect_options" {
				continue
			}
			if name == "global_expect_options" && child != nil {
				options, ok := child.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid monitor options at %s", instancePointer(path, name))
				}
				if value, exists := options["config_updated_at"]; exists {
					normalized, err := p.normalize(value, &instanceStatusShape{kind: "date"}, instancePointer(instancePointer(path, name), "config_updated_at"), encapDepth)
					if err != nil {
						return nil, err
					}
					options["config_updated_at"] = normalized
				}
			}
			normalized, err := p.normalize(child, childShape, instancePointer(path, name), encapDepth)
			if err != nil {
				return nil, err
			}
			if _, omitted := normalized.(instanceStatusOmitted); !omitted {
				result[name] = normalized
			}
		}
		return result, nil
	case "map":
		fields, ok := value.(map[string]any)
		if !ok || fields == nil {
			return invalid()
		}
		keys := instanceSortedKeys(fields)
		for _, key := range keys {
			if !utf8.ValidString(key) || utf8.RuneCountInString(key) > 512 {
				return nil, fmt.Errorf("invalid map key at %s", path)
			}
		}
		if shape.encap {
			if encapDepth >= maxInstanceStatusEncapDepth && len(keys) > 0 {
				return instanceStatusOmitted{}, p.reduction(path, "collection", len(keys), 0)
			}
			encapDepth++
		}
		end := min(len(keys), shape.limit)
		if end < len(keys) {
			if err := p.reduction(path, "collection", len(keys), end); err != nil {
				return nil, err
			}
		}
		result := make(map[string]any, end)
		for _, name := range keys[:end] {
			normalized, err := p.normalize(fields[name], shape.element, instancePointer(path, name), encapDepth)
			if err != nil {
				return nil, err
			}
			result[name] = normalized
		}
		return result, nil
	case "list":
		items, ok := value.([]any)
		if !ok {
			return invalid()
		}
		// Validate sort keys before sorting. Normalize and shorten text only after
		// selection, so truncation cannot change which source records are retained.
		if len(shape.order) > 0 {
			for _, item := range items {
				if shape.element.kind == "object" {
					if _, ok := item.(map[string]any); !ok {
						return invalid()
					}
				} else if _, ok := item.(string); !ok {
					return invalid()
				}
			}
			sort.SliceStable(items, func(i, j int) bool { return instanceCompare(items[i], items[j], shape.order) < 0 })
		}
		end := min(len(items), shape.limit)
		if end < len(items) {
			if err := p.reduction(path, "collection", len(items), end); err != nil {
				return nil, err
			}
		}
		result := make([]any, end)
		for index, item := range items[:end] {
			normalized, err := p.normalize(item, shape.element, instancePointer(path, fmt.Sprint(index)), encapDepth)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	case "json":
		nodes := 0
		return p.normalizeJSON(value, path, 0, &nodes)
	default:
		return nil, fmt.Errorf("unsupported instance projection at %s", path)
	}
}

func (p *instanceStatusProjection) normalizeJSON(value any, path string, depth int, nodes *int) (any, error) {
	*nodes = *nodes + 1
	if depth > maxInstanceStatusJSONDepth || *nodes > maxInstanceStatusJSONNodes {
		return nil, fmt.Errorf("instance JSON depth or node limit exceeded at %s", path)
	}
	switch value := value.(type) {
	case nil, bool:
		return value, nil
	case json.Number:
		if len(value.String()) > 512 {
			return nil, fmt.Errorf("instance JSON number too large at %s", path)
		}
		return value, nil
	case string:
		return p.normalize(value, &instanceStatusShape{kind: "text", limit: 4096}, path, 0)
	case []any:
		end := min(len(value), 100)
		if end < len(value) {
			if err := p.reduction(path, "collection", len(value), end); err != nil {
				return nil, err
			}
		}
		result := make([]any, end)
		for index, child := range value[:end] {
			normalized, err := p.normalizeJSON(child, instancePointer(path, fmt.Sprint(index)), depth+1, nodes)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	case map[string]any:
		keys := instanceSortedKeys(value)
		end := min(len(keys), 100)
		if end < len(keys) {
			if err := p.reduction(path, "collection", len(keys), end); err != nil {
				return nil, err
			}
		}
		result := make(map[string]any, end)
		for _, name := range keys[:end] {
			if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 512 {
				return nil, fmt.Errorf("invalid instance JSON key at %s", path)
			}
			normalized, err := p.normalizeJSON(value[name], instancePointer(path, name), depth+1, nodes)
			if err != nil {
				return nil, err
			}
			result[name] = normalized
		}
		return result, nil
	default:
		return nil, fmt.Errorf("invalid instance JSON value at %s", path)
	}
}

func instancePointer(parent, key string) string {
	return parent + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

func instanceSortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func instanceCompare(left, right any, fields []string) int {
	if text, ok := left.(string); ok {
		return strings.Compare(text, right.(string))
	}
	l, r := left.(map[string]any), right.(map[string]any)
	for _, field := range fields {
		if field == "at" || field == "mtime" {
			lt, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(l[field]))
			rt, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(r[field]))
			if comparison := lt.Compare(rt); comparison != 0 {
				return comparison
			}
		} else if field == "pid" {
			ln, lok := l[field].(json.Number)
			rn, rok := r[field].(json.Number)
			if lok && rok {
				li, _ := ln.Int64()
				ri, _ := rn.Int64()
				if li < ri {
					return -1
				}
				if li > ri {
					return 1
				}
			}
		} else if comparison := strings.Compare(fmt.Sprint(l[field]), fmt.Sprint(r[field])); comparison != 0 {
			return comparison
		}
	}
	// Tie-break on all source fields for deterministic duplicate selection.
	// JSON object keys are sorted by encoding/json; source arrays keep their order.
	lb, _ := json.Marshal(l)
	rb, _ := json.Marshal(r)
	return bytes.Compare(lb, rb)
}
