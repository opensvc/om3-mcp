package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// InstanceAction is an action on one instance of an object, run by the
// daemon of its node at once, outside of the orchestration of the object.
type InstanceAction string

const (
	InstanceActionStart    InstanceAction = "start"
	InstanceActionStop     InstanceAction = "stop"
	InstanceActionRestart  InstanceAction = "restart"
	InstanceActionFreeze   InstanceAction = "freeze"
	InstanceActionUnfreeze InstanceAction = "unfreeze"
	// An unprovision stops then unprovisions the resources of the instance,
	// which destroys the data of its disk and fs resources.
	InstanceActionUnprovision InstanceAction = "unprovision"
	// A run runs tasks of the instance, task#<name> resources, each the
	// command its configuration names.
	InstanceActionRun InstanceAction = "run"
	// A start of the standby resources starts the resources standby=true
	// only, and lifts the stopped flag of every resource.
	InstanceActionStartStandby InstanceAction = "startstandby"
	// A shutdown stops every resource, the standby ones too, and flags the
	// instance stopped no more than a start does.
	InstanceActionShutdown InstanceAction = "shutdown"
	// The caps updated write the pg_* keywords in the cgroups of the
	// instance; reset, they lift every capping of the object slice.
	InstanceActionPGUpdate InstanceAction = "pg/update"
	InstanceActionPGReset  InstanceAction = "pg/reset"
	// An info recomputes the resource information cache of the instance,
	// which list_resource_info reads.
	InstanceActionInfo InstanceAction = "info"
)

// instanceActionKinds are the object kinds the instance actions accept, as
// the daemon API declares them.
var instanceActionKinds = []string{"svc", "vol"}

// clearInstanceKinds are the object kinds the clear of an instance accepts.
var clearInstanceKinds = []string{"svc", "vol", "cfg", "nscfg", "sec", "usr"}

const (
	defaultInstanceActionWait = 10 * time.Second
	maxInstanceActionWait     = 60 * time.Second
	clearInstanceWait         = 10 * time.Second
)

// failedMonitorStates are the monitor states of an action that failed, the
// ones a clear lifts. The daemon sets the state a clear asks without looking
// at the current one: a clear of an action running would set the monitor
// idle under it.
var failedMonitorStates = []string{
	"boot failed", "shutdown failed", "start failed", "stop failed", "freeze failed", "unfreeze failed",
	"provision failed", "unprovision failed", "delete failed", "resize failed", "cap failed", "prepare failed", "purge failed",
}

type InstanceActionOptions struct {
	// Rids are the exact ids of the resources a start, stop, restart or
	// unprovision applies to; none, the whole instance.
	Rids []string
	// DisableRollback has a start or a restart leave started the resources
	// it started before a failure, rather than stopping them.
	DisableRollback bool
	// InterruptSyncs has a stop end the syncs running on the instance
	// instead of waiting for them to end.
	InterruptSyncs bool
	// Leader has an unprovision also unprovision the resources shared by the
	// instances, which the leader of the placement holds.
	Leader bool
	// StateOnly has an unprovision record the resources as unprovisioned,
	// touching nothing, for resources a sysadmin freed by hand.
	StateOnly bool
	// Env are the KEY=VALUE variables a run exports to the commands of its
	// tasks, after those of their configuration. Their values are never
	// returned.
	Env []string
	// ConfigUpdatedAt has the action wait for the configuration written at
	// that time, as update_object_config gives it, on the node.
	ConfigUpdatedAt string
	// Wait is how long the MCP waits for the end of the execution; zero, 10
	// seconds.
	Wait time.Duration
}

