package core

import (
	"bufio"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// previewNote is a note of a change: its key, for a client to say it in its
// own language, and its text, in English.
type previewNote struct{ key, text string }

var (
	notSetNote           = previewNote{"not_set", "not set"}
	emptyOrNotSetNote    = previewNote{"empty_or_not_set", "empty or not set"}
	secretNote           = previewNote{"secret", "secret: values not shown"}
	appliedAfterNote     = previewNote{"applied_after_file", "applied after the configuration file"}
	sectionDeletedNote   = previewNote{"section_deleted_first", "its section is deleted first"}
	currentTooLongNote   = previewNote{"current_too_long", "current value too long to compute the result"}
	afterUncomputedNote  = previewNote{"after_uncomputed", "after an operation the MCP does not compute"}
	notInListNote        = previewNote{"not_in_list", "the value is not in the list: nothing changes"}
	alreadyInListNote    = previewNote{"already_in_list", "the value is already in the list: nothing changes"}
	emptyIndexNote       = previewNote{"empty_index", "an empty value at an index removes nothing"}
	indexPastWordsNoteID = "index_past_words"
)

// addNote adds a note to a change: its text to the note, its key to the keys.
func (c *PreviewChange) addNote(note previewNote) {
	if note.key == "" {
		return
	}
	c.NoteKeys = append(c.NoteKeys, note.key)
	if c.Note == "" {
		c.Note = note.text
	} else {
		c.Note += "; " + note.text
	}
}

// PreviewObjectConfigUpdate tells what update_object_config would do with
// the same arguments: the keys it sets or unsets, read now, the value the
// MCP computes they would take, and the sections it deletes with their keys.
func (s *Service) PreviewObjectConfigUpdate(ctx context.Context, options UpdateObjectConfigOptions) (Preview, error) {
	reference, err := validateConfigWritePath(options.Path)
	if err != nil {
		return Preview{}, err
	}
	update, err := parseConfigUpdate(options.Set, options.Unset, options.Delete)
	if err != nil {
		return Preview{}, err
	}
	secretData := secretDataKeys(reference.Kind)
	read := func(keys []string, limit int) (ObjectConfig, error) {
		return s.GetObjectConfig(ctx, GetObjectConfigOptions{Path: reference.Path, Keywords: keys, Limit: limit})
	}
	preview, err := update.preview(secretData, read)
	if err != nil {
		return Preview{}, err
	}
	preview.Provenance = s.newProvenance()
	preview.Summary = summaryOf("config_update",
		"Update the configuration of {path}: {sets} set, {unsets} unset, {deletes} sections deleted",
		map[string]string{"path": reference.Path, "sets": strconv.Itoa(len(options.Set)), "unsets": strconv.Itoa(len(options.Unset)), "deletes": strconv.Itoa(len(options.Delete))})
	return preview, nil
}

// PreviewNodeConfigUpdate tells what update_node_config would do with the
// same arguments, as PreviewObjectConfigUpdate does for an object.
func (s *Service) PreviewNodeConfigUpdate(ctx context.Context, options UpdateNodeConfigOptions) (Preview, error) {
	if err := validateNodeTarget(options.Node); err != nil {
		return Preview{}, err
	}
	update, err := parseConfigUpdate(options.Set, options.Unset, options.Delete)
	if err != nil {
		return Preview{}, err
	}
	read := func(keys []string, limit int) (ObjectConfig, error) {
		config, err := s.GetNodeConfigKeywords(ctx, GetNodeConfigKeywordsOptions{Node: options.Node, Keywords: keys, Limit: limit})
		return ObjectConfig{Provenance: config.Provenance, Items: config.Items, Truncated: config.Truncated, SecretsOmitted: config.SecretsOmitted}, err
	}
	preview, err := update.preview(false, read)
	if err != nil {
		return Preview{}, err
	}
	preview.Provenance = s.newProvenance()
	preview.Summary = summaryOf("node_config_update",
		"Update the configuration of the node {node}: {sets} set, {unsets} unset, {deletes} sections deleted",
		map[string]string{"node": options.Node, "sets": strconv.Itoa(len(options.Set)), "unsets": strconv.Itoa(len(options.Unset)), "deletes": strconv.Itoa(len(options.Delete))})
	return preview, nil
}

// PreviewObjectCreate tells what create_object would do with the same
// arguments: the configuration file as given, and the set operations
// applied after it. It reads nothing: the object does not exist yet.
func (s *Service) PreviewObjectCreate(options CreateObjectOptions) (Preview, error) {
	reference, err := validateConfigWritePath(options.Path)
	if err != nil {
		return Preview{}, err
	}
	if strings.TrimSpace(options.Config) == "" {
		return Preview{}, fmt.Errorf("config is required: the configuration file of the object, as INI text")
	}
	if len(options.Set) > maxConfigOperations {
		return Preview{}, fmt.Errorf("at most %d set operations are accepted", maxConfigOperations)
	}
	// A creation may take no set operation: the file alone.
	var update configUpdate
	if len(options.Set) > 0 {
		if update, err = parseConfigUpdate(options.Set, nil, nil); err != nil {
			return Preview{}, err
		}
	}
	text, truncated := cutUTF8(options.Config, maxConfigFileOutputBytes)
	preview := Preview{Content: &PreviewContent{MediaType: "text/x-ini", Text: text, Truncated: truncated}}
	for _, operation := range update.sets {
		change := PreviewChange{Key: operation.key, Scope: operation.scope, Op: operation.op, AfterComputedByMCP: false}
		if isSecretKeyword(operation.key) || (secretDataKeys(reference.Kind) && isDataKeyword(operation.key)) {
			change.addNote(secretNote)
			preview.SecretsOmitted++
		} else {
			change.Value = operation.value
			change.addNote(appliedAfterNote)
		}
		preview.Changes = append(preview.Changes, change)
	}
	preview.Summary = summaryOf("object_create", "Create {path}, a {kind} of {sections} sections",
		map[string]string{"path": reference.Path, "kind": reference.Kind, "sections": strconv.Itoa(iniSections(options.Config))})
	preview.Provenance = s.newProvenance()
	return preview, nil
}

// iniSections counts the sections of an INI text, DEFAULT included.
func iniSections(content string) int {
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64<<10), maxCreateObjectConfigBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			count++
		}
	}
	return count
}

