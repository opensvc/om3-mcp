package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	defaultDaemonExecutionLimit     = 50
	maxDaemonExecutionLimit         = 100
	maxDaemonExecutionCursorLength  = 1024
	maxDaemonExecutionFilterValues  = 16
	maxDaemonExecutionFilterLength  = 64
	maxDaemonExecutionIDLength      = 128
	maxDaemonExecutionNodeLength    = 255
	maxDaemonExecutionOriginLength  = 128
	maxDaemonExecutionRIDLength     = 255
	maxDaemonExecutionPathLength    = 1024
	maxDaemonExecutionCommandRunes  = 2048
	maxDaemonExecutionTitleRunes    = 512
	maxDaemonExecutionErrorRunes    = 4096
	maxDaemonExecutionPageTextRunes = 128 << 10
)

var daemonExecutionUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type ListDaemonExecutionsOptions struct {
	Node            string
	States          []string
	Origins         []string
	SessionID       string
	OrchestrationID string
	ExecID          string
	ObjectPath      string
	RID             string
	Limit           int
	Cursor          string
}

type DaemonExecutionList struct {
	Provenance Provenance        `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node       string            `json:"node" jsonschema:"the exact requested OpenSVC node name, reported by every execution"`
	Total      int               `json:"total" jsonschema:"number of daemon executions matching the requested filters before MCP pagination"`
	Count      int               `json:"count" jsonschema:"number of daemon executions returned in this page"`
	Executions []DaemonExecution `json:"executions" jsonschema:"daemon execution records sorted by start time descending and then execution id"`
	NextCursor string            `json:"next_cursor,omitempty" jsonschema:"opaque cursor to pass unchanged for the next page"`
	Truncated  bool              `json:"truncated" jsonschema:"whether matching execution records remain after this page"`
}

type DaemonExecution struct {
	SessionID        string  `json:"session_id" jsonschema:"session identifier reported by OpenSVC; several executions can share it"`
	ExecID           string  `json:"exec_id" jsonschema:"identifier of this exact execution on one node and object"`
	OrchestrationID  *string `json:"orchestration_id" jsonschema:"orchestration identifier reported by OpenSVC, or null when this execution is not an orchestration step"`
	Node             string  `json:"node" jsonschema:"node that ran this execution"`
	Path             *string `json:"path" jsonschema:"canonical object path acted on, or null for a node action"`
	Origin           string  `json:"origin" jsonschema:"exact submitter reported by OpenSVC, such as api, imon, nmon, or scheduler"`
	RID              *string `json:"rid" jsonschema:"resource identifier reported by OpenSVC, or null when this execution is not resource-specific"`
	Title            *string `json:"title" jsonschema:"bounded execution title reported by OpenSVC, or null when absent"`
	TitleTruncated   bool    `json:"title_truncated" jsonschema:"whether the execution title exceeded the 512-rune limit"`
	Command          string  `json:"command" jsonschema:"bounded command reported by OpenSVC; this root-only field can contain operational arguments"`
	CommandTruncated bool    `json:"command_truncated" jsonschema:"whether the command exceeded the 2048-rune limit"`
	State            string  `json:"state" jsonschema:"exact execution state reported by OpenSVC without MCP reclassification"`
	Error            *string `json:"error" jsonschema:"bounded execution error reported by OpenSVC, or null when absent"`
	ErrorTruncated   bool    `json:"error_truncated" jsonschema:"whether the execution error exceeded the 4096-rune limit"`
	ExitCode         *int    `json:"exit_code" jsonschema:"process exit code reported by OpenSVC, or null while running; -1 means the process never ran"`
	StartedAt        string  `json:"started_at" jsonschema:"execution start timestamp reported by OpenSVC"`
	EndedAt          *string `json:"ended_at" jsonschema:"execution end timestamp reported by OpenSVC, or null while running"`
	PID              *int    `json:"pid" jsonschema:"current process identifier reported by OpenSVC, or null when absent or ended"`
}

type daemonExecutionListResponse struct {
	Kind  string                    `json:"kind"`
	Items []daemonExecutionResponse `json:"items"`
}

type daemonExecutionResponse struct {
	SessionID       string  `json:"session_id"`
	ExecID          string  `json:"exec_id"`
	OrchestrationID *string `json:"orchestration_id"`
	Node            string  `json:"node"`
	Path            *string `json:"path"`
	Origin          string  `json:"origin"`
	RID             *string `json:"rid"`
	Title           *string `json:"title"`
	Command         string  `json:"command"`
	State           string  `json:"state"`
	Error           *string `json:"error"`
	ExitCode        *int    `json:"exit_code"`
	StartedAt       string  `json:"started_at"`
	EndedAt         *string `json:"ended_at"`
	PID             *int    `json:"pid"`
}

type parsedDaemonExecution struct {
	raw       daemonExecutionResponse
	startedAt time.Time
}

func (s *Service) ListDaemonExecutions(ctx context.Context, options ListDaemonExecutionsOptions) (DaemonExecutionList, error) {
	targetNode, query, cursor, limit, err := validateDaemonExecutionOptions(options)
	if err != nil {
		return DaemonExecutionList{}, err
	}

	var response daemonExecutionListResponse
	endpoint := fmt.Sprintf("/api/node/name/%s/daemon/exec", targetNode)
	if err := s.client.GetJSON(ctx, endpoint, query, &response); err != nil {
		return DaemonExecutionList{}, fmt.Errorf("list daemon executions: %w", err)
	}
	if response.Kind != "ExecList" {
		return DaemonExecutionList{}, shapeErrorf("list daemon executions: unexpected response kind %q", response.Kind)
	}

	parsed := make([]parsedDaemonExecution, 0, len(response.Items))
	for index, item := range response.Items {
		value, err := validateDaemonExecutionResponse(item)
		if err != nil {
			return DaemonExecutionList{}, fmt.Errorf("list daemon executions: item %d: %w", index, err)
		}
		if item.Node != targetNode {
			return DaemonExecutionList{}, fmt.Errorf("list daemon executions: item %d reports unexpected node %q", index, item.Node)
		}
		parsed = append(parsed, value)
	}
	sort.Slice(parsed, func(i, j int) bool {
		if parsed[i].startedAt.Equal(parsed[j].startedAt) {
			return parsed[i].raw.ExecID < parsed[j].raw.ExecID
		}
		return parsed[i].startedAt.After(parsed[j].startedAt)
	})

	start := 0
	if cursor != "" {
		start = -1
		for index := range parsed {
			if parsed[index].raw.ExecID == cursor {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return DaemonExecutionList{}, fmt.Errorf("daemon execution cursor is no longer present in the filtered result")
		}
	}

	items := make([]DaemonExecution, 0, min(limit, len(parsed)-start))
	textRunes := 0
	end := start
	for end < len(parsed) && len(items) < limit {
		item, itemTextRunes := daemonExecutionOutput(parsed[end].raw)
		if len(items) > 0 && textRunes+itemTextRunes > maxDaemonExecutionPageTextRunes {
			break
		}
		items = append(items, item)
		textRunes += itemTextRunes
		end++
	}
	if items == nil {
		items = []DaemonExecution{}
	}
	result := DaemonExecutionList{
		Provenance: s.newProvenance(),
		Node:       targetNode,
		Total:      len(parsed),
		Count:      len(items),
		Executions: items,
		Truncated:  end < len(parsed),
	}
	if result.Truncated {
		result.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(items[len(items)-1].ExecID))
	}
	return result, nil
}

func validateDaemonExecutionOptions(options ListDaemonExecutionsOptions) (string, url.Values, string, int, error) {
	validated := options
	if err := validateNodeTarget(validated.Node); err != nil {
		return "", nil, "", 0, err
	}
	var err error
	if validated.States, err = normalizeDaemonExecutionFilter(options.States, "state"); err != nil {
		return "", nil, "", 0, err
	}
	if validated.Origins, err = normalizeDaemonExecutionFilter(options.Origins, "origin"); err != nil {
		return "", nil, "", 0, err
	}
	if validated.SessionID, err = validateDaemonExecutionUUID(options.SessionID, "session id"); err != nil {
		return "", nil, "", 0, err
	}
	if validated.OrchestrationID, err = validateDaemonExecutionUUID(options.OrchestrationID, "orchestration id"); err != nil {
		return "", nil, "", 0, err
	}
	if validated.ExecID, err = validateDaemonExecutionUUID(options.ExecID, "execution id"); err != nil {
		return "", nil, "", 0, err
	}
	validated.ObjectPath = strings.TrimSpace(options.ObjectPath)
	if validated.ObjectPath != "" {
		if strings.ContainsAny(validated.ObjectPath, "*?[]") {
			return "", nil, "", 0, fmt.Errorf("daemon execution object path must identify one exact object without selector characters")
		}
		if _, err := validateExactObjectPath(validated.ObjectPath); err != nil {
			return "", nil, "", 0, fmt.Errorf("daemon execution object path: %w", err)
		}
	}
	validated.RID = strings.TrimSpace(options.RID)
	if len(validated.RID) > maxDaemonExecutionRIDLength || strings.ContainsAny(validated.RID, "\r\n\x00") {
		return "", nil, "", 0, fmt.Errorf("daemon execution resource selector exceeds %d characters or contains control characters", maxDaemonExecutionRIDLength)
	}

	limit := options.Limit
	if limit == 0 {
		limit = defaultDaemonExecutionLimit
	}
	if limit < 1 || limit > maxDaemonExecutionLimit {
		return "", nil, "", 0, fmt.Errorf("daemon execution limit must be between 1 and %d", maxDaemonExecutionLimit)
	}
	cursor, err := decodeDaemonExecutionCursor(options.Cursor)
	if err != nil {
		return "", nil, "", 0, err
	}

	targetNode := validated.Node
	query := make(url.Values)
	for _, state := range validated.States {
		query.Add("state", state)
	}
	for _, origin := range validated.Origins {
		query.Add("origin", origin)
	}
	if validated.SessionID != "" {
		query.Set("session_id", validated.SessionID)
	}
	if validated.OrchestrationID != "" {
		query.Set("orchestration_id", validated.OrchestrationID)
	}
	if validated.ExecID != "" {
		query.Set("exec_id", validated.ExecID)
	}
	if validated.ObjectPath != "" {
		query.Set("selector", validated.ObjectPath)
	}
	if validated.RID != "" {
		query.Set("rid", validated.RID)
	}
	return targetNode, query, cursor, limit, nil
}

func normalizeDaemonExecutionFilter(values []string, name string) ([]string, error) {
	if len(values) > maxDaemonExecutionFilterValues {
		return nil, fmt.Errorf("daemon execution %s filter accepts at most %d values", name, maxDaemonExecutionFilterValues)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || len(value) > maxDaemonExecutionFilterLength || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("daemon execution %s filter values must contain 1 to %d non-control characters", name, maxDaemonExecutionFilterLength)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func validateDaemonExecutionUUID(raw string, name string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if !daemonExecutionUUIDPattern.MatchString(value) {
		return "", fmt.Errorf("daemon execution %s must be a canonical UUID", name)
	}
	return value, nil
}

func decodeDaemonExecutionCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > maxDaemonExecutionCursorLength {
		return "", fmt.Errorf("daemon execution cursor exceeds %d characters", maxDaemonExecutionCursorLength)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) == 0 || len(decoded) > maxDaemonExecutionIDLength || !daemonExecutionUUIDPattern.Match(decoded) {
		return "", fmt.Errorf("invalid daemon execution cursor")
	}
	return string(decoded), nil
}

func validateDaemonExecutionResponse(item daemonExecutionResponse) (parsedDaemonExecution, error) {
	for name, value := range map[string]string{
		"session_id": item.SessionID,
		"exec_id":    item.ExecID,
		"node":       item.Node,
		"origin":     item.Origin,
		"command":    item.Command,
		"state":      item.State,
		"started_at": item.StartedAt,
	} {
		if value == "" {
			return parsedDaemonExecution{}, fmt.Errorf("required field %s is empty", name)
		}
	}
	if !daemonExecutionUUIDPattern.MatchString(item.SessionID) || !daemonExecutionUUIDPattern.MatchString(item.ExecID) ||
		(item.OrchestrationID != nil && !daemonExecutionUUIDPattern.MatchString(*item.OrchestrationID)) {
		return parsedDaemonExecution{}, fmt.Errorf("an execution identifier is not a canonical UUID")
	}
	if len(item.Node) > maxDaemonExecutionNodeLength || len(item.Origin) > maxDaemonExecutionOriginLength {
		return parsedDaemonExecution{}, fmt.Errorf("node or origin exceeds its output bound")
	}
	if len(item.State) > maxDaemonExecutionFilterLength {
		return parsedDaemonExecution{}, fmt.Errorf("execution state exceeds %d characters", maxDaemonExecutionFilterLength)
	}
	if (item.Path != nil && len(*item.Path) > maxDaemonExecutionPathLength) || (item.RID != nil && len(*item.RID) > maxDaemonExecutionRIDLength) {
		return parsedDaemonExecution{}, fmt.Errorf("object path or resource identifier exceeds its output bound")
	}
	startedAt, err := time.Parse(time.RFC3339Nano, item.StartedAt)
	if err != nil {
		return parsedDaemonExecution{}, fmt.Errorf("invalid started_at %q", item.StartedAt)
	}
	if item.EndedAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *item.EndedAt); err != nil {
			return parsedDaemonExecution{}, fmt.Errorf("invalid ended_at %q", *item.EndedAt)
		}
	}
	return parsedDaemonExecution{raw: item, startedAt: startedAt}, nil
}

func daemonExecutionOutput(raw daemonExecutionResponse) (DaemonExecution, int) {
	command, commandTruncated := boundedRunes(raw.Command, maxDaemonExecutionCommandRunes)
	result := DaemonExecution{
		SessionID: raw.SessionID, ExecID: raw.ExecID, OrchestrationID: raw.OrchestrationID,
		Node: raw.Node, Path: raw.Path, Origin: raw.Origin, RID: raw.RID,
		Command: command, CommandTruncated: commandTruncated, State: raw.State,
		ExitCode: raw.ExitCode, StartedAt: raw.StartedAt, EndedAt: raw.EndedAt, PID: raw.PID,
	}
	textRunes := daemonExecutionStringRunes(result)
	if raw.Title != nil {
		value, truncated := boundedRunes(*raw.Title, maxDaemonExecutionTitleRunes)
		result.Title = &value
		result.TitleTruncated = truncated
		textRunes += len([]rune(value))
	}
	if raw.Error != nil {
		value, truncated := boundedRunes(*raw.Error, maxDaemonExecutionErrorRunes)
		result.Error = &value
		result.ErrorTruncated = truncated
		textRunes += len([]rune(value))
	}
	return result, textRunes
}

func daemonExecutionStringRunes(item DaemonExecution) int {
	count := len([]rune(item.SessionID)) + len([]rune(item.ExecID)) + len([]rune(item.Node)) +
		len([]rune(item.Origin)) + len([]rune(item.Command)) + len([]rune(item.State)) +
		len([]rune(item.StartedAt))
	for _, value := range []*string{item.OrchestrationID, item.Path, item.RID, item.EndedAt} {
		if value != nil {
			count += len([]rune(*value))
		}
	}
	return count
}