type InstanceActionResult struct {
	Provenance Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object     ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference"`
	Node       string                 `json:"node" jsonschema:"the node of the instance"`
	Action     string                 `json:"action" jsonschema:"the instance action run"`
	Resources  []string               `json:"resources" jsonschema:"the resource ids the action applies to; empty for the whole instance"`
	StateOnly  bool                   `json:"state_only,omitempty" jsonschema:"for an unprovision, whether the resources were only recorded as unprovisioned, touched by nothing"`
	SessionID  string                 `json:"session_id" jsonschema:"the session the execution belongs to"`
	ExecID     string                 `json:"exec_id" jsonschema:"the execution the node forked for the action; pass it, with the node, to list_daemon_executions and get_node_logs"`
	// Instance is the instance as read before the action.
	Instance ObjectInstanceStatus `json:"instance_before" jsonschema:"the instance as read before the action, not its state after it: get_instance_status reads it again"`
	ExecutionOutcome
	// Tasks are the tasks a run ran, their last run read again.
	Tasks      []TaskRun `json:"tasks,omitempty" jsonschema:"for a run, each task, its last run read again in the instance status once the wait ended"`
	TasksError string    `json:"tasks_error,omitempty" jsonschema:"why the last runs of the tasks could not be read again; the run itself was accepted"`
}

type InstanceClearResult struct {
	Provenance    Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object        ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference"`
	Node          string                 `json:"node" jsonschema:"the node of the instance"`
	PreviousState string                 `json:"previous_state" jsonschema:"the failed monitor state read before the clear"`
	CurrentState  string                 `json:"current_state" jsonschema:"the monitor state read last after the clear"`
	Cleared       bool                   `json:"cleared" jsonschema:"whether the monitor state was seen to leave the failed state within 10 seconds"`
}

type daemonInstanceActionAccepted struct {
	SessionID string `json:"session_id"`
	ExecID    string `json:"exec_id"`
}

