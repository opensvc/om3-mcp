package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

const (
	maxConfigOperations      = 50
	maxConfigKeyLength       = 255
	maxConfigValueBytes      = 4 << 10
	maxConfigWaitSeconds     = 120
	configLastModifiedHeader = "OM-Last-Modified"
)

// configWriteOperators are the operators of a set operation that write a
// value, in the order the daemon tries them; the comparison operators the
// daemon also parses select, they do not write.
var configWriteOperators = []string{"=", "+=", "-=", "|=", "^="}

// configParsedOperators are all the operators the daemon splits a set
// operation on: the first one found in the operation is its operator.
var configParsedOperators = []string{"+=", "-=", "|=", "^=", "!=", ">=", "<=", ">", "<", "="}

var configKeyIndex = regexp.MustCompile(`^(.+)\[(\d+)]$`)

type UpdateObjectConfigOptions struct {
	Path   string
	Set    []string
	Unset  []string
	Delete []string
	// WaitSeconds holds the answer until the configuration written reached
	// every live node of the object, at most until the request timeout.
	WaitSeconds int
}

type ObjectConfigUpdate struct {
	Provenance Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object     ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference"`
	Outcome    string                 `json:"outcome" jsonschema:"written when the configuration changed, unchanged when the operations left it as it was"`
	IsChanged  bool                   `json:"is_changed" jsonschema:"whether the daemon wrote a changed configuration"`
	// LastModified is the time the configuration now carries.
	LastModified string   `json:"last_modified,omitempty" jsonschema:"the time the daemon gives the configuration written, as OM-Last-Modified"`
	Warnings     []string `json:"warnings,omitempty" jsonschema:"what the daemon leaves to look at, the write being done, such as a namespace holding more than it claims"`
	// PropagationPending are the live nodes the configuration had not
	// reached when the wait expired: the write is done, never to be repeated.
	PropagationPending []string `json:"propagation_pending,omitempty" jsonschema:"the live nodes the configuration had not reached when the wait expired; the write is done and must not be repeated, a += would apply twice"`
	// Keywords are the keywords the operations set or unset, read again.
	Keywords      []ObjectConfigKeyword `json:"keywords" jsonschema:"the keywords the set and unset operations name, read again after the write: the daemon reads an unset keyword back with an empty value, as it does a keyword never set"`
	KeywordsError string                `json:"keywords_error,omitempty" jsonschema:"why the keywords could not be read again; the write itself is done"`
	// SecretsOmitted counts the secret keywords written and left out of
	// the read back: written, never returned.
	SecretsOmitted int `json:"secrets_omitted,omitempty" jsonschema:"the number of secret keywords the operations name, cluster.secret, node.uuid and store.ssh_private_key, left out of the keywords read again: written, their values are never returned"`
}

// daemonProblem is an error answer of the daemon, its status and the detail
// of its problem.
type daemonProblem interface {
	error
	HTTPStatus() int
	ProblemDetail() string
}

type daemonCommitted struct {
	IsChanged bool      `json:"is_changed"`
	Warnings  *[]string `json:"warnings"`
}

// configPropagationExpired reads the 408 of a wait that expired: the
// configuration committed, and the nodes it had not reached.
var configPropagationExpired = regexp.MustCompile(`committed as of (\S+), and has not reached (.+) before the wait expired`)

