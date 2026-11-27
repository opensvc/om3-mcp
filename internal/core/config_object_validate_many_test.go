package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opensvc/om3-mcp/internal/client"
)

// validationsClient answers the object list and the validations of many
// objects, read at once from several goroutines. A hook, when set, answers a
// validation first: handled false leaves it to the payloads and errors.
type validationsClient struct {
	mu       sync.Mutex
	objects  string
	payloads map[string]string
	errs     map[string]error
	hook     func(ctx context.Context, path string) (err error, handled bool)
	queries  []url.Values
	reads    int
	read     map[string]bool
	inFlight int
	peak     int
}

func (c *validationsClient) GetJSON(ctx context.Context, path string, query url.Values, output any) error {
	recordRoute("GET", path)
	c.mu.Lock()
	if path == "/api/object" {
		defer c.mu.Unlock()
		c.queries = append(c.queries, query)
		return json.Unmarshal([]byte(c.objects), output)
	}
	c.reads++
	c.inFlight++
	c.peak = max(c.peak, c.inFlight)
	hook := c.hook
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.inFlight--
		if c.read == nil {
			c.read = map[string]bool{}
		}
		c.read[path] = true
		c.mu.Unlock()
	}()
	if hook != nil {
		if err, handled := hook(ctx, path); handled {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.errs[path]; err != nil {
		return err
	}
	payload, ok := c.payloads[path]
	if !ok {
		return fmt.Errorf("unexpected read %s", path)
	}
	return json.Unmarshal([]byte(payload), output)
}

// wasRead tells whether the validation of endpoint was answered.
func (c *validationsClient) wasRead(endpoint string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.read[endpoint]
}

// objectItem is an object of the list, the nodes holding its configuration
// and its scope given.
func objectItem(path string, nodes []string, scope []string) string {
	instances := map[string]any{}
	for _, node := range nodes {
		instances[node] = map[string]any{}
	}
	data, _ := json.Marshal(map[string]any{"meta": map[string]string{"object": path}, "data": map[string]any{"scope": scope, "instances": instances}})
	return string(data)
}

func objectsPayload(items ...string) string {
	return `{"kind":"ObjectList","items":[` + strings.Join(items, ",") + `]}`
}

// validationPayload is a ConfigAlertList of path on node, its alerts given by level and key.
func validationPayload(path, node string, alerts ...[2]string) string {
	items := make([]string, 0, len(alerts))
	for _, a := range alerts {
		items = append(items, fmt.Sprintf(`{"node":%q,"path":%q,"level":%q,"kind":"unknown keyword","key":%q,"driver":"","comment":"keyword does not exist"}`, node, path, a[0], a[1]))
	}
	return `{"kind":"ConfigAlertList","items":[` + strings.Join(items, ",") + `]}`
}

func endpointOf(t *testing.T, path, node string) string {
	t.Helper()
	reference, err := validateExactObjectPath(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("/api/node/name/%s/instance/path/%s/%s/%s/config/validate", node, reference.Namespace, reference.Kind, reference.Name)
}

// validClient lists count objects app/svc/sNNN on n1, all valid.
func validClient(t *testing.T, count int) *validationsClient {
	t.Helper()
	items := make([]string, 0, count)
	payloads := map[string]string{}
	for i := range count {
		path := fmt.Sprintf("app/svc/s%03d", i)
		items = append(items, objectItem(path, []string{"n1"}, []string{"n1"}))
		payloads[endpointOf(t, path, "n1")] = validationPayload(path, "n1")
	}
	return &validationsClient{objects: objectsPayload(items...), payloads: payloads, errs: map[string]error{}}
}

func TestValidateObjectConfigsTellsTheObjectsInErrorAndInWarning(t *testing.T) {
	client := &validationsClient{
		objects: objectsPayload(
			objectItem("with-cfg-error", []string{"n2", "n1"}, []string{"n1", "n2"}),
			objectItem("test/svc/bad-keyword", []string{"n1"}, []string{"n1"}),
			objectItem("test/svc/bad-keyword", []string{"n1"}, []string{"n1"}),
			objectItem("cluster", []string{"n1"}, []string{"n1"}),
			objectItem("data/vol/lake", nil, []string{"n2", "n1"}),
			objectItem("data/cfg/settings", []string{"n1"}, []string{"n1"}),
			objectItem("hr/sec/vault", []string{"n1"}, []string{"n1"}),
			objectItem("lost/svc/orphan", nil, nil),
		),
		payloads: map[string]string{},
		errs:     map[string]error{},
	}
	client.payloads[endpointOf(t, "with-cfg-error", "n1")] = validationPayload("with-cfg-error", "n1", [2]string{"warning", "foo#1."})
	client.payloads[endpointOf(t, "test/svc/bad-keyword", "n1")] = validationPayload("test/svc/bad-keyword", "n1", [2]string{"warning", "x"}, [2]string{"warning", "a"})
	// The vol is validated on the first of its scope, no node holding its configuration.
	client.payloads[endpointOf(t, "data/vol/lake", "n1")] = validationPayload("data/vol/lake", "n1", [2]string{"error", "orchestrate"}, [2]string{"warning", "disk#0.name"})
	client.payloads[endpointOf(t, "data/cfg/settings", "n1")] = validationPayload("data/cfg/settings", "n1")
	client.errs[endpointOf(t, "hr/sec/vault", "n1")] = daemonProblemError{status: http.StatusForbidden, detail: "the administrators of namespace hr and root only"}

	result, err := New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{})
	if err != nil {
		t.Fatalf("validate object configs: %v", err)
	}
	if result.Selector != "**" || !reflect.DeepEqual(client.queries, []url.Values{{"path": {"**"}}}) {
		t.Errorf("got selector %q, queries %v, want ** read once", result.Selector, client.queries)
	}
	// The cluster configuration is left out, the path given twice validated once.
	if result.Total != 6 || result.Validated != 6 || result.Truncated || result.NextCursor != "" || result.Stopped != "" || client.reads != 5 {
		t.Fatalf("got total %d, validated %d, truncated %v, stopped %q, %d reads, want 6, 6, not truncated, 5 reads", result.Total, result.Validated, result.Truncated, result.Stopped, client.reads)
	}
	if result.ErrorObjects != 1 || result.WarningObjects != 2 || result.ValidObjects != 1 || result.RefusedObjects != 1 || result.FailedObjects != 1 {
		t.Fatalf("got %d error, %d warning, %d valid, %d refused, %d failed, want 1, 2, 1, 1, 1",
			result.ErrorObjects, result.WarningObjects, result.ValidObjects, result.RefusedObjects, result.FailedObjects)
	}
	paths := make([]string, 0, len(result.Objects))
	for _, object := range result.Objects {
		paths = append(paths, object.Path+"@"+object.Node+"="+object.Level)
	}
	if want := []string{"data/vol/lake@n1=error", "test/svc/bad-keyword@n1=warning", "with-cfg-error@n1=warning"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("got objects %v, want the error first, then by path %v", paths, want)
	}
	if keys := []string{result.Objects[1].Alerts[0].Key, result.Objects[1].Alerts[1].Key}; !reflect.DeepEqual(keys, []string{"a", "x"}) {
		t.Errorf("got alerts %v, want by key", keys)
	}
	if !reflect.DeepEqual(result.Refused, []string{"hr/sec/vault"}) || len(result.Failed) != 1 || result.Failed[0].Objects != 1 ||
		!reflect.DeepEqual(result.Failed[0].Paths, []string{"lost/svc/orphan"}) || !strings.Contains(result.Failed[0].Reason, "no node") {
		t.Errorf("got refused %v, failed %+v", result.Refused, result.Failed)
	}
	if result.Provenance.ObservedAt == "" || result.Note != "" {
		t.Errorf("got %+v, want provenance and no note", result)
	}
}

func TestValidateObjectConfigsContinuesWithACursor(t *testing.T) {
	client := validClient(t, maxValidatedObjects+5)

	first, err := New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{Selector: "app/**"})
	if err != nil {
		t.Fatalf("validate object configs: %v", err)
	}
	if first.Total != maxValidatedObjects+5 || first.Validated != maxValidatedObjects || !first.Truncated || first.NextCursor != fmt.Sprintf("app/svc/s%03d", maxValidatedObjects-1) {
		t.Fatalf("got total %d, validated %d, truncated %v, cursor %q", first.Total, first.Validated, first.Truncated, first.NextCursor)
	}
	next, err := New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{Selector: "app/**", Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("validate object configs: %v", err)
	}
	if next.Validated != 5 || next.Truncated || next.ValidObjects != 5 || client.reads != maxValidatedObjects+5 {
		t.Fatalf("got validated %d, truncated %v, %d valid, %d reads, want the last 5", next.Validated, next.Truncated, next.ValidObjects, client.reads)
	}
	// A cursor past the last object validates nothing, and is no error.
	past, err := New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{Selector: "app/**", Cursor: "zz"})
	if err != nil || past.Validated != 0 || past.Truncated || past.Note != "" {
		t.Fatalf("got %+v, %v past the end", past, err)
	}
}

