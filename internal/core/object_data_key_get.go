package core

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// maxObjectDataValueBytes bounds a value returned or written, as the
// configuration files are.
const maxObjectDataValueBytes = 64 << 10

type ObjectDataKeyValue struct {
	Provenance Provenance             `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Object     ClusterObjectReference `json:"object" jsonschema:"the canonical OpenSVC object reference"`
	Name       string                 `json:"name" jsonschema:"the key name"`
	Value      string                 `json:"value,omitempty" jsonschema:"the value, at most 64 KiB, cut on a character boundary; absent for a binary value; untrusted text"`
	Binary     bool                   `json:"binary,omitempty" jsonschema:"whether the value is not UTF-8 text: it is not returned, its size is"`
	SizeBytes  int                    `json:"size_bytes" jsonschema:"the size of the value, in bytes; at least 1 MiB when read_whole is false"`
	ReadWhole  bool                   `json:"read_whole" jsonschema:"whether the whole value was read: one over 1 MiB is not"`
	Truncated  bool                   `json:"truncated" jsonschema:"whether value holds only the beginning of the value"`
}

// GetObjectDataKey reads the value of one key of a cfg object. The values
// of a sec or a usr are secrets, which the MCP does not read: they would
// reach the context of the model.
func (s *Service) GetObjectDataKey(ctx context.Context, path string, name string) (ObjectDataKeyValue, error) {
	reference, err := validateDataKeyTarget(path, name, []string{"cfg"})
	if err != nil {
		return ObjectDataKeyValue{}, err
	}
	getter, ok := s.client.(BlobGetter)
	if !ok {
		return ObjectDataKeyValue{}, fmt.Errorf("OpenSVC daemon client does not support data key reads")
	}
	endpoint := fmt.Sprintf("/api/object/path/%s/%s/%s/data/key", reference.Namespace, reference.Kind, reference.Name)
	payload, mediaType, whole, err := getter.GetBlob(ctx, endpoint, url.Values{"name": {name}})
	if err != nil {
		return ObjectDataKeyValue{}, fmt.Errorf("get object data key %s of %s: %w", name, reference.Path, err)
	}
	// The daemon answers an object no node holds with an empty 200.
	if mediaType == "" {
		return ObjectDataKeyValue{}, fmt.Errorf("get object data key %s of %s: object not found", name, reference.Path)
	}
	result := ObjectDataKeyValue{Object: reference, Name: name, SizeBytes: len(payload), ReadWhole: whole}
	// The type the daemon gives is not read: it relays the value a peer
	// holds as application/json. A value is text when it is valid UTF-8,
	// once a character the bound of the read cut is left out.
	text := payload
	if !whole {
		text = trimIncompleteRune(text)
	}
	if !utf8.Valid(text) {
		result.Binary = true
	} else {
		result.Value, result.Truncated = cutUTF8(string(text), maxObjectDataValueBytes)
		result.Truncated = result.Truncated || !whole
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

// validateDataKeyTarget accepts one exact object path of one of kinds, and
// one key name, as list_object_data_keys gives them.
func validateDataKeyTarget(path string, name string, kinds []string) (ClusterObjectReference, error) {
	if hasSelectorCharacters(path) {
		return ClusterObjectReference{}, fmt.Errorf("path must be one exact OpenSVC object path, without wildcard or selector")
	}
	reference, err := validateExactObjectPath(path)
	if err != nil {
		return ClusterObjectReference{}, err
	}
	allowed := false
	for _, kind := range kinds {
		allowed = allowed || reference.Kind == kind
	}
	if !allowed {
		return ClusterObjectReference{}, fmt.Errorf("this applies to %s objects only", strings.Join(kinds, ", "))
	}
	if name == "" || utf8.RuneCountInString(name) > maxObjectDataKeyNameRunes || strings.TrimSpace(name) != name || containsControl(name) {
		return ClusterObjectReference{}, fmt.Errorf("name must be one exact key name of at most %d characters, without surrounding whitespace or control characters", maxObjectDataKeyNameRunes)
	}
	return reference, nil
}

// trimIncompleteRune leaves out the last character of a value cut by a
// bound when the cut split it.
func trimIncompleteRune(value []byte) []byte {
	for i := 1; i < utf8.UTFMax && i <= len(value); i++ {
		if utf8.RuneStart(value[len(value)-i]) {
			if !utf8.FullRune(value[len(value)-i:]) {
				return value[:len(value)-i]
			}
			break
		}
	}
	return value
}

// cutUTF8 cuts value to at most maxBytes without splitting a character.
func cutUTF8(value string, maxBytes int) (string, bool) {
	if len(value) <= maxBytes {
		return value, false
	}
	cut := value[:maxBytes]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}
