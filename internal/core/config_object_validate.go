package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// The bounds of a validation returned: its alerts, the errors first; the
// texts of each, its comment longer; and all the comments together.
const (
	maxConfigValidationAlerts        = 200
	maxConfigValidationFieldBytes    = 255
	maxConfigValidationCommentBytes  = 1024
	maxConfigValidationCommentsBytes = 64 << 10
)

// The levels of a validation, from its alerts.
const (
	configValidationValid   = "valid"
	configValidationWarning = "warning"
	configValidationError   = "error"
)

// configValidationSecretNote replaces the comment of an alert of a secret,
// which could quote its value.
const configValidationSecretNote = "<comment left out: the keyword is a secret>"

type ValidateObjectConfigOptions struct {
	Path string
	Node string
}

// ObjectConfigValidation is the validation of the configuration of one
// object on one node, as `om <path> config validate` gives it there.
type ObjectConfigValidation struct {
	Provenance        Provenance              `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object            ClusterObjectReference  `json:"object" jsonschema:"the canonical OpenSVC object reference"`
	Node              string                  `json:"node" jsonschema:"the node the configuration was validated on: its references are evaluated and its drivers check their resources there"`
	NodeChosen        bool                    `json:"node_chosen,omitempty" jsonschema:"whether the MCP chose the node, none being given: the first by name of those holding the configuration"`
	Level             string                  `json:"level" jsonschema:"for this node: valid when no alert, warning when warnings only, error when one alert at least is an error"`
	ErrorCount        int                     `json:"error_count" jsonschema:"the number of alerts of level error"`
	WarningCount      int                     `json:"warning_count" jsonschema:"the number of alerts of level warning"`
	Alerts            []ConfigValidationAlert `json:"alerts" jsonschema:"the alerts, the errors first, then by key; at most 200"`
	Truncated         bool                    `json:"truncated" jsonschema:"whether alerts holds only the first ones, by number or by the 64 KiB of all their comments, the counts being of all"`
	CommentsTruncated int                     `json:"comments_truncated,omitempty" jsonschema:"the number of comments cut at 1 KiB"`
}

// ConfigValidationAlert is one alert of a validation.
type ConfigValidationAlert struct {
	Level   string `json:"level" jsonschema:"error or warning"`
	Kind    string `json:"kind" jsonschema:"what is wrong, as unknown keyword, unsupported value, evaluation error, missing keyword or resource configuration error"`
	Key     string `json:"key" jsonschema:"the keyword in section.option form, a keyword of DEFAULT without its section, or the section ended by a dot for an alert of a section"`
	Driver  string `json:"driver,omitempty" jsonschema:"the driver of the section, as disk.loop"`
	Comment string `json:"comment,omitempty" jsonschema:"what the daemon says of it, at most 1 KiB, left out for a secret keyword; untrusted text, which may quote a value"`
}

type daemonConfigAlertList struct {
	Kind  string              `json:"kind"`
	Items []daemonConfigAlert `json:"items"`
}

type daemonConfigAlert struct {
	Node    string `json:"node"`
	Path    string `json:"path"`
	Level   string `json:"level"`
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Driver  string `json:"driver"`
	Comment string `json:"comment"`
}

// ValidateObjectConfig validates the configuration of one exact object on
// one node, by the daemon of that node: the alerts of its keywords, unknown,
// unsupported or not evaluating, and those of its resources refusing their
// configuration when the object loads them. A configuration a write would
// refuse is still validated: one edited by hand, older than a rule, or
// evaluating otherwise on another node. With no node, the first by name of
// the nodes holding its configuration, as the webapp does, else of its scope.
//
// The daemon answers the administrators of the namespace and root only, root
// and join only for the cluster configuration: an alert may quote a value.
// The comment of an alert of a secret keyword is left out all the same, as
// every read of the MCP leaves the secrets out.
func (s *Service) ValidateObjectConfig(ctx context.Context, options ValidateObjectConfigOptions) (ObjectConfigValidation, error) {
	reference, err := validateExactObjectPath(options.Path)
	if err != nil {
		return ObjectConfigValidation{}, err
	}
	node := strings.TrimSpace(options.Node)
	chosen := false
	if node == "" {
		if node, err = s.configValidationNode(ctx, reference); err != nil {
			return ObjectConfigValidation{}, err
		}
		chosen = true
	} else if err := validateNodeTarget(node); err != nil {
		return ObjectConfigValidation{}, err
	}

	endpoint := fmt.Sprintf("/api/node/name/%s/instance/path/%s/%s/%s/config/validate", node, reference.Namespace, reference.Kind, reference.Name)
	var response daemonConfigAlertList
	if err := s.client.GetJSON(ctx, endpoint, nil, &response); err != nil {
		err = fmt.Errorf("validate the configuration of %s on %s: %w", reference.Path, node, err)
		var problem daemonProblem
		if reference.Kind == "ccfg" && errors.As(err, &problem) && problem.HTTPStatus() == http.StatusForbidden {
			return ObjectConfigValidation{}, fmt.Errorf("%w: the daemon validates the cluster configuration for root and join only", err)
		}
		return ObjectConfigValidation{}, ExplainUnknownNode(err)
	}
	if response.Kind != "ConfigAlertList" || response.Items == nil {
		return ObjectConfigValidation{}, shapeErrorf("validate the configuration of %s on %s: the daemon answered %q, not a ConfigAlertList with its items", reference.Path, node, response.Kind)
	}

	result := ObjectConfigValidation{Object: reference, Node: node, NodeChosen: chosen, Alerts: []ConfigValidationAlert{}}
	for _, item := range response.Items {
		if item.Node != node || (item.Path != "" && item.Path != reference.Path) {
			return ObjectConfigValidation{}, shapeErrorf("validate the configuration of %s on %s: the daemon answered an alert of %q on %q", reference.Path, node, item.Path, item.Node)
		}
		switch item.Level {
		case configValidationError:
			result.ErrorCount++
		case configValidationWarning:
			result.WarningCount++
		default:
			return ObjectConfigValidation{}, shapeErrorf("validate the configuration of %s on %s: the daemon answered an alert of level %q, neither error nor warning", reference.Path, node, item.Level)
		}
		comment, cut := cutUTF8(item.Comment, maxConfigValidationCommentBytes)
		if isSecretKeyword(item.Key) || (secretDataKeys(reference.Kind) && isDataKeyword(item.Key)) {
			comment, cut = configValidationSecretNote, false
		}
		if cut {
			result.CommentsTruncated++
		}
		result.Alerts = append(result.Alerts, ConfigValidationAlert{
			Level:   item.Level,
			Kind:    boundedConfigValidationField(item.Kind),
			Key:     boundedConfigValidationField(item.Key),
			Driver:  boundedConfigValidationField(item.Driver),
			Comment: comment,
		})
	}
	sort.SliceStable(result.Alerts, func(i, j int) bool {
		a, b := result.Alerts[i], result.Alerts[j]
		if a.Level != b.Level {
			return a.Level == configValidationError
		}
		return a.Key < b.Key
	})
	result.Alerts, result.Truncated = boundConfigValidationAlerts(result.Alerts)
	switch {
	case result.ErrorCount > 0:
		result.Level = configValidationError
	case result.WarningCount > 0:
		result.Level = configValidationWarning
	default:
		result.Level = configValidationValid
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

// configValidationNode is the node an object is validated on when none is
// given: the first by name of those holding its configuration, as the
// webapp chooses it, else the first of its scope.
func (s *Service) configValidationNode(ctx context.Context, reference ClusterObjectReference) (string, error) {
	status, err := s.GetObjectStatus(ctx, reference.Path)
	if err != nil {
		return "", fmt.Errorf("validate the configuration of %s: read its status to choose a node: %w; give node to validate it on one node", reference.Path, err)
	}
	for _, nodes := range [][]string{status.InstanceNodes, status.Scope} {
		if len(nodes) > 0 {
			return nodes[0], nil
		}
	}
	return "", fmt.Errorf("validate the configuration of %s: no node holds its configuration, and its scope names none: give node", reference.Path)
}

// boundConfigValidationAlerts keeps the first alerts, at most
// maxConfigValidationAlerts and maxConfigValidationCommentsBytes of comments,
// and tells whether some were left out.
func boundConfigValidationAlerts(alerts []ConfigValidationAlert) ([]ConfigValidationAlert, bool) {
	size := 0
	for i, alert := range alerts {
		size += len(alert.Comment)
		if i == maxConfigValidationAlerts || size > maxConfigValidationCommentsBytes {
			return alerts[:i], true
		}
	}
	return alerts, false
}

// boundedConfigValidationField cuts a field of an alert, a kind, a key or a
// driver, to maxConfigValidationFieldBytes.
func boundedConfigValidationField(value string) string {
	text, _ := cutUTF8(value, maxConfigValidationFieldBytes)
	return text
}