func TestValidateObjectConfigsSharesTheAlertsAmongTheObjects(t *testing.T) {
	items := make([]string, 0, maxValidatedObjects)
	payloads := map[string]string{}
	for i := range maxValidatedObjects {
		path := fmt.Sprintf("app/svc/s%03d", i)
		items = append(items, objectItem(path, []string{"n1"}, []string{"n1"}))
		payloads[endpointOf(t, path, "n1")] = validationPayload(path, "n1", [2]string{"warning", "k1"}, [2]string{"warning", "k2"}, [2]string{"warning", "k3"})
	}
	client := &validationsClient{objects: objectsPayload(items...), payloads: payloads}
	result, err := New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{})
	if err != nil {
		t.Fatalf("validate object configs: %v", err)
	}
	// Every object in warning is listed, each with its share of the alerts.
	if result.WarningObjects != maxValidatedObjects || len(result.Objects) != maxValidatedObjects {
		t.Fatalf("got %d warning, %d listed", result.WarningObjects, len(result.Objects))
	}
	for _, object := range result.Objects {
		if len(object.Alerts) != maxConfigValidationsAlerts/maxValidatedObjects || !object.AlertsTruncated || object.WarningCount != 3 {
			t.Fatalf("got %+v, want its first %d alerts, the others counted", object, maxConfigValidationsAlerts/maxValidatedObjects)
		}
	}

	// One object alone takes the alerts of all, within the bytes of the comments.
	alerts := make([]string, 0, 40)
	for k := range 40 {
		alerts = append(alerts, fmt.Sprintf(`{"node":"n1","path":"app/svc/one","level":"warning","kind":"unknown keyword","key":"k%02d","comment":%q}`, k, strings.Repeat("c", 1000)))
	}
	alerts = append(alerts, `{"node":"n1","path":"app/svc/one","level":"error","kind":"evaluation error","key":"cluster.secret","comment":"secret value s3cr3t"}`)
	client = &validationsClient{
		objects:  objectsPayload(objectItem("app/svc/one", []string{"n1"}, []string{"n1"})),
		payloads: map[string]string{endpointOf(t, "app/svc/one", "n1"): `{"kind":"ConfigAlertList","items":[` + strings.Join(alerts, ",") + `]}`},
	}
	result, err = New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{})
	if err != nil {
		t.Fatalf("validate object configs: %v", err)
	}
	object := result.Objects[0]
	size := 0
	for _, alert := range object.Alerts {
		size += len(alert.Comment)
	}
	if size > maxConfigValidationsCommentsBytes || len(object.Alerts) != 1+(maxConfigValidationsCommentsBytes-len(configValidationSecretNote))/1000 || !object.AlertsTruncated {
		t.Fatalf("got %d alerts, %d bytes of comments", len(object.Alerts), size)
	}
	if object.Alerts[0].Key != "cluster.secret" || object.Alerts[0].Comment != configValidationSecretNote {
		t.Errorf("got %+v first, want the error, its secret comment left out", object.Alerts[0])
	}
}