// configOperation is one set operation, split as the daemon splits it.
type configOperation struct {
	// key is the key as named, its scope included; base without it.
	key, base, scope string
	op, value        string
	// index is the position of an indexed =, -1 otherwise.
	index int
}

type configUpdate struct {
	sets    []configOperation
	unsets  []string
	deletes []string
}

// parseConfigUpdate checks the operations as the update does, and splits
// the set operations: the key, its scope, the operator, the value unquoted
// as om3 unquotes it, and the index of an indexed =.
func parseConfigUpdate(set, unset, deletes []string) (configUpdate, error) {
	if len(set)+len(unset)+len(deletes) == 0 {
		return configUpdate{}, fmt.Errorf("at least one set, unset or delete operation is required")
	}
	if len(set)+len(unset)+len(deletes) > maxConfigOperations {
		return configUpdate{}, fmt.Errorf("at most %d operations are accepted in one update", maxConfigOperations)
	}
	if _, _, err := validateConfigSets(set); err != nil {
		return configUpdate{}, err
	}
	for _, key := range unset {
		if err := validateConfigKey(key); err != nil {
			return configUpdate{}, fmt.Errorf("unset %q: %w", key, err)
		}
	}
	for _, section := range deletes {
		if err := validateConfigSection(section); err != nil {
			return configUpdate{}, fmt.Errorf("delete %q: %w", section, err)
		}
	}
	update := configUpdate{unsets: unset, deletes: deletes}
	for _, operation := range set {
		key, operator, value := splitConfigOperation(operation)
		parsed := configOperation{op: operator, value: unquoteConfigValue(value), index: -1}
		if match := configKeyIndex.FindStringSubmatch(key); match != nil {
			index, err := strconv.Atoi(match[2])
			if err != nil {
				return configUpdate{}, fmt.Errorf("set %q: the index is not a number", operation)
			}
			key, parsed.op, parsed.index = match[1], "insert", index
		}
		parsed.key = key
		parsed.base, parsed.scope, _ = strings.Cut(key, "@")
		update.sets = append(update.sets, parsed)
	}
	return update, nil
}

