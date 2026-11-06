package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// maxObjectConfigReferenceBytes bounds the reference of the configuration
// of an object: the agent reads a resource of 64 KiB at most, and the
// reference is read in the turns that write a configuration.
const maxObjectConfigReferenceBytes = 16 << 10

// The keyword catalog of the daemon the examples are checked against,
// testdata/keywords.json: the options of the DEFAULT section, of a subset
// section and of the drivers the examples use, each telling whether it takes
// a scope. Taken from dev5 (om v3.0.0-rc53-24-gb1cd9cde8) with
// `om <path> config doc -o json --kw DEFAULT.*`, `--kw subset#app:g1.*` and
// `--driver <group>.<type>`; take it again when an example uses another
// driver.
type keywordCatalog map[string]map[string]bool

// The reference of the configuration of an object, docs/object-config.md,
// which an AIManager publishes in the collector as the resource
// object-config, uses in its examples only keywords the catalog of the daemon
// gives, a scope only on a keyword that takes one.
func TestObjectConfigReference(t *testing.T) {
	text, err := os.ReadFile("../../docs/object-config.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(text) > maxObjectConfigReferenceBytes {
		t.Fatalf("the reference holds %d bytes", len(text))
	}
	data, err := os.ReadFile("testdata/keywords.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog keywordCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```ini\n(.*?)```").FindAllStringSubmatch(string(text), -1)
	if len(blocks) < 3 {
		t.Fatalf("%d examples", len(blocks))
	}
	for index, block := range blocks {
		for section, keywords := range parseExample(t, index+1, block[1]) {
			checkExampleSection(t, index+1, catalog, section, keywords)
		}
	}
}

// parseExample reads the sections of an example and their keywords.
func parseExample(t *testing.T, example int, text string) map[string]map[string]string {
	t.Helper()
	sections := map[string]map[string]string{}
	current := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			current = line[1 : len(line)-1]
			if sections[current] != nil {
				t.Errorf("example %d repeats the section %s", example, current)
			}
			sections[current] = map[string]string{}
		default:
			keyword, value, ok := strings.Cut(line, " = ")
			if !ok || current == "" {
				t.Fatalf("example %d: line %q is not a keyword of a section", example, line)
			}
			sections[current][keyword] = value
		}
	}
	return sections
}

// checkExampleSection checks the keywords of a section against the catalog:
// the DEFAULT section, a subset, a free section, or a resource whose type
// selects the driver.
func checkExampleSection(t *testing.T, example int, catalog keywordCatalog, section string, keywords map[string]string) {
	t.Helper()
	var options map[string]bool
	switch {
	case section == "env" || section == "labels":
		return
	case section == "DEFAULT":
		options = catalog["DEFAULT"]
	case strings.HasPrefix(section, "subset#"):
		options = catalog["subset"]
	default:
		group, _, ok := strings.Cut(section, "#")
		if !ok || keywords["type"] == "" {
			t.Errorf("example %d: section %s is no resource with a type", example, section)
			return
		}
		driver := group + "." + keywords["type"]
		if options = catalog[driver]; options == nil {
			t.Errorf("example %d: driver %s is not in the catalog snapshot", example, driver)
			return
		}
	}
	for keyword := range keywords {
		option, scope, scoped := strings.Cut(keyword, "@")
		scopable, known := options[option]
		switch {
		case !known:
			t.Errorf("example %d: %s.%s is not a keyword of the catalog", example, section, option)
		case scoped && (!scopable || scope == ""):
			t.Errorf("example %d: %s.%s takes no scope", example, section, keyword)
		}
	}
}