// RunInstanceAction runs one action on the instance of one exact object on
// one node, then waits a bounded time for the end of the execution the node
// forked for it.
func (s *Service) RunInstanceAction(ctx context.Context, path string, node string, action InstanceAction, options InstanceActionOptions) (InstanceActionResult, error) {
	reference, err := validateInstanceTarget(path, node, string(action), instanceActionKinds)
	if err != nil {
		return InstanceActionResult{}, err
	}
	query, err := instanceActionQuery(action, options)
	if err != nil {
		return InstanceActionResult{}, err
	}
	wait := options.Wait
	if wait == 0 {
		wait = defaultInstanceActionWait
	}
	if wait < time.Second || wait > maxInstanceActionWait {
		return InstanceActionResult{}, fmt.Errorf("wait must be between 1 and %d seconds", int(maxInstanceActionWait.Seconds()))
	}
	poster, ok := s.client.(JSONPoster)
	if !ok {
		return InstanceActionResult{}, fmt.Errorf("OpenSVC daemon client does not support POST requests")
	}
	instance, err := s.refuseInstanceAction(ctx, reference, node, action, options)
	if err != nil {
		return InstanceActionResult{}, err
	}
	var logged map[string]bool
	if action == InstanceActionRun {
		if logged, err = s.refuseTaskRun(ctx, reference, node, options.Rids); err != nil {
			return InstanceActionResult{}, err
		}
	}
	endpoint := fmt.Sprintf("/api/node/name/%s/instance/path/%s/%s/%s/action/%s", node, reference.Namespace, reference.Kind, reference.Name, action)
	var accepted daemonInstanceActionAccepted
	if err := poster.PostJSON(ctx, endpoint, query, nil, &accepted); err != nil {
		// The daemon answers 409 to a node whose configuration is older
		// than config_updated_at: a model fills it with a status time of a
		// listing, which no configuration matches.
		var problem daemonProblem
		if options.ConfigUpdatedAt != "" && errors.As(err, &problem) && problem.HTTPStatus() == http.StatusConflict {
			return InstanceActionResult{}, fmt.Errorf("%s instance %s on %s: the daemon refused config_updated_at %s, most likely not the last_modified update_object_config gave in this turn: leave config_updated_at out and call again (the daemon said: %s)", action, reference.Path, node, options.ConfigUpdatedAt, problem.ProblemDetail())
		}
		return InstanceActionResult{}, fmt.Errorf("%s instance %s on %s: %w", action, reference.Path, node, err)
	}
	if !daemonExecutionUUIDPattern.MatchString(accepted.SessionID) || !daemonExecutionUUIDPattern.MatchString(accepted.ExecID) {
		return InstanceActionResult{}, fmt.Errorf("%s instance %s on %s: daemon returned no valid session and exec ids", action, reference.Path, node)
	}
	result := InstanceActionResult{
		Object:    reference,
		Node:      node,
		Action:    string(action),
		Resources: append([]string{}, options.Rids...),
		StateOnly: options.StateOnly,
		SessionID: strings.ToLower(accepted.SessionID),
		ExecID:    strings.ToLower(accepted.ExecID),
		Instance:  instance,
	}
	outcome, err := s.waitExecution(ctx, node, result.ExecID, wait)
	if err != nil {
		return InstanceActionResult{}, err
	}
	result.ExecutionOutcome = outcome
	if action == InstanceActionRun {
		result.Tasks, err = s.readTaskRuns(ctx, reference, node, result.SessionID, options.Rids, logged, outcome.Completed)
		if err != nil {
			result.TasksError = err.Error()
		}
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

// validateInstanceTarget checks the exact object, of a kind the action
// accepts, and the exact node of the instance.
func validateInstanceTarget(path string, node string, action string, kinds []string) (ClusterObjectReference, error) {
	if hasSelectorCharacters(path) {
		return ClusterObjectReference{}, fmt.Errorf("path must be one exact OpenSVC object path, without wildcard or selector")
	}
	reference, err := validateExactObjectPath(path)
	if err != nil {
		return ClusterObjectReference{}, err
	}
	if !slices.Contains(kinds, reference.Kind) {
		return ClusterObjectReference{}, fmt.Errorf("the %s action applies to %s objects only", action, strings.Join(kinds, ", "))
	}
	if err := validateNodeTarget(node); err != nil {
		return ClusterObjectReference{}, err
	}
	return reference, nil
}

// instanceActionQuery checks the options, each on the action that takes it,
// and gives the query of the action.
func instanceActionQuery(action InstanceAction, options InstanceActionOptions) (url.Values, error) {
	switch action {
	case InstanceActionStart, InstanceActionStop, InstanceActionRestart, InstanceActionFreeze, InstanceActionUnfreeze, InstanceActionUnprovision, InstanceActionRun,
		InstanceActionStartStandby, InstanceActionShutdown, InstanceActionPGUpdate, InstanceActionPGReset, InstanceActionInfo:
	default:
		return nil, fmt.Errorf("unsupported instance action %q", action)
	}
	query := url.Values{}
	if action == InstanceActionRun {
		// A run without rid runs every task of the instance: the tasks are
		// named, one at least.
		if len(options.Rids) == 0 {
			return nil, fmt.Errorf("a run names its tasks: rids, one task#<name> at least")
		}
		for _, rid := range options.Rids {
			if !strings.HasPrefix(rid, "task#") {
				return nil, fmt.Errorf("resource %q is not a task: a run runs task#<name> resources only", rid)
			}
		}
	}
	if len(options.Env) > 0 {
		if action != InstanceActionRun {
			return nil, fmt.Errorf("env applies to the run action only")
		}
		if err := validateTaskEnv(options.Env); err != nil {
			return nil, err
		}
		query["env"] = append([]string(nil), options.Env...)
	}
	if len(options.Rids) > 0 {
		if action == InstanceActionFreeze || action == InstanceActionUnfreeze {
			return nil, fmt.Errorf("rids do not apply to the freeze and unfreeze actions")
		}
		if len(options.Rids) > maxEnableResources {
			return nil, fmt.Errorf("at most %d resource ids are accepted", maxEnableResources)
		}
		for _, rid := range options.Rids {
			if !exactResourceID.MatchString(rid) {
				return nil, fmt.Errorf("resource %q must be one exact resource id, such as fs#1", rid)
			}
		}
		query.Set("rid", strings.Join(options.Rids, ","))
	}
	if options.DisableRollback {
		if action != InstanceActionStart && action != InstanceActionRestart && action != InstanceActionStartStandby {
			return nil, fmt.Errorf("disable_rollback applies to the start, restart and startstandby actions only")
		}
		query.Set("disable_rollback", "true")
	}
	if options.InterruptSyncs {
		if action != InstanceActionStop {
			return nil, fmt.Errorf("interrupt_syncs applies to the stop action only")
		}
		query.Set("interrupt_syncs", "true")
	}
	if options.Leader {
		if action != InstanceActionUnprovision {
			return nil, fmt.Errorf("leader applies to the unprovision action only")
		}
		query.Set("leader", "true")
	}
	if options.StateOnly {
		if action != InstanceActionUnprovision {
			return nil, fmt.Errorf("state_only applies to the unprovision action only")
		}
		if options.Leader {
			return nil, fmt.Errorf("leader and state_only are exclusive: a record touches nothing, shared or not")
		}
		query.Set("state_only", "true")
	}
	if options.ConfigUpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, options.ConfigUpdatedAt); err != nil {
			return nil, fmt.Errorf("config_updated_at must be an RFC 3339 time, the last_modified update_object_config gives")
		}
		query.Set("config_updated_at", options.ConfigUpdatedAt)
	}
	return query, nil
}

