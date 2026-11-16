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
	defaultDaemonOrchestrationLimit     = 50
	maxDaemonOrchestrationLimit         = 100
	maxDaemonOrchestrationCursorLength  = 1024
	maxDaemonOrchestrationFilterValues  = 16
	maxDaemonOrchestrationStateLength   = 64
	maxDaemonOrchestrationIDLength      = 128
	maxDaemonOrchestrationNodeLength    = 255
	maxDaemonOrchestrationPathLength    = 512
	maxDaemonOrchestrationExpectRunes   = 512
	maxDaemonOrchestrationErrorRunes    = 4096
	maxDaemonOrchestrationPageTextRunes = 128 << 10
)

var daemonOrchestrationUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type ListDaemonOrchestrationsOptions struct {
	Node       string
	States     []string
	ObjectPath string
	Limit      int
	Cursor     string
}

type DaemonOrchestrationList struct {
	Provenance     Provenance            `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node           string                `json:"node" jsonschema:"the exact requested OpenSVC node name whose daemon reported the orchestrations"`
	Total          int                   `json:"total" jsonschema:"number of daemon orchestrations matching the requested filters before MCP pagination"`
	Count          int                   `json:"count" jsonschema:"number of daemon orchestrations returned in this page"`
	Orchestrations []DaemonOrchestration `json:"orchestrations" jsonschema:"daemon orchestration records sorted by start time descending and then orchestration id"`
	NextCursor     string                `json:"next_cursor,omitempty" jsonschema:"opaque cursor to pass unchanged for the next page"`
	Truncated      bool                  `json:"truncated" jsonschema:"whether matching orchestration records remain after this page"`
}

type DaemonOrchestration struct {
	OrchestrationID string  `json:"orchestration_id" jsonschema:"identifier handed to the submitter of this orchestration"`
	Node            string  `json:"node" jsonschema:"node that accepted the orchestration, or an empty string when this daemon learned it only from participating monitors"`
	Path            *string `json:"path" jsonschema:"canonical object path acted on, or null for a node orchestration"`
	Expect          *string `json:"expect" jsonschema:"bounded target state requested by the orchestration, or null when absent"`
	ExpectTruncated bool    `json:"expect_truncated" jsonschema:"whether the target state exceeded the 512-rune limit"`
	State           string  `json:"state" jsonschema:"exact orchestration state reported by OpenSVC without MCP reclassification"`
	Error           *string `json:"error" jsonschema:"bounded orchestration error reported by OpenSVC, or null when absent"`
	ErrorTruncated  bool    `json:"error_truncated" jsonschema:"whether the orchestration error exceeded the 4096-rune limit"`
	StartedAt       string  `json:"started_at" jsonschema:"orchestration start timestamp reported by OpenSVC"`
	EndedAt         *string `json:"ended_at" jsonschema:"orchestration end timestamp reported by OpenSVC, or null while running"`
}

type daemonOrchestrationListResponse struct {
	Kind  string                        `json:"kind"`
	Items []daemonOrchestrationResponse `json:"items"`
}

type daemonOrchestrationResponse struct {
	OrchestrationID string  `json:"orchestration_id"`
	Node            string  `json:"node"`
	Path            *string `json:"path"`
	Expect          *string `json:"expect"`
	State           string  `json:"state"`
	Error           *string `json:"error"`
	StartedAt       string  `json:"started_at"`
	EndedAt         *string `json:"ended_at"`
}

type parsedDaemonOrchestration struct {
	raw       daemonOrchestrationResponse
	startedAt time.Time
}

func (s *Service) ListDaemonOrchestrations(ctx context.Context, options ListDaemonOrchestrationsOptions) (DaemonOrchestrationList, error) {
	targetNode, query, cursor, limit, err := validateDaemonOrchestrationOptions(options)
	if err != nil {
		return DaemonOrchestrationList{}, err
	}

	var response daemonOrchestrationListResponse
	endpoint := fmt.Sprintf("/api/node/name/%s/daemon/orchestration", targetNode)
	if err := s.client.GetJSON(ctx, endpoint, query, &response); err != nil {
		return DaemonOrchestrationList{}, fmt.Errorf("list daemon orchestrations: %w", err)
	}
	if response.Kind != "OrchestrationList" {
		return DaemonOrchestrationList{}, shapeErrorf("list daemon orchestrations: unexpected response kind %q", response.Kind)
	}

	parsed := make([]parsedDaemonOrchestration, 0, len(response.Items))
	for index, item := range response.Items {
		value, err := validateDaemonOrchestrationResponse(item)
		if err != nil {
			return DaemonOrchestrationList{}, fmt.Errorf("list daemon orchestrations: item %d: %w", index, err)
		}
		parsed = append(parsed, value)
	}
	sort.Slice(parsed, func(i, j int) bool {
		if parsed[i].startedAt.Equal(parsed[j].startedAt) {
			return parsed[i].raw.OrchestrationID < parsed[j].raw.OrchestrationID
		}
		return parsed[i].startedAt.After(parsed[j].startedAt)
	})

	start := 0
	if cursor != "" {
		start = -1
		for index := range parsed {
			if parsed[index].raw.OrchestrationID == cursor {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return DaemonOrchestrationList{}, fmt.Errorf("daemon orchestration cursor is no longer present in the filtered result")
		}
	}

	items := make([]DaemonOrchestration, 0, min(limit, len(parsed)-start))
	textRunes := 0
	end := start
	for end < len(parsed) && len(items) < limit {
		item, itemTextRunes := daemonOrchestrationOutput(parsed[end].raw)
		if len(items) > 0 && textRunes+itemTextRunes > maxDaemonOrchestrationPageTextRunes {
			break
		}
		items = append(items, item)
		textRunes += itemTextRunes
		end++
	}
	if items == nil {
		items = []DaemonOrchestration{}
	}
	result := DaemonOrchestrationList{
		Provenance:     s.newProvenance(),
		Node:           targetNode,
		Total:          len(parsed),
		Count:          len(items),
		Orchestrations: items,
		Truncated:      end < len(parsed),
	}
	if result.Truncated {
		result.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(items[len(items)-1].OrchestrationID))
	}
	return result, nil
}

func validateDaemonOrchestrationOptions(options ListDaemonOrchestrationsOptions) (string, url.Values, string, int, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return "", nil, "", 0, err
	}
	states, err := normalizeDaemonOrchestrationStates(options.States)
	if err != nil {
		return "", nil, "", 0, err
	}
	objectPath := strings.TrimSpace(options.ObjectPath)
	if objectPath != "" {
		if strings.ContainsAny(objectPath, "*?[]") {
			return "", nil, "", 0, fmt.Errorf("daemon orchestration object path must identify one exact object without selector characters")
		}
		if _, err := validateExactObjectPath(objectPath); err != nil {
			return "", nil, "", 0, fmt.Errorf("daemon orchestration object path: %w", err)
		}
	}

	limit := options.Limit
	if limit == 0 {
		limit = defaultDaemonOrchestrationLimit
	}
	if limit < 1 || limit > maxDaemonOrchestrationLimit {
		return "", nil, "", 0, fmt.Errorf("daemon orchestration limit must be between 1 and %d", maxDaemonOrchestrationLimit)
	}
	cursor, err := decodeDaemonOrchestrationCursor(options.Cursor)
	if err != nil {
		return "", nil, "", 0, err
	}

	query := make(url.Values)
	for _, state := range states {
		query.Add("state", state)
	}
	if objectPath != "" {
		query.Set("selector", objectPath)
	}
	return node, query, cursor, limit, nil
}

func normalizeDaemonOrchestrationStates(values []string) ([]string, error) {
	if len(values) > maxDaemonOrchestrationFilterValues {
		return nil, fmt.Errorf("daemon orchestration state filter accepts at most %d values", maxDaemonOrchestrationFilterValues)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || len(value) > maxDaemonOrchestrationStateLength || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("daemon orchestration state filter values must contain 1 to %d non-control characters", maxDaemonOrchestrationStateLength)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func decodeDaemonOrchestrationCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > maxDaemonOrchestrationCursorLength {
		return "", fmt.Errorf("daemon orchestration cursor exceeds %d characters", maxDaemonOrchestrationCursorLength)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(decoded) == 0 || len(decoded) > maxDaemonOrchestrationIDLength || !daemonOrchestrationUUIDPattern.Match(decoded) {
		return "", fmt.Errorf("invalid daemon orchestration cursor")
	}
	return string(decoded), nil
}

func validateDaemonOrchestrationResponse(item daemonOrchestrationResponse) (parsedDaemonOrchestration, error) {
	for name, value := range map[string]string{
		"orchestration_id": item.OrchestrationID,
		"state":            item.State,
		"started_at":       item.StartedAt,
	} {
		if value == "" {
			return parsedDaemonOrchestration{}, fmt.Errorf("required field %s is empty", name)
		}
	}
	if !daemonOrchestrationUUIDPattern.MatchString(item.OrchestrationID) {
		return parsedDaemonOrchestration{}, fmt.Errorf("orchestration identifier is not a canonical UUID")
	}
	if len(item.Node) > maxDaemonOrchestrationNodeLength {
		return parsedDaemonOrchestration{}, fmt.Errorf("orchestration node exceeds %d characters", maxDaemonOrchestrationNodeLength)
	}
	if len(item.State) > maxDaemonOrchestrationStateLength {
		return parsedDaemonOrchestration{}, fmt.Errorf("orchestration state exceeds %d characters", maxDaemonOrchestrationStateLength)
	}
	if item.Path != nil {
		if len(*item.Path) > maxDaemonOrchestrationPathLength || strings.ContainsAny(*item.Path, "*?[]") {
			return parsedDaemonOrchestration{}, fmt.Errorf("orchestration object path exceeds its bound or contains selector characters")
		}
		if _, err := validateExactObjectPath(*item.Path); err != nil {
			return parsedDaemonOrchestration{}, fmt.Errorf("invalid orchestration object path: %w", err)
		}
	}
	startedAt, err := time.Parse(time.RFC3339Nano, item.StartedAt)
	if err != nil {
		return parsedDaemonOrchestration{}, fmt.Errorf("invalid started_at %q", item.StartedAt)
	}
	if item.EndedAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *item.EndedAt); err != nil {
			return parsedDaemonOrchestration{}, fmt.Errorf("invalid ended_at %q", *item.EndedAt)
		}
	}
	return parsedDaemonOrchestration{raw: item, startedAt: startedAt}, nil
}

func daemonOrchestrationOutput(raw daemonOrchestrationResponse) (DaemonOrchestration, int) {
	result := DaemonOrchestration{
		OrchestrationID: raw.OrchestrationID,
		Node:            raw.Node,
		Path:            raw.Path,
		State:           raw.State,
		StartedAt:       raw.StartedAt,
		EndedAt:         raw.EndedAt,
	}
	textRunes := len([]rune(result.OrchestrationID)) + len([]rune(result.Node)) +
		len([]rune(result.State)) + len([]rune(result.StartedAt))
	for _, value := range []*string{result.Path, result.EndedAt} {
		if value != nil {
			textRunes += len([]rune(*value))
		}
	}
	if raw.Expect != nil {
		value, truncated := boundedRunes(*raw.Expect, maxDaemonOrchestrationExpectRunes)
		result.Expect = &value
		result.ExpectTruncated = truncated
		textRunes += len([]rune(value))
	}
	if raw.Error != nil {
		value, truncated := boundedRunes(*raw.Error, maxDaemonOrchestrationErrorRunes)
		result.Error = &value
		result.ErrorTruncated = truncated
		textRunes += len([]rune(value))
	}
	return result, textRunes
}
