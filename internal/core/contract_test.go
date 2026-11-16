package core

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v2"
)

// The routes of the daemon the core calls, as its tests call them, and the
// contract they are checked against: contract/om3-api.yaml, the API of the
// daemon this MCP follows, and contract/routes.txt, the operations of it the
// MCP uses, which the contract job of CI checks against the API of the daemon
// of the day (contract/check.sh).
const (
	contractSpec   = "../../contract/om3-api.yaml"
	contractRoutes = "../../contract/routes.txt"
	// updateRoutes, set to 1, writes contract/routes.txt from the tests.
	updateRoutes = "OM3MCP_UPDATE_ROUTES"
)

var calledRoutes sync.Map

// recordRoute notes a request the core sent the fake daemon clients.
func recordRoute(method, path string) {
	calledRoutes.Store(method+" "+path, true)
}

func TestMain(m *testing.M) {
	code := m.Run()
	// The routes are those of the whole package: a run of some tests calls
	// some of them only, a listing of the tests none.
	if code == 0 && flag.Lookup("test.run").Value.String() == "" && flag.Lookup("test.list").Value.String() == "" {
		if err := checkRoutes(); err != nil {
			fmt.Fprintln(os.Stderr, "contract:", err)
			code = 1
		}
	}
	os.Exit(code)
}

// checkRoutes maps each request the tests sent to an operation of the
// contract API, and compares the operations with contract/routes.txt.
func checkRoutes() error {
	operations, err := contractOperations()
	if err != nil {
		return err
	}
	used := map[string]bool{}
	var unknown []string
	calledRoutes.Range(func(key, _ any) bool {
		method, path, _ := strings.Cut(key.(string), " ")
		if operation, ok := matchOperation(operations, method, path); ok {
			used[operation] = true
		} else {
			unknown = append(unknown, key.(string))
		}
		return true
	})
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("requests the API of contract/om3-api.yaml has no operation for:\n  %s", strings.Join(unknown, "\n  "))
	}
	lines := make([]string, 0, len(used))
	for operation := range used {
		lines = append(lines, operation)
	}
	sort.Strings(lines)
	want := strings.Join(lines, "\n") + "\n"
	if os.Getenv(updateRoutes) == "1" {
		return os.WriteFile(contractRoutes, []byte(want), 0o644)
	}
	got, err := os.ReadFile(contractRoutes)
	if err != nil {
		return err
	}
	if string(got) != want {
		return fmt.Errorf("contract/routes.txt is not the operations the core uses: run %s=1 go test ./internal/core/ and commit it", updateRoutes)
	}
	return nil
}

// operation is a path of the contract API, as a pattern of its concrete
// paths, and its methods.
type operation struct {
	path    string
	pattern *regexp.Regexp
	methods []string
	// literal counts the characters outside of the parameters: the most
	// literal path wins when several match.
	literal int
}

func contractOperations() ([]operation, error) {
	data, err := os.ReadFile(contractSpec)
	if err != nil {
		return nil, err
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("read %s: %w", contractSpec, err)
	}
	parameter := regexp.MustCompile(`\{[^}/]+\}`)
	operations := make([]operation, 0, len(spec.Paths))
	for path, item := range spec.Paths {
		var methods []string
		for method := range item {
			if slices.Contains([]string{"get", "post", "put", "patch", "delete"}, method) {
				methods = append(methods, strings.ToUpper(method))
			}
		}
		operations = append(operations, operation{
			path: path, pattern: pathPattern(path), methods: methods,
			literal: len(parameter.ReplaceAllString(path, "")),
		})
	}
	return operations, nil
}

// pathPattern is the pattern of the concrete paths of an API path: a
// parameter, {name}, any text but a slash, the rest as it is.
func pathPattern(path string) *regexp.Regexp {
	var pattern strings.Builder
	pattern.WriteString("^")
	for rest := path; ; {
		start := strings.Index(rest, "{")
		end := strings.Index(rest, "}")
		if start < 0 || end < start {
			pattern.WriteString(regexp.QuoteMeta(rest))
			break
		}
		pattern.WriteString(regexp.QuoteMeta(rest[:start]) + `[^/]+`)
		rest = rest[end+1:]
	}
	pattern.WriteString("$")
	return regexp.MustCompile(pattern.String())
}

// matchOperation finds the operation of a request: its path, as the client
// sends it, a parameter escaped within its segment, and its method; the most
// literal path wins.
func matchOperation(operations []operation, method, path string) (string, bool) {
	best, found := operation{literal: -1}, false
	for _, candidate := range operations {
		if candidate.pattern.MatchString(path) && slices.Contains(candidate.methods, method) && candidate.literal > best.literal {
			best, found = candidate, true
		}
	}
	return method + " " + best.path, found
}