// UpdateObjectConfig deletes sections, unsets keywords and applies set
// operations to the configuration of one object, as the daemon does, in
// this order. The daemon validates the result and refuses what it does not
// accept, with the reason; the MCP checks the form of the operations only.
func (s *Service) UpdateObjectConfig(ctx context.Context, options UpdateObjectConfigOptions) (ObjectConfigUpdate, error) {
	reference, err := validateConfigWritePath(options.Path)
	if err != nil {
		return ObjectConfigUpdate{}, err
	}
	if len(options.Set)+len(options.Unset)+len(options.Delete) == 0 {
		return ObjectConfigUpdate{}, fmt.Errorf("at least one set, unset or delete operation is required")
	}
	if len(options.Set)+len(options.Unset)+len(options.Delete) > maxConfigOperations {
		return ObjectConfigUpdate{}, fmt.Errorf("at most %d operations are accepted in one update", maxConfigOperations)
	}
	sets, setKeys, err := validateConfigSets(options.Set)
	if err != nil {
		return ObjectConfigUpdate{}, err
	}
	for _, key := range options.Unset {
		if err := validateConfigKey(key); err != nil {
			return ObjectConfigUpdate{}, fmt.Errorf("unset %q: %w", key, err)
		}
	}
	for _, section := range options.Delete {
		if err := validateConfigSection(section); err != nil {
			return ObjectConfigUpdate{}, fmt.Errorf("delete %q: %w", section, err)
		}
	}
	if options.WaitSeconds < 0 || options.WaitSeconds > maxConfigWaitSeconds {
		return ObjectConfigUpdate{}, fmt.Errorf("wait_seconds must be between 0 and %d", maxConfigWaitSeconds)
	}
	patcher, ok := s.client.(JSONPatcher)
	if !ok {
		return ObjectConfigUpdate{}, fmt.Errorf("OpenSVC daemon client does not support PATCH requests")
	}
	query := url.Values{}
	for _, section := range options.Delete {
		query.Add("delete", section)
	}
	for _, key := range options.Unset {
		query.Add("unset", key)
	}
	for _, operation := range sets {
		query.Add("set", operation)
	}
	if options.WaitSeconds > 0 {
		query.Set("wait", fmt.Sprintf("%ds", options.WaitSeconds))
	}
	endpoint := fmt.Sprintf("/api/object/path/%s/%s/%s/config", reference.Namespace, reference.Kind, reference.Name)
	var committed daemonCommitted
	header, err := patcher.PatchJSON(ctx, endpoint, query, &committed)
	result := ObjectConfigUpdate{Object: reference, Outcome: "written", IsChanged: true}
	if err != nil {
		// A wait that expired is no failure of the write: the configuration is
		// committed, and still on its way to the nodes the daemon names.
		var problem daemonProblem
		if !errors.As(err, &problem) || problem.HTTPStatus() != http.StatusRequestTimeout {
			return ObjectConfigUpdate{}, fmt.Errorf("update object config: %w", err)
		}
		match := configPropagationExpired.FindStringSubmatch(problem.ProblemDetail())
		if match == nil {
			return ObjectConfigUpdate{}, fmt.Errorf("update object config: %w", err)
		}
		result.LastModified = match[1]
		result.PropagationPending = strings.Split(match[2], ", ")
	} else {
		result.IsChanged = committed.IsChanged
		if !committed.IsChanged {
			result.Outcome = "unchanged"
		}
		if committed.Warnings != nil {
			result.Warnings = *committed.Warnings
		}
	}
	if modified := header.Get(configLastModifiedHeader); modified != "" {
		result.LastModified = modified
	}
	// The keywords the operations name are read again, for the value
	// written to be seen: the daemon gives an unset one an empty value.
	keys := slices.Concat(setKeys, options.Unset)
	result.Keywords = []ObjectConfigKeyword{}
	if len(keys) > 0 {
		config, err := s.GetObjectConfig(ctx, GetObjectConfigOptions{Path: reference.Path, Keywords: keys, Limit: maxGetObjectConfigLimit})
		if err != nil {
			result.KeywordsError = err.Error()
		} else {
			result.Keywords = config.Items
			result.SecretsOmitted = config.SecretsOmitted
		}
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

// validateConfigWritePath accepts one exact object path, as the actions do.
func validateConfigWritePath(path string) (ClusterObjectReference, error) {
	if hasSelectorCharacters(path) {
		return ClusterObjectReference{}, fmt.Errorf("path must be one exact OpenSVC object path, without wildcard or selector")
	}
	return validateExactObjectPath(path)
}

// validateConfigSets checks the form of the set operations, a key, an
// operator that writes and a value, as the daemon parses them: an operation
// it would read otherwise, such as a key without operator taken for an
// empty one, is refused rather than written. It gives the keys they name,
// without index.
func validateConfigSets(operations []string) ([]string, []string, error) {
	keys := make([]string, 0, len(operations))
	for _, operation := range operations {
		key, operator, value := splitConfigOperation(operation)
		switch {
		case operator == "":
			return nil, nil, fmt.Errorf("set %q: an operation is <section>.<keyword><operator><value>, the operator one of %s", operation, strings.Join(configWriteOperators, ", "))
		case !slices.Contains(configWriteOperators, operator):
			return nil, nil, fmt.Errorf("set %q: operator must be one of %s, not %s", operation, strings.Join(configWriteOperators, ", "), operator)
		case len(value) > maxConfigValueBytes:
			return nil, nil, fmt.Errorf("set %q: the value exceeds %d bytes", key, maxConfigValueBytes)
		}
		if match := configKeyIndex.FindStringSubmatch(key); match != nil {
			if operator != "=" {
				return nil, nil, fmt.Errorf("set %q: an indexed keyword takes the operator = only", operation)
			}
			key = match[1]
		}
		if err := validateConfigKey(key); err != nil {
			return nil, nil, fmt.Errorf("set %q: %w", operation, err)
		}
		keys = append(keys, key)
	}
	return operations, keys, nil
}

// splitConfigOperation splits an operation on its first operator, as the
// daemon does: of the operators found, the one starting first.
func splitConfigOperation(operation string) (string, string, string) {
	position, operator := len(operation), ""
	for _, candidate := range configParsedOperators {
		if index := strings.Index(operation, candidate); index >= 0 && index < position {
			position, operator = index, candidate
		}
	}
	if operator == "" {
		return operation, "", ""
	}
	return operation[:position], operator, operation[position+len(operator):]
}

func validateConfigKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("the keyword is empty")
	case len(key) > maxConfigKeyLength:
		return fmt.Errorf("the keyword exceeds %d characters", maxConfigKeyLength)
	case strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		return fmt.Errorf("the keyword holds a space or a control character")
	}
	return nil
}

// validateConfigSection accepts a section name: the daemon skips an empty
// one, and DEFAULT is the whole object, not a section to delete.
func validateConfigSection(section string) error {
	if strings.EqualFold(strings.TrimSpace(section), "DEFAULT") {
		return fmt.Errorf("DEFAULT is not a section to delete")
	}
	return validateConfigKey(section)
}
