package core

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// maxPreviewParamRunes bounds each value a summary names: they come from the
// arguments of the model, untrusted text.
const maxPreviewParamRunes = 256

// Preview tells what a state-changing call would do, before it runs, for the
// user who confirms it: a summary, the configuration keys it changes, the
// sections it deletes, or the content it writes. It reads, never writes.
type Preview struct {
	Provenance Provenance              `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Summary    PreviewSummary          `json:"summary" jsonschema:"what the call does, in one sentence"`
	Changes    []PreviewChange         `json:"changes,omitempty" jsonschema:"the keys the call sets or unsets, with their configured value read now and the value the MCP computes they would take"`
	Deleted    []PreviewDeletedSection `json:"deleted,omitempty" jsonschema:"the sections the call deletes, with the keys they hold"`
	Content    *PreviewContent         `json:"content,omitempty" jsonschema:"the content the call writes, as given"`
	// ReadAt is the time the current values were read: they may change
	// before the call runs.
	ReadAt   string   `json:"read_at,omitempty" jsonschema:"when the current values were read, RFC 3339 UTC; they may change before the call runs"`
	Warnings []string `json:"warnings,omitempty" jsonschema:"what limits the preview, such as a read cut short"`
	// SecretsOmitted counts the changes on secret keys, whose values are
	// never shown.
	SecretsOmitted int `json:"secrets_omitted,omitempty" jsonschema:"the number of changes on secret keys, cluster.secret, node.uuid and store.ssh_private_key, or data keys of a secret, whose values are not shown"`
}

// PreviewSummary is the sentence of a preview, and what a client composes
// it again from in its own language: key names the sentence, params fill it.
type PreviewSummary struct {
	Text   string            `json:"text" jsonschema:"the sentence, in English"`
	Key    string            `json:"key" jsonschema:"the name of the sentence, for a client to say it in its own language"`
	Params map[string]string `json:"params" jsonschema:"the values the sentence names, untrusted text"`
}

// PreviewChange is one key a call sets or unsets.
type PreviewChange struct {
	Key string `json:"key" jsonschema:"the key, as the operation names it, its node scope included, such as fs#1.mnt@n1"`
	// Scope is the node a scoped key applies to.
	Scope string `json:"scope,omitempty" jsonschema:"the node the key is scoped to, the part after @"`
	Op    string `json:"op" jsonschema:"the operation: =, +=, -=, |=, ^=, insert for an indexed =, or unset"`
	Value string `json:"value,omitempty" jsonschema:"the value the operation gives, absent for an unset or a secret"`
	// Before and After are absent for a secret.
	Before *string `json:"before,omitempty" jsonschema:"the raw configured value read now, not evaluated; empty when the key is not set; absent for a secret"`
	After  *string `json:"after,omitempty" jsonschema:"the value the MCP computes the key would take; the daemon applies the operation and is the authority; absent for a secret or when it is not computed"`
	// AfterComputedByMCP is false when the MCP could not compute After.
	AfterComputedByMCP bool   `json:"after_computed_by_mcp" jsonschema:"whether after was computed by the MCP, as om3 applies the operator"`
	Note               string `json:"note,omitempty" jsonschema:"what to know of this change, such as a key not set, in English"`
	// NoteKeys name the notes of Note, for a client to say them in its
	// own language.
	NoteKeys []string `json:"note_keys,omitempty" jsonschema:"the keys of the notes, in their order: not_set, empty_or_not_set, secret, applied_after_file, section_deleted_first, current_too_long, after_uncomputed, not_in_list, already_in_list, empty_index, index_past_words"`
}

// PreviewDeletedSection is a section a call deletes, and its keys.
type PreviewDeletedSection struct {
	Section string   `json:"section" jsonschema:"the section deleted, such as fs#2"`
	Keys    []string `json:"keys" jsonschema:"the keys the section holds, read now, secrets left out"`
	// SecretsOmitted counts the secret keys of the section, never named.
	SecretsOmitted int `json:"secrets_omitted,omitempty" jsonschema:"the number of secret keys the section holds, left out of keys and counted in the secrets_omitted of the preview"`
}

// PreviewContent is a content a call writes, such as a configuration file.
type PreviewContent struct {
	MediaType string `json:"media_type" jsonschema:"the media type of the text, such as text/x-ini"`
	Text      string `json:"text" jsonschema:"the content, as given to the call"`
	Truncated bool   `json:"truncated,omitempty" jsonschema:"whether the text was cut to keep the preview bounded"`
}

// PreviewActionOptions names the action to preview and its arguments.
type PreviewActionOptions struct {
	Tool      string
	Arguments map[string]any
}

// actionSummaries are the sentences of the actions the user confirms, by
// tool, each {name} an argument of the tool. A tool absent here has no
// summary; a test holds the table to the arguments of the tools.
var actionSummaries = map[string]string{
	"cap_object":                "Cap the resources of {path}",
	"create_object_certificate": "Create the certificate of {path}",
	"delete_object":             "Delete {path}, its configuration on every node",
	"delete_object_data_key":    "Delete the key {name} of {path}",
	"dequeue_node_actions":      "Run the actions the collector queued for {node}",
	"disable_object":            "Disable {path}",
	"drain_node":                "Drain {node}",
	"giveback_object":           "Give {path} back to its preferred nodes",
	"purge_object":              "Purge {path}: unprovision its resources on every node, then delete it",
	"restart_daemon":            "Restart the daemon of {node}",
	"restart_daemon_listener":   "Restart the daemon listener of {node}",
	"restart_heartbeat":         "Restart the heartbeat {name} on {node}",
	"restart_instance":          "Restart {path} on {node}",
	"restart_object":            "Restart {path}",
	"run_task":                  "Run the task {rids} of {path} on {node}",
	"set_object_data_key":       "Set the key {name} of {path}",
	"shutdown_instance":         "Shut down {path} on {node}, standby resources included",
	"stop_instance":             "Stop {path} on {node}",
	"stop_object":               "Stop {path}",
	"switch_object":             "Switch {path}",
	"unfreeze_cluster":          "Unfreeze the cluster",
	"unfreeze_instance":         "Unfreeze {path} on {node}",
	"unfreeze_node":             "Unfreeze {node}",
	"unfreeze_object":           "Unfreeze {path}",
	"unprovision_instance":      "Unprovision {path} on {node}",
	"unprovision_object":        "Unprovision {path} on every node",
	"update_instance_caps":      "Apply the caps of {path} on {node}",
}

// optionalSummaryParams end a sentence when the call gives them: the
// resources it is limited to, the node it moves to.
var optionalSummaryParams = []struct{ name, text string }{
	{"rids", " ({rids})"},
	{"to", " to {to}"},
}

var summaryParam = regexp.MustCompile(`\{([a-z_]+)\}`)

// ActionSummaryParams gives the arguments each sentence names, the optional
// ones included, by tool: the test holding the table to the tools reads it.
func ActionSummaryParams() map[string][]string {
	params := make(map[string][]string, len(actionSummaries))
	for tool, text := range actionSummaries {
		for _, match := range summaryParam.FindAllStringSubmatch(text, -1) {
			params[tool] = append(params[tool], match[1])
		}
	}
	return params
}

// ActionSummaryTools are the tools a sentence is known for.
func ActionSummaryTools() []string {
	return slices.Sorted(maps.Keys(actionSummaries))
}

// PreviewAction gives the sentence of an action: it reads nothing, the
// arguments fill the sentence of the tool as they are given.
func (s *Service) PreviewAction(options PreviewActionOptions) (Preview, error) {
	text, ok := actionSummaries[options.Tool]
	if !ok {
		return Preview{}, fmt.Errorf("no preview of the tool %q", options.Tool)
	}
	params := map[string]string{}
	for _, match := range summaryParam.FindAllStringSubmatch(text, -1) {
		value, err := summaryValue(options.Arguments[match[1]])
		if err != nil || value == "" {
			return Preview{}, fmt.Errorf("preview of %s: argument %s is required", options.Tool, match[1])
		}
		params[match[1]] = value
	}
	for _, optional := range optionalSummaryParams {
		if _, named := params[optional.name]; named {
			continue
		}
		if value, err := summaryValue(options.Arguments[optional.name]); err == nil && value != "" {
			params[optional.name] = value
			text += optional.text
		}
	}
	return Preview{Provenance: s.newProvenance(), Summary: summaryOf(options.Tool, text, params)}, nil
}

// summaryValue is an argument as a sentence names it: a string, a number or
// a list of strings, joined; bounded, never interpreted.
func summaryValue(value any) (string, error) {
	var text string
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		text = typed
	case float64, bool, json.Number:
		text = fmt.Sprint(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			part, ok := item.(string)
			if !ok {
				return "", fmt.Errorf("a list of strings is expected")
			}
			parts = append(parts, part)
		}
		text = strings.Join(parts, ", ")
	default:
		return "", fmt.Errorf("unsupported value")
	}
	bounded, _, _ := boundObjectConfigValue(text, maxPreviewParamRunes)
	return bounded, nil
}

// summaryOf fills a sentence with its params.
func summaryOf(key, text string, params map[string]string) PreviewSummary {
	filled := summaryParam.ReplaceAllStringFunc(text, func(name string) string {
		return params[strings.Trim(name, "{}")]
	})
	return PreviewSummary{Text: filled, Key: key, Params: params}
}