// refuseInstanceAction reads the instances of the object: the one of the
// node must exist. The daemon starts an instance of a failover service while
// another runs on a peer, which would run it twice, on the same shared
// storage: a start or a restart is refused when another instance is up or
// warn. The daemon stops an instance up, then destroys its data, on an
// unprovision: it is refused when the instance is up or warn, unless it only
// records the state, which touches nothing.
func (s *Service) refuseInstanceAction(ctx context.Context, reference ClusterObjectReference, node string, action InstanceAction, options InstanceActionOptions) (ObjectInstanceStatus, error) {
	instances, err := s.ListObjectInstances(ctx, ListObjectInstancesOptions{Path: reference.Path, Limit: maxListObjectInstancesLimit})
	if err != nil {
		return ObjectInstanceStatus{}, fmt.Errorf("%s instance %s on %s: read its instances: %w", action, reference.Path, node, err)
	}
	index := slices.IndexFunc(instances.Instances, func(instance ObjectInstanceStatus) bool { return instance.Node == node })
	if index < 0 {
		return ObjectInstanceStatus{}, fmt.Errorf("%s instance %s on %s: the object has no instance on that node, or it is not visible to the caller", action, reference.Path, node)
	}
	if instance := instances.Instances[index]; action == InstanceActionUnprovision && !options.StateOnly && (instance.Availability == "up" || instance.Availability == "warn") {
		return ObjectInstanceStatus{}, fmt.Errorf("unprovision instance %s on %s: the instance is %s: stop it first, stop_instance; the unprovision destroys the data of its disk and fs resources", reference.Path, node, instance.Availability)
	}
	if action == InstanceActionShutdown {
		if err := s.refuseShutdownUnflagged(ctx, reference, node, instances.Instances[index]); err != nil {
			return ObjectInstanceStatus{}, err
		}
	}
	if action != InstanceActionStart && action != InstanceActionRestart {
		return instances.Instances[index], nil
	}
	status, err := s.GetObjectStatus(ctx, reference.Path)
	if err != nil {
		return ObjectInstanceStatus{}, fmt.Errorf("%s instance %s on %s: read its topology: %w", action, reference.Path, node, err)
	}
	if status.Topology != "failover" {
		return instances.Instances[index], nil
	}
	for _, instance := range instances.Instances {
		if instance.Node != node && (instance.Availability == "up" || instance.Availability == "warn") {
			return ObjectInstanceStatus{}, fmt.Errorf("%s instance %s on %s: the service is failover and its instance on %s is %s: it runs on one node at a time; switch_object moves it", action, reference.Path, node, instance.Node, instance.Availability)
		}
	}
	return instances.Instances[index], nil
}

