package core

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ObjectAction is an orchestrated action on one object. The daemon records
// the target state and its monitors reach it on every node of the object.
type ObjectAction string

const (
	ObjectActionFreeze   ObjectAction = "freeze"
	ObjectActionUnfreeze ObjectAction = "unfreeze"
	ObjectActionAbort    ObjectAction = "abort"
	ObjectActionStart    ObjectAction = "start"
	ObjectActionStop     ObjectAction = "stop"
	ObjectActionRestart  ObjectAction = "restart"
	ObjectActionSwitch   ObjectAction = "switch"
	ObjectActionGiveback ObjectAction = "giveback"
	// The actions of the lifecycle of an object, which require admin on its
	// namespace.
	ObjectActionProvision ObjectAction = "provision"
	ObjectActionResize    ObjectAction = "resize"
	ObjectActionCap       ObjectAction = "cap"
	ObjectActionDelete    ObjectAction = "delete"
	// An unprovision stops then unprovisions the resources of every
	// instance, which destroys the data of the disk and fs resources; a
	// purge unprovisions, then deletes the object.
	ObjectActionUnprovision ObjectAction = "unprovision"
	ObjectActionPurge       ObjectAction = "purge"
)

// objectActionKinds are the object kinds each action accepts, as the daemon
// API declares them.
var objectActionKinds = map[ObjectAction][]string{
	ObjectActionFreeze:    {"svc", "vol"},
	ObjectActionUnfreeze:  {"svc", "vol"},
	ObjectActionAbort:     {"svc", "vol", "cfg", "sec", "usr", "nscfg"},
	ObjectActionStart:     {"svc"},
	ObjectActionStop:      {"svc"},
	ObjectActionRestart:   {"svc"},
	ObjectActionSwitch:    {"svc"},
	ObjectActionGiveback:  {"svc"},
	ObjectActionProvision: {"svc", "vol"},
	// The daemon declares resize for svc, and refuses all but a vol: a svc
	// has no size to grow.
	ObjectActionResize: {"vol"},
	ObjectActionCap:    {"svc", "vol"},
	// The deletion of a namespace configuration touches the whole namespace:
	// it is not offered.
	ObjectActionDelete: {"svc", "vol", "cfg", "sec", "usr"},
	// The daemon declares unprovision and purge for svc and vol, and takes
	// any kind: the purge of a cfg, a sec or a usr is a delete.
	ObjectActionUnprovision: {"svc", "vol"},
	ObjectActionPurge:       {"svc", "vol"},
}

// ObjectActionOptions are the options of the actions that take some.
type ObjectActionOptions struct {
	// InterruptSyncs has a stop or a switch end the syncs running on the
	// instances it stops instead of waiting for them to end.
	InterruptSyncs bool
	// To is the one node a switch moves the object to. Empty, the daemon
	// selects it.
	To string
	// Live has a switch migrate the object live when its resources allow it.
	Live bool
	// Adopt has a provision take over the system resources it finds holding
	// what it would take, rather than refusing them.
	Adopt bool
	// Size is the size a resize grows the volume to, such as 11g, or the
	// amount it adds, such as +1g; empty, the size configured.
	Size string
	// Sets are the pg_* operations a cap writes, [<section>.]pg_<name>[@<node>]=<value>;
	// none applies the caps configured.
	Sets []string
	// ConfigUpdatedAt has a cap or a resize wait for the configuration
	// written at that time, as update_object_config gives it, on every node.
	ConfigUpdatedAt string
}

const maxCapOperations = 50

// resizeSize is a size a resize takes: absolute, or an amount to add.
var resizeSize = regexp.MustCompile(`(?i)^\+?[0-9]+(\.[0-9]+)?\s*[kmgtpe]?i?b?$`)

// capOperation is the form of a cap operation: a pg_* keyword of DEFAULT, a
// subset or a resource, maybe scoped, and the = operator.
var capOperation = regexp.MustCompile(`^(?:[^\s=]+\.)?pg_[a-z0-9_]+(?:@[^\s=]+)?=[^\n]*$`)

type daemonResizeRequest struct {
	Size string `json:"size,omitempty"`
}

// daemonSwitchRequest is the body of a switch. The daemon takes a list of
// destinations, which it reads as a node selector: the MCP sends at most one
// exact node.
type daemonSwitchRequest struct {
	Destination    []string `json:"destination,omitempty"`
	Live           bool     `json:"live"`
	InterruptSyncs bool     `json:"interrupt_syncs,omitempty"`
}