func TestValidateObjectConfigsStopsWhenTheDaemonThrottles(t *testing.T) {
	client := validClient(t, 10)
	throttled := endpointOf(t, "app/svc/s005", "n1")
	before := make([]string, 0, 5)
	for i := range 5 {
		before = append(before, endpointOf(t, fmt.Sprintf("app/svc/s%03d", i), "n1"))
	}
	// The 429 answers once the objects before it are validated, whatever the
	// order the goroutines run in.
	client.hook = func(ctx context.Context, path string) (error, bool) {
		if path != throttled {
			return nil, false
		}
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			done := true
			for _, endpoint := range before {
				done = done && client.wasRead(endpoint)
			}
			if done {
				break
			}
		}
		return daemonProblemError{status: http.StatusTooManyRequests, detail: "rate limit exceeded"}, true
	}
	result, err := New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{})
	if err != nil {
		t.Fatalf("validate object configs: %v", err)
	}
	if result.Validated != 5 || result.ValidObjects != 5 || result.FailedObjects != 0 || !result.Truncated || result.NextCursor != "app/svc/s004" || result.Stopped != "throttled" {
		t.Fatalf("got validated %d, %d valid, %d failed, truncated %v, cursor %q, stopped %q, want the 5 before the 429",
			result.Validated, result.ValidObjects, result.FailedObjects, result.Truncated, result.NextCursor, result.Stopped)
	}

	// Throttled at once, nothing is validated: the call fails, saying to wait.
	client = validClient(t, 3)
	client.hook = func(context.Context, string) (error, bool) {
		return daemonProblemError{status: http.StatusTooManyRequests, detail: "rate limit exceeded"}, true
	}
	if _, err := New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{}); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("got %v, want the 429 told", err)
	}
}