// refuseShutdownUnflagged refuses a shutdown the daemon would undo at once.
// A shutdown, unlike a stop, does not flag the instance stopped: on an
// object orchestrated ha, the daemon seeing the instance down without the
// flag starts it again here, or fails over. It is refused unless the object,
// the instance or the node is frozen, as a drain freezes the node first.
func (s *Service) refuseShutdownUnflagged(ctx context.Context, reference ClusterObjectReference, node string, instance ObjectInstanceStatus) error {
	status, err := s.GetObjectStatus(ctx, reference.Path)
	if err != nil {
		return fmt.Errorf("shutdown instance %s on %s: read its orchestration: %w", reference.Path, node, err)
	}
	if status.Orchestrate != "ha" || status.Frozen == "frozen" || isSetTime(instance.FrozenAt) {
		return nil
	}
	nodeStatus, err := s.GetNodeStatus(ctx, node)
	if err != nil {
		return fmt.Errorf("shutdown instance %s on %s: read whether the node is frozen: %w", reference.Path, node, err)
	}
	if isSetTime(nodeStatus.Status.FrozenAt) {
		return nil
	}
	return fmt.Errorf("shutdown instance %s on %s: the object is orchestrated ha and neither it nor the node is frozen: a shutdown does not flag the instance stopped, and the daemon would start it again here, or fail over, at once; freeze_instance on %s first, then shutdown_instance again", reference.Path, node, node)
}

// ClearInstance lifts the failed monitor state of the instance of one exact
// object on one node, then reads it again for a bounded time.
func (s *Service) ClearInstance(ctx context.Context, path string, node string) (InstanceClearResult, error) {
	reference, err := validateInstanceTarget(path, node, "clear", clearInstanceKinds)
	if err != nil {
		return InstanceClearResult{}, err
	}
	poster, ok := s.client.(JSONPoster)
	if !ok {
		return InstanceClearResult{}, fmt.Errorf("OpenSVC daemon client does not support POST requests")
	}
	previous, err := s.getExactObjectInstance(ctx, reference.Path, node)
	if err != nil {
		return InstanceClearResult{}, fmt.Errorf("clear instance %s on %s: %w", reference.Path, node, err)
	}
	switch {
	case slices.Contains(failedMonitorStates, previous.MonitorState):
	case previous.MonitorState == "idle":
		return InstanceClearResult{}, fmt.Errorf("clear instance %s on %s: nothing to clear: its monitor state is idle", reference.Path, node)
	default:
		return InstanceClearResult{}, fmt.Errorf("clear instance %s on %s: its monitor state is %q, not a failure: an action is running or ending, and a clear would set the monitor idle under it", reference.Path, node, previous.MonitorState)
	}
	endpoint := fmt.Sprintf("/api/node/name/%s/instance/path/%s/%s/%s/clear", node, reference.Namespace, reference.Kind, reference.Name)
	// The daemon answers 200 with a null body.
	if err := poster.PostJSON(ctx, endpoint, nil, nil, nil); err != nil {
		return InstanceClearResult{}, fmt.Errorf("clear instance %s on %s: %w", reference.Path, node, err)
	}
	result := InstanceClearResult{Object: reference, Node: node, PreviousState: previous.MonitorState, CurrentState: previous.MonitorState}
	waitCtx, cancel := context.WithTimeout(ctx, clearInstanceWait)
	defer cancel()
	for attempt := 0; !result.Cleared; attempt++ {
		timer := time.NewTimer(refreshPollDelay(attempt))
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return InstanceClearResult{}, ctx.Err()
			}
			result.Provenance = s.newProvenance()
			return result, nil
		case <-timer.C:
		}
		current, err := s.getExactObjectInstance(waitCtx, reference.Path, node)
		if err != nil {
			if ctx.Err() != nil {
				return InstanceClearResult{}, ctx.Err()
			}
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				break
			}
			return InstanceClearResult{}, fmt.Errorf("clear instance %s on %s: read it again: %w", reference.Path, node, err)
		}
		result.CurrentState = current.MonitorState
		result.Cleared = current.MonitorState != previous.MonitorState
	}
	result.Provenance = s.newProvenance()
	return result, nil
}