type ObjectActionResult struct {
	Provenance      Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object          ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference the action was submitted for"`
	Action          string                 `json:"action" jsonschema:"the action submitted"`
	OrchestrationID string                 `json:"orchestration_id" jsonschema:"the identifier of the orchestration the daemon queued; pass it to list_daemon_orchestrations and get_node_logs to follow it"`
	// LastModified is the time of the configuration a cap wrote.
	LastModified string `json:"last_modified,omitempty" jsonschema:"for a cap, the time the daemon gives the configuration it wrote, as OM-Last-Modified"`
}

type daemonOrchestrationQueued struct {
	OrchestrationID string `json:"orchestration_id"`
	// Node is the node that accepted the orchestration, which answers for
	// it at once.
	Node string `json:"node"`
}

// SubmitObjectAction submits one orchestrated action on one exact object and
// returns as soon as the daemon queued it. It does not wait for the object to
// reach the target state.
func (s *Service) SubmitObjectAction(ctx context.Context, path string, action ObjectAction, options ObjectActionOptions) (ObjectActionResult, error) {
	kinds, known := objectActionKinds[action]
	if !known {
		return ObjectActionResult{}, fmt.Errorf("unsupported object action %q", action)
	}
	if hasSelectorCharacters(path) {
		return ObjectActionResult{}, fmt.Errorf("path must be one exact OpenSVC object path, without wildcard or selector")
	}
	reference, err := validateExactObjectPath(path)
	if err != nil {
		return ObjectActionResult{}, err
	}
	if !slices.Contains(kinds, reference.Kind) {
		if action == ObjectActionPurge && slices.Contains(objectActionKinds[ObjectActionDelete], reference.Kind) {
			return ObjectActionResult{}, fmt.Errorf("a %s object holds nothing to unprovision: its purge is a delete, delete_object", reference.Kind)
		}
		return ObjectActionResult{}, fmt.Errorf("the %s action applies to %s objects only", action, strings.Join(kinds, ", "))
	}
	if options.InterruptSyncs && action != ObjectActionStop && action != ObjectActionSwitch {
		return ObjectActionResult{}, fmt.Errorf("interrupt_syncs applies to the stop and switch actions only")
	}
	if (options.To != "" || options.Live) && action != ObjectActionSwitch {
		return ObjectActionResult{}, fmt.Errorf("to and live apply to the switch action only")
	}
	if err := validateLifecycleOptions(action, options); err != nil {
		return ObjectActionResult{}, err
	}
	var query url.Values
	var body any
	switch action {
	case ObjectActionStop:
		if options.InterruptSyncs {
			query = url.Values{"interrupt_syncs": {"true"}}
		}
	case ObjectActionSwitch:
		request := daemonSwitchRequest{Live: options.Live, InterruptSyncs: options.InterruptSyncs}
		if options.To != "" {
			if err := validateNodeTarget(options.To); err != nil {
				return ObjectActionResult{}, fmt.Errorf("to: %w", err)
			}
			request.Destination = []string{options.To}
		}
		body = request
	case ObjectActionProvision:
		if options.Adopt {
			query = url.Values{"adopt": {"true"}}
		}
	case ObjectActionResize:
		body = daemonResizeRequest{Size: strings.TrimSpace(options.Size)}
	case ObjectActionCap:
		query = url.Values{}
		for _, operation := range options.Sets {
			query.Add("set", operation)
		}
	case ObjectActionDelete:
		if err := s.refuseLeakingDelete(ctx, reference); err != nil {
			return ObjectActionResult{}, err
		}
	case ObjectActionUnprovision, ObjectActionPurge:
		if err := s.refuseUnprovisionUp(ctx, reference, action); err != nil {
			return ObjectActionResult{}, err
		}
	}
	if options.ConfigUpdatedAt != "" {
		if query == nil {
			query = url.Values{}
		}
		query.Set("config_updated_at", options.ConfigUpdatedAt)
	}
	poster, ok := s.client.(JSONPoster)
	if !ok {
		return ObjectActionResult{}, fmt.Errorf("OpenSVC daemon client does not support POST requests")
	}

	endpoint := fmt.Sprintf("/api/object/path/%s/%s/%s/action/%s", reference.Namespace, reference.Kind, reference.Name, action)
	var queued daemonOrchestrationQueued
	var lastModified string
	if action == ObjectActionCap {
		// A cap writes the configuration, and gives its time.
		headerPoster, ok := s.client.(JSONHeaderPoster)
		if !ok {
			return ObjectActionResult{}, fmt.Errorf("OpenSVC daemon client does not give the headers of a POST answer")
		}
		header, err := headerPoster.PostJSONWithHeader(ctx, endpoint, query, body, &queued)
		if err != nil {
			return ObjectActionResult{}, fmt.Errorf("%s object %s: %w", action, reference.Path, err)
		}
		lastModified = header.Get(configLastModifiedHeader)
	} else if err := poster.PostJSON(ctx, endpoint, query, body, &queued); err != nil {
		return ObjectActionResult{}, fmt.Errorf("%s object %s: %w", action, reference.Path, err)
	}
	if !daemonExecutionUUIDPattern.MatchString(queued.OrchestrationID) {
		return ObjectActionResult{}, fmt.Errorf("%s object %s: daemon returned no valid orchestration id", action, reference.Path)
	}
	return ObjectActionResult{
		Provenance:      s.newProvenance(),
		Object:          reference,
		Action:          string(action),
		OrchestrationID: strings.ToLower(queued.OrchestrationID),
		LastModified:    lastModified,
	}, nil
}