func TestValidateObjectConfigsAnswersWhatTheDeadlineLeft(t *testing.T) {
	saved := configValidationsDeadline
	configValidationsDeadline = 200 * time.Millisecond
	t.Cleanup(func() { configValidationsDeadline = saved })

	client := validClient(t, 6)
	slow := endpointOf(t, "app/svc/s003", "n1")
	client.hook = func(ctx context.Context, path string) (error, bool) {
		if path != slow {
			return nil, false
		}
		<-ctx.Done()
		return fmt.Errorf("get: %w", ctx.Err()), true
	}
	result, err := New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{})
	if err != nil {
		t.Fatalf("validate object configs: %v", err)
	}
	if result.Validated != 3 || result.ValidObjects != 3 || !result.Truncated || result.NextCursor != "app/svc/s002" || result.Stopped != "deadline" {
		t.Fatalf("got validated %d, %d valid, truncated %v, cursor %q, stopped %q, want the 3 before the slow one",
			result.Validated, result.ValidObjects, result.Truncated, result.NextCursor, result.Stopped)
	}

	// A cancellation of the call is its error.
	ctx, cancel := context.WithCancel(context.Background())
	client = validClient(t, 6)
	client.hook = func(hookCtx context.Context, path string) (error, bool) {
		cancel()
		<-hookCtx.Done()
		return hookCtx.Err(), true
	}
	if _, err := New(client).ValidateObjectConfigs(ctx, ValidateObjectConfigsOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the cancellation", err)
	}
}

