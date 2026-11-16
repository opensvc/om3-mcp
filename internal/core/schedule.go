package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ScheduleScopeNode     = "node"
	ScheduleScopeObject   = "object"
	ScheduleScopeInstance = "instance"

	defaultScheduleLimit       = 100
	maxScheduleLimit           = 200
	maxScheduleItems           = 10000
	maxScheduleCursorRunes     = 64
	maxScheduleIdentityRunes   = 512
	maxScheduleExpressionRunes = 4096
	maxSchedulePageRunes       = 128 << 10
)

type ListSchedulesOptions struct {
	Scope  string
	Path   string
	Node   string
	Limit  int
	Cursor string
}

type ScheduleList struct {
	Provenance Provenance              `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Scope      string                  `json:"scope" jsonschema:"exact requested schedule scope: node, object, or instance"`
	Object     *ClusterObjectReference `json:"object,omitempty" jsonschema:"exact object reference for object and instance scopes; omitted for node scope"`
	Node       string                  `json:"node,omitempty" jsonschema:"exact node for node and instance scopes; omitted for object scope"`
	Total      int                     `json:"total" jsonschema:"number of schedule entries returned by OpenSVC before MCP pagination"`
	Count      int                     `json:"count" jsonschema:"number of schedule entries returned in this page"`
	Schedules  []ScheduleEntry         `json:"schedules" jsonschema:"schedule entries sorted by object path, node, key, action, and schedule expression"`
	NextCursor string                  `json:"next_cursor,omitempty" jsonschema:"opaque cursor to pass unchanged for the next page with the same scope, object, and node"`
	Truncated  bool                    `json:"truncated" jsonschema:"whether schedule entries remain after this page"`
}

type ScheduleEntry struct {
	Object             string  `json:"object,omitempty" jsonschema:"canonical object path reported by OpenSVC; empty for a node-level schedule"`
	Node               string  `json:"node" jsonschema:"node owning this schedule entry"`
	Key                string  `json:"key" jsonschema:"configuration key that defines this schedule"`
	Action             string  `json:"action" jsonschema:"OpenSVC action launched by this schedule"`
	Schedule           string  `json:"schedule" jsonschema:"raw OpenSVC schedule expression without MCP interpretation; empty when the task is not scheduled"`
	Scheduled          bool    `json:"scheduled" jsonschema:"false when the task has no schedule expression: OpenSVC does not run it on a schedule, a normal state, not an error"`
	LastRunAt          *string `json:"last_run_at" jsonschema:"last execution timestamp reported by OpenSVC, or null when the action has never run"`
	NextRunAt          *string `json:"next_run_at" jsonschema:"next execution timestamp reported by OpenSVC, or null when no next run is scheduled"`
	MaxParallel        int     `json:"max_parallel" jsonschema:"maximum parallel executions reported by OpenSVC"`
	Require            string  `json:"require" jsonschema:"raw OpenSVC resource requirement expression; empty when none is configured"`
	RequireCollector   bool    `json:"require_collector" jsonschema:"whether OpenSVC requires collector availability before running the action"`
	RequireProvisioned bool    `json:"require_provisioned" jsonschema:"whether OpenSVC requires the object to be provisioned before running the action"`
}

type daemonScheduleList struct {
	Kind  string               `json:"kind"`
	Items []daemonScheduleItem `json:"items"`
}

type daemonScheduleItem struct {
	Kind string `json:"kind"`
	Meta struct {
		Node   string `json:"node"`
		Object string `json:"object"`
	} `json:"meta"`
	Data struct {
		Action             string  `json:"action"`
		Key                string  `json:"key"`
		LastRunAt          *string `json:"last_run_at"`
		MaxParallel        int     `json:"max_parallel"`
		NextRunAt          *string `json:"next_run_at"`
		Require            string  `json:"require"`
		RequireCollector   bool    `json:"require_collector"`
		RequireProvisioned bool    `json:"require_provisioned"`
		Schedule           string  `json:"schedule"`
	} `json:"data"`
}

type scheduleTarget struct {
	scope     string
	node      string
	object    ClusterObjectReference
	hasObject bool
}

type scheduleRecord struct {
	entry  ScheduleEntry
	cursor string
}

func (s *Service) ListSchedules(ctx context.Context, options ListSchedulesOptions) (ScheduleList, error) {
	target, limit, cursor, err := validateScheduleOptions(options)
	if err != nil {
		return ScheduleList{}, err
	}

	var response daemonScheduleList
	if err := s.client.GetJSON(ctx, scheduleEndpoint(target), url.Values{}, &response); err != nil {
		return ScheduleList{}, fmt.Errorf("list schedules: %w", err)
	}
	if response.Kind != "ScheduleList" {
		return ScheduleList{}, fmt.Errorf("list schedules: unexpected response kind %q", response.Kind)
	}
	if response.Items == nil {
		return ScheduleList{}, fmt.Errorf("list schedules: response items must be an array")
	}
	if len(response.Items) > maxScheduleItems {
		return ScheduleList{}, fmt.Errorf("list schedules: response contains %d items, limit is %d", len(response.Items), maxScheduleItems)
	}

	records := make([]scheduleRecord, 0, len(response.Items))
	for index, raw := range response.Items {
		entry, err := projectScheduleEntry(raw, target)
		if err != nil {
			return ScheduleList{}, fmt.Errorf("list schedules: item %d: %w", index, err)
		}
		records = append(records, scheduleRecord{entry: entry})
	}

	sort.SliceStable(records, func(i, j int) bool {
		return compareScheduleEntries(records[i].entry, records[j].entry) < 0
	})
	occurrences := make(map[string]int, len(records))
	for index := range records {
		fingerprint := scheduleRecordFingerprint(target, records[index].entry)
		occurrence := occurrences[fingerprint]
		records[index].cursor = fingerprint + "." + strconv.Itoa(occurrence)
		occurrences[fingerprint] = occurrence + 1
	}

	start := 0
	if cursor != "" {
		found := false
		for index := range records {
			if records[index].cursor == cursor {
				start = index + 1
				found = true
				break
			}
		}
		if !found {
			return ScheduleList{}, fmt.Errorf("schedule cursor is no longer present for this scope")
		}
	}

	items := make([]ScheduleEntry, 0, min(limit, len(records)-start))
	pageRunes := 0
	end := start
	for end < len(records) && len(items) < limit {
		itemRunes := scheduleEntryRunes(records[end].entry)
		if len(items) > 0 && pageRunes+itemRunes > maxSchedulePageRunes {
			break
		}
		items = append(items, records[end].entry)
		pageRunes += itemRunes
		end++
	}

	result := ScheduleList{
		Provenance: s.newProvenance(),
		Scope:      target.scope,
		Node:       target.node,
		Total:      len(records),
		Count:      len(items),
		Schedules:  items,
		Truncated:  end < len(records),
	}
	if target.hasObject {
		object := target.object
		result.Object = &object
	}
	if target.scope == ScheduleScopeObject {
		result.Node = ""
	}
	if result.Truncated {
		result.NextCursor = records[end-1].cursor
	}
	return result, nil
}

func validateScheduleOptions(options ListSchedulesOptions) (scheduleTarget, int, string, error) {
	target := scheduleTarget{scope: options.Scope, node: options.Node}
	switch target.scope {
	case ScheduleScopeNode:
		if options.Path != "" {
			return scheduleTarget{}, 0, "", fmt.Errorf("schedule path must be empty for node scope")
		}
		if err := validateNodeTarget(target.node); err != nil {
			return scheduleTarget{}, 0, "", err
		}
	case ScheduleScopeObject:
		if target.node != "" {
			return scheduleTarget{}, 0, "", fmt.Errorf("schedule node must be empty for object scope")
		}
		reference, err := validateExactObjectPath(options.Path)
		if err != nil {
			return scheduleTarget{}, 0, "", err
		}
		target.object, target.hasObject = reference, true
	case ScheduleScopeInstance:
		if err := validateNodeTarget(target.node); err != nil {
			return scheduleTarget{}, 0, "", err
		}
		reference, err := validateExactObjectPath(options.Path)
		if err != nil {
			return scheduleTarget{}, 0, "", err
		}
		target.object, target.hasObject = reference, true
	default:
		return scheduleTarget{}, 0, "", fmt.Errorf("schedule scope must be exactly node, object, or instance")
	}

	limit := options.Limit
	if limit == 0 {
		limit = defaultScheduleLimit
	}
	if limit < 1 || limit > maxScheduleLimit {
		return scheduleTarget{}, 0, "", fmt.Errorf("schedule limit must be between 1 and %d", maxScheduleLimit)
	}
	if err := validateScheduleCursor(options.Cursor); err != nil {
		return scheduleTarget{}, 0, "", err
	}
	return target, limit, options.Cursor, nil
}

func scheduleEndpoint(target scheduleTarget) string {
	switch target.scope {
	case ScheduleScopeNode:
		return fmt.Sprintf("/api/node/name/%s/schedule", target.node)
	case ScheduleScopeObject:
		return fmt.Sprintf("/api/object/path/%s/%s/%s/schedule", target.object.Namespace, target.object.Kind, target.object.Name)
	default:
		return fmt.Sprintf("/api/node/name/%s/instance/path/%s/%s/%s/schedule", target.node, target.object.Namespace, target.object.Kind, target.object.Name)
	}
}

func projectScheduleEntry(raw daemonScheduleItem, target scheduleTarget) (ScheduleEntry, error) {
	if raw.Kind != "ScheduleItem" {
		return ScheduleEntry{}, fmt.Errorf("unexpected kind %q", raw.Kind)
	}
	if !validExactNodeName(raw.Meta.Node) {
		return ScheduleEntry{}, fmt.Errorf("invalid node %q", raw.Meta.Node)
	}
	if target.node != "" && raw.Meta.Node != target.node {
		return ScheduleEntry{}, fmt.Errorf("unexpected node %q", raw.Meta.Node)
	}
	if raw.Meta.Object != "" {
		reference, err := validateExactObjectPath(raw.Meta.Object)
		if err != nil || reference.Path != raw.Meta.Object {
			return ScheduleEntry{}, fmt.Errorf("invalid object path %q", raw.Meta.Object)
		}
	}
	if target.hasObject && raw.Meta.Object != target.object.Path {
		return ScheduleEntry{}, fmt.Errorf("unexpected object path %q", raw.Meta.Object)
	}
	for name, value := range map[string]string{
		"action": raw.Data.Action,
		"key":    raw.Data.Key,
	} {
		if err := validateScheduleString(name, value, maxScheduleIdentityRunes, false); err != nil {
			return ScheduleEntry{}, err
		}
	}
	// A task with no schedule expression is not scheduled, a normal state, as
	// the pushbackup of a store set to none: on 2026-11-16 a node of dev5
	// answered one, and the whole list was refused. Oversized or with control
	// characters, the expression still is.
	if err := validateScheduleString("schedule", raw.Data.Schedule, maxScheduleExpressionRunes, true); err != nil {
		return ScheduleEntry{}, err
	}
	if err := validateScheduleString("require", raw.Data.Require, maxScheduleExpressionRunes, true); err != nil {
		return ScheduleEntry{}, err
	}
	if raw.Data.MaxParallel < 0 {
		return ScheduleEntry{}, fmt.Errorf("max_parallel must not be negative")
	}
	if err := validateScheduleTimestamp("last_run_at", raw.Data.LastRunAt); err != nil {
		return ScheduleEntry{}, err
	}
	if err := validateScheduleTimestamp("next_run_at", raw.Data.NextRunAt); err != nil {
		return ScheduleEntry{}, err
	}
	return ScheduleEntry{
		Object:             raw.Meta.Object,
		Node:               raw.Meta.Node,
		Key:                raw.Data.Key,
		Action:             raw.Data.Action,
		Schedule:           raw.Data.Schedule,
		Scheduled:          raw.Data.Schedule != "",
		LastRunAt:          raw.Data.LastRunAt,
		NextRunAt:          raw.Data.NextRunAt,
		MaxParallel:        raw.Data.MaxParallel,
		Require:            raw.Data.Require,
		RequireCollector:   raw.Data.RequireCollector,
		RequireProvisioned: raw.Data.RequireProvisioned,
	}, nil
}

func validateScheduleString(name, value string, maxRunes int, allowEmpty bool) error {
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s is empty", name)
	}
	if value != strings.TrimSpace(value) || len([]rune(value)) > maxRunes || containsControl(value) {
		return fmt.Errorf("%s is oversized, contains control characters, or has surrounding whitespace", name)
	}
	return nil
}

func validateScheduleTimestamp(name string, value *string) error {
	if value == nil {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, *value); err != nil {
		return fmt.Errorf("%s is not an RFC3339 timestamp", name)
	}
	return nil
}

func compareScheduleEntries(left, right ScheduleEntry) int {
	leftFields := [...]string{left.Object, left.Node, left.Key, left.Action, left.Schedule}
	rightFields := [...]string{right.Object, right.Node, right.Key, right.Action, right.Schedule}
	for index := range leftFields {
		if comparison := strings.Compare(leftFields[index], rightFields[index]); comparison != 0 {
			return comparison
		}
	}
	return 0
}

func scheduleRecordFingerprint(target scheduleTarget, entry ScheduleEntry) string {
	path := ""
	if target.hasObject {
		path = target.object.Path
	}
	payload := strings.Join([]string{target.scope, target.node, path, entry.Object, entry.Node, entry.Key, entry.Action, entry.Schedule}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func validateScheduleCursor(cursor string) error {
	if cursor == "" {
		return nil
	}
	if len([]rune(cursor)) > maxScheduleCursorRunes || containsControl(cursor) {
		return fmt.Errorf("schedule cursor exceeds %d characters or contains control characters", maxScheduleCursorRunes)
	}
	fingerprint, occurrence, ok := strings.Cut(cursor, ".")
	decoded, err := base64.RawURLEncoding.DecodeString(fingerprint)
	if !ok || err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid schedule cursor")
	}
	index, err := strconv.Atoi(occurrence)
	if err != nil || index < 0 || strconv.Itoa(index) != occurrence {
		return fmt.Errorf("invalid schedule cursor")
	}
	return nil
}

func scheduleEntryRunes(entry ScheduleEntry) int {
	count := len([]rune(entry.Object)) + len([]rune(entry.Node)) + len([]rune(entry.Key)) +
		len([]rune(entry.Action)) + len([]rune(entry.Schedule)) + len([]rune(entry.Require))
	if entry.LastRunAt != nil {
		count += len([]rune(*entry.LastRunAt))
	}
	if entry.NextRunAt != nil {
		count += len([]rune(*entry.NextRunAt))
	}
	return count
}