// unquoteConfigValue strips one pair of quotes around a value, as om3
// does: a value surrounded by them and holding no other.
func unquoteConfigValue(value string) string {
	for _, quote := range []byte{'\'', '"'} {
		if len(value) >= 2 && value[0] == quote && value[len(value)-1] == quote && strings.IndexByte(value[1:], quote) == len(value)-2 {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// configRead reads keys of the configuration, all of them when keys is
// empty, at most limit.
type configRead func(keys []string, limit int) (ObjectConfig, error)

// preview reads the keys the update names, and the whole configuration
// when it deletes sections, then tells each change, in the order the daemon
// applies them: the sections deleted, the keys unset, then the sets, each
// from the value the previous ones leave.
func (u configUpdate) preview(secretData bool, read configRead) (Preview, error) {
	hidden := func(key string) bool {
		return isSecretKeyword(key) || (secretData && isDataKeyword(key))
	}
	var keys, names []string
	want := func(key string) {
		if !hidden(key) && !slices.Contains(names, configKeyName(key)) {
			keys, names = append(keys, key), append(names, configKeyName(key))
		}
	}
	for _, operation := range u.sets {
		want(operation.key)
	}
	for _, key := range u.unsets {
		want(key)
	}
	preview := Preview{}
	current := map[string]ObjectConfigKeyword{}
	readAt := ""
	if len(keys) > 0 {
		config, err := read(keys, maxGetObjectConfigLimit)
		if err != nil {
			return Preview{}, err
		}
		readAt = config.Provenance.ObservedAt
		// The daemon gives a scoped key with its scope in the keyword, for an
		// object or a node; the node of a node read is in Node, no scope. It
		// names a key of DEFAULT without its section: the name is taken again
		// as a precaution only.
		for _, item := range config.Items {
			if _, seen := current[configKeyName(item.Keyword)]; !seen {
				current[configKeyName(item.Keyword)] = item
			}
		}
	}
	if len(u.deletes) > 0 {
		config, err := read(nil, maxGetObjectConfigLimit)
		if err != nil {
			return Preview{}, err
		}
		readAt = config.Provenance.ObservedAt
		for _, section := range u.deletes {
			deleted := PreviewDeletedSection{Section: section, Keys: []string{}}
			for _, item := range config.Items {
				if strings.HasPrefix(item.Keyword, section+".") && !slices.Contains(deleted.Keys, item.Keyword) {
					deleted.Keys = append(deleted.Keys, item.Keyword)
				}
			}
			// The read leaves the secrets out: a secret key of a deleted
			// section is counted, never named.
			deleted.SecretsOmitted = secretsOfSection(config, section)
			preview.SecretsOmitted += deleted.SecretsOmitted
			preview.Deleted = append(preview.Deleted, deleted)
		}
		if config.Truncated {
			preview.Warnings = append(preview.Warnings, fmt.Sprintf("the configuration holds more than the %d keys read: the keys of the deleted sections may be more", maxGetObjectConfigLimit))
		}
	}
	// state is the value each key holds as the operations go, and whether
	// it is known in full, by the name the daemon gives the key: two names
	// of one key, with and without DEFAULT, share it.
	type value struct {
		text  string
		known bool
		note  previewNote
	}
	state := map[string]value{}
	valueOf := func(key string) value {
		if v, ok := state[configKeyName(key)]; ok {
			return v
		}
		if section, _ := configKeySplit(key); slices.Contains(u.deletes, section) {
			return value{known: true, note: sectionDeletedNote}
		}
		item, ok := current[configKeyName(key)]
		switch {
		case !ok:
			return value{known: true, note: notSetNote}
		case item.ValueTruncated:
			return value{text: item.Value, note: currentTooLongNote}
		case item.Value == "":
			return value{known: true, note: emptyOrNotSetNote}
		}
		return value{text: item.Value, known: true}
	}
	for _, key := range u.unsets {
		change := PreviewChange{Key: key, Op: "unset", AfterComputedByMCP: true}
		_, change.Scope, _ = strings.Cut(key, "@")
		if hidden(key) {
			change.AfterComputedByMCP = false
			change.addNote(secretNote)
			preview.SecretsOmitted++
		} else {
			before := valueOf(key)
			change.Before, change.After = ptr(before.text), ptr("")
			change.addNote(before.note)
		}
		state[configKeyName(key)] = value{known: true}
		preview.Changes = append(preview.Changes, change)
	}
	for _, operation := range u.sets {
		change := PreviewChange{Key: operation.key, Scope: operation.scope, Op: operation.op}
		if hidden(operation.key) {
			change.addNote(secretNote)
			preview.SecretsOmitted++
			state[configKeyName(operation.key)] = value{}
			preview.Changes = append(preview.Changes, change)
			continue
		}
		change.Value = operation.value
		before := valueOf(operation.key)
		change.Before = ptr(before.text)
		change.addNote(before.note)
		after, computed, note := applyConfigOperation(before.text, before.known, operation)
		change.addNote(note)
		if computed {
			change.After, change.AfterComputedByMCP = ptr(after), true
			state[configKeyName(operation.key)] = value{text: after, known: true}
		} else {
			state[configKeyName(operation.key)] = value{note: afterUncomputedNote}
		}
		preview.Changes = append(preview.Changes, change)
	}
	preview.ReadAt = readAt
	return preview, nil
}

// configKeySplit splits a key as om3 does: the section of env, data and
// labels is the part before the first dot, of the others the part before the
// last dot ahead of the scope, and a key without a dot is of DEFAULT. The
// option keeps the scope.
func configKeySplit(key string) (section, option string) {
	if first, rest, ok := strings.Cut(key, "."); ok && slices.Contains([]string{"env", "data", "labels"}, first) {
		return first, rest
	}
	prefix, scope, scoped := strings.Cut(key, "@")
	dot := strings.LastIndex(prefix, ".")
	if dot < 0 {
		return "DEFAULT", key
	}
	section, option = prefix[:dot], prefix[dot+1:]
	if scoped {
		option += "@" + scope
	}
	return section, option
}

// configKeyName gives a key as the daemon names it: om3 leaves the DEFAULT
// section out of the name of its keys, so DEFAULT.orchestrate is read back
// as orchestrate. The section is told by its exact case, as om3 tells it.
func configKeyName(key string) string {
	if section, option := configKeySplit(key); section == "DEFAULT" {
		return option
	}
	return key
}

// applyConfigOperation computes the value a key takes, from its raw value,
// as om3 applies the operator: = sets, += appends even a value present,
// -= removes every word equal to the value, |= appends a value absent, ^=
// removes a value present or appends it, and an indexed = inserts the value
// at that word, an empty value at an index removing nothing. Not computed
// from a value not known in full, or for an index past the words.
func applyConfigOperation(before string, known bool, operation configOperation) (string, bool, previewNote) {
	if operation.op == "=" {
		return operation.value, true, previewNote{}
	}
	if !known {
		return "", false, previewNote{}
	}
	words := strings.Fields(before)
	switch operation.op {
	case "+=":
		if before == "" {
			return operation.value, true, previewNote{}
		}
		return before + " " + operation.value, true, previewNote{}
	case "-=":
		kept := slices.DeleteFunc(slices.Clone(words), func(word string) bool { return word == operation.value })
		if len(kept) == len(words) {
			return before, true, notInListNote
		}
		return strings.Join(kept, " "), true, previewNote{}
	case "|=":
		if slices.Contains(words, operation.value) {
			return before, true, alreadyInListNote
		}
		return applyConfigOperation(before, known, configOperation{op: "+=", value: operation.value})
	case "^=":
		if slices.Contains(words, operation.value) {
			return applyConfigOperation(before, known, configOperation{op: "-=", value: operation.value})
		}
		return applyConfigOperation(before, known, configOperation{op: "+=", value: operation.value})
	case "insert":
		if operation.value == "" {
			return before, true, emptyIndexNote
		}
		if operation.index > len(words) {
			return "", false, previewNote{indexPastWordsNoteID, fmt.Sprintf("index %d is past the %d words of the value: the daemon refuses or fails", operation.index, len(words))}
		}
		inserted := slices.Insert(slices.Clone(words), operation.index, operation.value)
		return strings.Join(inserted, " "), true, previewNote{}
	}
	return "", false, previewNote{}
}

// secretsOfSection counts the secret keys of a section the read left out:
// the secret keywords of that section, as many as the read omitted, and the
// data keys of a secret.
func secretsOfSection(config ObjectConfig, section string) int {
	if strings.EqualFold(section, "data") {
		return config.DataKeysOmitted
	}
	count := 0
	for _, keyword := range secretKeywords {
		base, _, _ := strings.Cut(strings.ToLower(section), "#")
		if strings.HasPrefix(keyword, base+".") {
			count++
		}
	}
	return min(count, config.SecretsOmitted)
}

func ptr(value string) *string { return &value }