// validateLifecycleOptions checks the options of the lifecycle actions, each
// on the action that takes it, and their form; the daemon judges the rest.
func validateLifecycleOptions(action ObjectAction, options ObjectActionOptions) error {
	if options.Adopt && action != ObjectActionProvision {
		return fmt.Errorf("adopt applies to the provision action only")
	}
	if options.Size != "" && action != ObjectActionResize {
		return fmt.Errorf("size applies to the resize action only")
	}
	if len(options.Sets) > 0 && action != ObjectActionCap {
		return fmt.Errorf("sets apply to the cap action only")
	}
	if options.ConfigUpdatedAt != "" {
		if action != ObjectActionCap && action != ObjectActionResize {
			return fmt.Errorf("config_updated_at applies to the cap and resize actions only")
		}
		if _, err := time.Parse(time.RFC3339Nano, options.ConfigUpdatedAt); err != nil {
			return fmt.Errorf("config_updated_at must be an RFC 3339 time, the last_modified update_object_config gives")
		}
	}
	if size := strings.TrimSpace(options.Size); size != "" && !resizeSize.MatchString(size) {
		return fmt.Errorf("size must be a size such as 11g or 12Gi, or an amount to add such as +1g")
	}
	if len(options.Sets) > maxCapOperations {
		return fmt.Errorf("at most %d cap operations are accepted", maxCapOperations)
	}
	for _, operation := range options.Sets {
		key, _, _ := strings.Cut(operation, "=")
		if !capOperation.MatchString(operation) || strings.ContainsAny(key[len(key)-1:], "+-|^!<>") {
			return fmt.Errorf("cap operation %q must be [<section>.]pg_<name>[@<node>]=<value>, the operator = only", operation)
		}
	}
	return nil
}

// refuseLeakingDelete refuses to delete an object an instance of which
// runs, or, for a svc or a vol, holds provisioned resources: the daemon
// deletes the configuration all the same, and leaves the resources mounted,
// or the storage, held by nothing.
func (s *Service) refuseLeakingDelete(ctx context.Context, reference ClusterObjectReference) error {
	instances, err := s.ListObjectInstances(ctx, ListObjectInstancesOptions{Path: reference.Path, Limit: maxListObjectInstancesLimit})
	if err != nil {
		return fmt.Errorf("delete object %s: read its instances: %w", reference.Path, err)
	}
	for _, instance := range instances.Instances {
		switch instance.Availability {
		case "down", "n/a", "":
		default:
			return fmt.Errorf("delete object %s: the instance on %s is %s: stop it, then purge it, purge_object, which unprovisions and deletes it", reference.Path, instance.Node, instance.Availability)
		}
		if (reference.Kind == "svc" || reference.Kind == "vol") && (instance.Provisioned == "true" || instance.Provisioned == "mixed") {
			return fmt.Errorf("delete object %s: the instance on %s is provisioned: stop it, then purge it, purge_object, which unprovisions and deletes it", reference.Path, instance.Node)
		}
	}
	return nil
}

// refuseUnprovisionUp refuses to unprovision or purge an object an instance
// of which is up or warn: the daemon asks nothing, stops the instance, then
// destroys the data its disk and fs resources hold, where an operator would
// expect a refusal of a service running.
func (s *Service) refuseUnprovisionUp(ctx context.Context, reference ClusterObjectReference, action ObjectAction) error {
	instances, err := s.ListObjectInstances(ctx, ListObjectInstancesOptions{Path: reference.Path, Limit: maxListObjectInstancesLimit})
	if err != nil {
		return fmt.Errorf("%s object %s: read its instances: %w", action, reference.Path, err)
	}
	for _, instance := range instances.Instances {
		if instance.Availability == "up" || instance.Availability == "warn" {
			return fmt.Errorf("%s object %s: the instance on %s is %s: stop it first, stop_object; the %s destroys the data of its disk and fs resources", action, reference.Path, instance.Node, instance.Availability, action)
		}
	}
	return nil
}
