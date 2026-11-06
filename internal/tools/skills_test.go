package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/opensvc/om3-mcp/internal/core"
	"go.yaml.in/yaml/v2"
)

// skillToolsKey names, in the metadata of a skill, the tools it uses, which
// a client adds to the catalog of a run when it loads the skill.
const skillToolsKey = "opensvc/tools"

var (
	skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// skillToolReference is a tool the body of a skill tells to call: a
	// name between backticks. A tool it only names, such as one never to
	// use, is written without them.
	skillToolReference = regexp.MustCompile("`([a-z]+(?:_[a-z]+)+)`")
)

type skillFrontmatter struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Metadata    map[string]string `yaml:"metadata"`
}

// TestSkillsNameRealTools checks every skill against the whole catalog, the
// action tools included: a skill naming a renamed tool is worse than no
// skill.
func TestSkillsNameRealTools(t *testing.T) {
	registrar := newTestRegistrar(t)
	for _, register := range []func(*Registrar, *core.Service) error{
		RegisterAuthTools, RegisterDaemonTools, RegisterDaemonLogTools, RegisterClusterTools, RegisterClusterLockTools,
		RegisterNetworkTools, RegisterNodeTools, RegisterNodeConfigTools, RegisterObjectTools, RegisterInstanceTools,
		RegisterResourceTools, RegisterScheduleTools, RegisterPoolTools, RegisterPreviewTools,
		RegisterObjectActionTools, RegisterObjectConfigWriteTools, RegisterObjectLifecycleTools, RegisterInstanceActionTools,
		RegisterNodeActionTools, RegisterObjectDataActionTools, RegisterNodeConfigWriteTools, RegisterClusterActionTools,
		RegisterDaemonActionTools, RegisterNodeRefreshTools,
	} {
		if err := register(registrar, nil); err != nil {
			t.Fatalf("register tools: %v", err)
		}
	}
	paths, err := filepath.Glob("../../docs/skills/*/SKILL.md")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no skill found: %v", err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rest, ok := strings.CutPrefix(string(content), "---\n")
			if !ok {
				t.Fatal("SKILL.md does not begin with a frontmatter")
			}
			header, body, ok := strings.Cut(rest, "\n---\n")
			if !ok {
				t.Fatal("the frontmatter is not closed")
			}
			var frontmatter skillFrontmatter
			if err := yaml.Unmarshal([]byte(header), &frontmatter); err != nil {
				t.Fatalf("frontmatter: %v", err)
			}
			if frontmatter.Name != filepath.Base(filepath.Dir(path)) || len(frontmatter.Name) > 64 || !skillNamePattern.MatchString(frontmatter.Name) {
				t.Errorf("name %q must be the directory name, at most 64 lowercase letters, digits and single hyphens", frontmatter.Name)
			}
			if frontmatter.Description == "" || utf8.RuneCountInString(frontmatter.Description) > 1024 {
				t.Errorf("description must hold 1 to 1024 characters")
			}
			declared := strings.Fields(frontmatter.Metadata[skillToolsKey])
			if len(declared) == 0 {
				t.Fatalf("metadata %s lists no tool", skillToolsKey)
			}
			for index, name := range declared {
				if _, ok := registrar.names[name]; !ok {
					t.Errorf("metadata %s names %s, which is no tool", skillToolsKey, name)
				}
				if slices.Contains(declared[:index], name) {
					t.Errorf("metadata %s names %s twice", skillToolsKey, name)
				}
			}
			referenced := map[string]bool{}
			for _, match := range skillToolReference.FindAllStringSubmatch(body, -1) {
				referenced[match[1]] = true
				if !slices.Contains(declared, match[1]) {
					t.Errorf("the body calls %s, missing from metadata %s", match[1], skillToolsKey)
				}
			}
			for _, name := range declared {
				if !referenced[name] {
					t.Errorf("metadata %s names %s, which the body never calls", skillToolsKey, name)
				}
			}
		})
	}
}