func TestValidateObjectConfigsCountsANodeNotAnswering(t *testing.T) {
	items := []string{objectItem("app/svc/up", []string{"n1"}, []string{"n1"})}
	payloads := map[string]string{endpointOf(t, "app/svc/up", "n1"): validationPayload("app/svc/up", "n1")}
	for i := range 10 {
		items = append(items, objectItem(fmt.Sprintf("app/svc/z%02d", i), []string{"n2"}, []string{"n2"}))
	}
	client := &validationsClient{objects: objectsPayload(items...), payloads: payloads}
	var mu sync.Mutex
	sent := 0
	client.hook = func(_ context.Context, path string) (error, bool) {
		if !strings.Contains(path, "/node/name/n2/") {
			return nil, false
		}
		mu.Lock()
		sent++
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		// The timeout of one request, not of the call.
		return fmt.Errorf("request OpenSVC daemon %s: %w", path, &url.Error{Op: "Get", URL: "https://n2:1215" + path, Err: fmt.Errorf("%w (Client.Timeout exceeded while awaiting headers)", context.DeadlineExceeded)}), true
	}
	result, err := New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{})
	if err != nil {
		t.Fatalf("validate object configs: %v", err)
	}
	if result.Validated != 11 || result.ValidObjects != 1 || result.FailedObjects != 10 || result.Truncated {
		t.Fatalf("got validated %d, %d valid, %d failed, truncated %v", result.Validated, result.ValidObjects, result.FailedObjects, result.Truncated)
	}
	if sent > maxConfigValidationNodeFailures+configValidationParallel-1 {
		t.Errorf("got %d requests to the node not answering, want the others not sent", sent)
	}
	total := 0
	for _, failure := range result.Failed {
		total += failure.Objects
		if !strings.HasPrefix(failure.Reason, "on n2: ") || strings.Contains(failure.Reason, "app/svc/") {
			t.Errorf("got reason %q, want the node, not the object", failure.Reason)
		}
	}
	if total != 10 || len(result.Failed) != 2 || !strings.Contains(result.Failed[0].Reason+result.Failed[1].Reason, "Client.Timeout") {
		t.Errorf("got %+v, want the 10 grouped", result.Failed)
	}
}

func TestValidateObjectConfigsFailsOnTheToken(t *testing.T) {
	for name, answer := range map[string]error{
		"token refused": daemonProblemError{status: http.StatusUnauthorized, detail: "token expired"},
		"incompatible":  &client.IncompatibleError{Method: "GET", Path: "/api/node/name/n1/...", Reason: "the daemon knows no such route"},
		"all failed":    daemonProblemError{status: http.StatusInternalServerError, detail: "boom"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := validClient(t, 3)
			fake.hook = func(context.Context, string) (error, bool) { return answer, true }
			_, err := New(fake).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{})
			if err == nil {
				t.Fatal("got no error, want the call failed rather than nothing to tell")
			}
			if name == "incompatible" && !errors.Is(err, client.ErrIncompatible) {
				t.Errorf("got %v, want daemon_incompatible", err)
			}
		})
	}
}

func TestValidateObjectConfigsBoundsTheRefusedAndTheParallelRequests(t *testing.T) {
	client := validClient(t, maxConfigValidationsNamed+5)
	client.hook = func(context.Context, string) (error, bool) {
		time.Sleep(2 * time.Millisecond)
		return daemonProblemError{status: http.StatusForbidden, detail: "the administrators of namespace app and root only"}, true
	}
	result, err := New(client).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{})
	if err != nil {
		t.Fatalf("validate object configs: %v", err)
	}
	if result.RefusedObjects != maxConfigValidationsNamed+5 || len(result.Refused) != maxConfigValidationsNamed {
		t.Errorf("got %d refused, %d named", result.RefusedObjects, len(result.Refused))
	}
	if client.peak > configValidationParallel || client.peak == 0 {
		t.Errorf("got %d validations at once, want at most %d", client.peak, configValidationParallel)
	}
}

func TestValidateObjectConfigsTellsAnEmptySelectionAndATerm(t *testing.T) {
	fake := &validationsClient{objects: objectsPayload()}
	result, err := New(fake).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{Selector: "*/svc/*+app=web"})
	if err != nil {
		t.Fatalf("validate object configs: %v", err)
	}
	if result.TermNote == "" || result.Note == "" || result.Total != 0 || result.Objects == nil {
		t.Errorf("got %+v, want the notes of an empty selection and of a term, an empty list", result)
	}
	for _, objects := range []string{`{"kind":"ObjectList"}`, `{"kind":"Other","items":[]}`} {
		fake := &validationsClient{objects: objects}
		if _, err := New(fake).ValidateObjectConfigs(context.Background(), ValidateObjectConfigsOptions{}); !errors.Is(err, client.ErrIncompatible) {
			t.Errorf("%s: got %v, want daemon_incompatible", objects, err)
		}
	}
}
