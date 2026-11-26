package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/opensvc/om3-mcp/internal/client"
)

const validateEndpoint = "/api/node/name/node-a/instance/path/data/vol/lake/config/validate"

// objectStatusPayload is the status of data/vol/lake, its scope and the
// nodes holding its configuration given.
func objectStatusPayload(scope, instances string) string {
	return `{"kind":"ObjectList","items":[{"kind":"ObjectItem","meta":{"object":"data/vol/lake"},
		"data":{"scope":` + scope + `,"updated_at":"2026-11-26T10:00:00Z","instances":` + instances + `}}]}`
}

// alertsPayload is a ConfigAlertList of data/vol/lake on node-a.
func alertsPayload(items ...string) string {
	return `{"kind":"ConfigAlertList","items":[` + strings.Join(items, ",") + `]}`
}

func alert(level, key, comment string) string {
	return fmt.Sprintf(`{"node":"node-a","path":"data/vol/lake","level":%q,"kind":"unknown keyword","key":%q,"driver":"disk.loop","comment":%q}`, level, key, comment)
}

func TestValidateObjectConfigOnTheFirstNodeHoldingTheConfiguration(t *testing.T) {
	client := &readsClient{payloads: map[string]string{
		// node-a is first of the scope, but node-b alone holds the configuration.
		"/api/object": objectStatusPayload(`["node-a","node-b"]`, `{"node-b":{}}`),
		"/api/node/name/node-b/instance/path/data/vol/lake/config/validate": `{"kind":"ConfigAlertList","items":[
			{"node":"node-b","path":"data/vol/lake","level":"warning","kind":"unknown keyword","key":"disk#0.name","driver":"disk.loop","comment":"keyword does not exist: \"disk#0.name\""},
			{"node":"node-b","path":"data/vol/lake","level":"error","kind":"unsupported value","key":"orchestrate","driver":"","comment":"sometimes"},
			{"node":"node-b","path":"data/vol/lake","level":"warning","kind":"unknown keyword","key":"a","driver":"","comment":"keyword does not exist: \"DEFAULT.a\""}
		]}`,
	}}

	result, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: " data/vol/lake "})
	if err != nil {
		t.Fatalf("validate object config: %v", err)
	}
	if result.Node != "node-b" || !result.NodeChosen || result.Object.Path != "data/vol/lake" {
		t.Fatalf("got node %q chosen %v object %q, want node-b, chosen, data/vol/lake", result.Node, result.NodeChosen, result.Object.Path)
	}
	if result.Level != "error" || result.ErrorCount != 1 || result.WarningCount != 2 || result.Truncated || result.CommentsTruncated != 0 {
		t.Fatalf("got %+v, want an error and two warnings, nothing truncated", result)
	}
	want := []ConfigValidationAlert{
		{Level: "error", Kind: "unsupported value", Key: "orchestrate", Comment: "sometimes"},
		{Level: "warning", Kind: "unknown keyword", Key: "a", Comment: `keyword does not exist: "DEFAULT.a"`},
		{Level: "warning", Kind: "unknown keyword", Key: "disk#0.name", Driver: "disk.loop", Comment: `keyword does not exist: "disk#0.name"`},
	}
	if !reflect.DeepEqual(result.Alerts, want) {
		t.Errorf("got alerts %#v, want the errors first, then by key %#v", result.Alerts, want)
	}
	if result.Provenance.ObservedAt == "" {
		t.Errorf("got no provenance")
	}
}

func TestValidateObjectConfigOnTheFirstNodeOfTheScopeWhenNoneHoldsIt(t *testing.T) {
	client := &readsClient{payloads: map[string]string{
		"/api/object":    objectStatusPayload(`["node-b","node-a"]`, `{}`),
		validateEndpoint: alertsPayload(),
	}}

	result, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: "data/vol/lake"})
	if err != nil {
		t.Fatalf("validate object config: %v", err)
	}
	if result.Node != "node-a" || !result.NodeChosen || result.Level != "valid" {
		t.Fatalf("got %+v, want valid on node-a, chosen", result)
	}
}

func TestValidateObjectConfigOnTheNodeGiven(t *testing.T) {
	client := &readsClient{payloads: map[string]string{
		"/api/node/name/node-b/instance/path/data/cfg/settings/config/validate": `{"kind":"ConfigAlertList","items":[]}`,
	}}

	result, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: "data/cfg/settings", Node: "node-b"})
	if err != nil {
		t.Fatalf("validate object config: %v", err)
	}
	if result.Level != "valid" || result.NodeChosen || result.Node != "node-b" || len(result.Alerts) != 0 || result.Alerts == nil {
		t.Fatalf("got %+v, want valid on node-b, no alert, an empty list", result)
	}
	if len(client.paths) != 1 {
		t.Errorf("got reads %v, want the validation only: the node is given", client.paths)
	}
}

func TestValidateObjectConfigNamesTheObjectsOfTheRootNamespaceAndTheCluster(t *testing.T) {
	for path, endpoint := range map[string]string{
		"with-cfg-error": "/api/node/name/node-a/instance/path/root/svc/with-cfg-error/config/validate",
		"cluster":        "/api/node/name/node-a/instance/path/root/ccfg/cluster/config/validate",
		"data/":          "/api/node/name/node-a/instance/path/data/nscfg/namespace/config/validate",
	} {
		client := &readsClient{payloads: map[string]string{endpoint: `{"kind":"ConfigAlertList","items":[]}`}}
		if _, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: path, Node: "node-a"}); err != nil {
			t.Errorf("validate %s: %v", path, err)
		}
	}
}

func TestValidateObjectConfigLeavesTheDaemonTellWhyItRefuses(t *testing.T) {
	client := &readsClient{errs: map[string]error{
		validateEndpoint: daemonProblemError{status: http.StatusForbidden, detail: "the validation of data/vol/lake may quote its secrets: the administrators of namespace data and root only"},
	}}

	_, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: "data/vol/lake", Node: "node-a"})
	if err == nil || strings.Count(err.Error(), "root only") != 1 {
		t.Fatalf("got error %v, want the reason of the daemon, once", err)
	}
	var problem daemonProblem
	if !errors.As(err, &problem) || problem.HTTPStatus() != http.StatusForbidden {
		t.Errorf("got error %v, want the problem of the daemon kept", err)
	}
}

func TestValidateObjectConfigTellsTheClusterConfigurationIsForRootAndJoin(t *testing.T) {
	client := &readsClient{errs: map[string]error{
		"/api/node/name/node-a/instance/path/root/ccfg/cluster/config/validate": daemonProblemError{status: http.StatusForbidden, detail: "forbidden"},
	}}

	_, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: "cluster", Node: "node-a"})
	if err == nil || !strings.Contains(err.Error(), "for root and join only") || strings.Contains(err.Error(), "administrators of namespace") {
		t.Fatalf("got error %v, want root and join named, no administrator of a namespace", err)
	}
}

func TestValidateObjectConfigExplainsAnUnknownNode(t *testing.T) {
	client := &readsClient{errs: map[string]error{
		validateEndpoint: &client.APIError{StatusCode: http.StatusNotFound, Title: unknownNodeTitle},
	}}

	_, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: "data/vol/lake", Node: "node-a"})
	if err == nil || !strings.Contains(err.Error(), "get_cluster_status lists the nodes") {
		t.Fatalf("got error %v, want the unknown node explained", err)
	}
}

func TestValidateObjectConfigRefusesWhatItCannotTarget(t *testing.T) {
	for _, options := range []ValidateObjectConfigOptions{
		{Path: "data/vol/*"},
		{Path: "data/vol/lake", Node: "_"},
		{Path: "data/vol/lake", Node: "node a"},
	} {
		client := &readsClient{}
		if _, err := New(client).ValidateObjectConfig(context.Background(), options); err == nil {
			t.Errorf("validated %+v, want a refusal", options)
		}
		if len(client.paths) != 0 {
			t.Errorf("read %v for %+v, want nothing read", client.paths, options)
		}
	}
}

func TestValidateObjectConfigAsksANodeWhenItCannotChooseOne(t *testing.T) {
	for name, client := range map[string]*readsClient{
		"no node":           {payloads: map[string]string{"/api/object": objectStatusPayload(`[]`, `{}`)}},
		"no status to read": {errs: map[string]error{"/api/object": daemonProblemError{status: http.StatusInternalServerError, detail: "down"}}},
	} {
		_, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: "data/vol/lake"})
		if err == nil || !strings.Contains(err.Error(), "give node") {
			t.Errorf("%s: got error %v, want to be asked a node", name, err)
		}
	}
}

func TestValidateObjectConfigRefusesAnAnswerOfAnotherShape(t *testing.T) {
	for name, payload := range map[string]string{
		"empty":         `{}`,
		"no items":      `{"kind":"ConfigAlertList","items":null}`,
		"another kind":  `{"kind":"KeywordList","items":[]}`,
		"unknown level": alertsPayload(`{"node":"node-a","path":"data/vol/lake","level":"info","kind":"x","key":"k"}`),
		"another node":  alertsPayload(`{"node":"node-b","path":"data/vol/lake","level":"warning","kind":"x","key":"k"}`),
		"another path":  alertsPayload(`{"node":"node-a","path":"data/vol/other","level":"warning","kind":"x","key":"k"}`),
	} {
		client := &readsClient{payloads: map[string]string{validateEndpoint: payload}}
		_, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: "data/vol/lake", Node: "node-a"})
		if err == nil {
			t.Errorf("%s: got no error, want the answer refused", name)
		}
	}
}

func TestValidateObjectConfigLeavesOutTheCommentsOfTheSecrets(t *testing.T) {
	for path, key := range map[string]string{
		"cluster":         "cluster.secret",
		"data/sec/vault":  "data.password",
		"system/usr/hugo": "data.password",
	} {
		reference, _ := validateExactObjectPath(path)
		endpoint := fmt.Sprintf("/api/node/name/node-a/instance/path/%s/%s/%s/config/validate", reference.Namespace, reference.Kind, reference.Name)
		payload := alertsPayload(fmt.Sprintf(`{"node":"node-a","path":%q,"level":"error","kind":"unsupported value","key":%q,"comment":"s3cr3t"}`, reference.Path, key))
		client := &readsClient{payloads: map[string]string{endpoint: payload}}
		result, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: path, Node: "node-a"})
		if err != nil {
			t.Fatalf("validate %s: %v", path, err)
		}
		if len(result.Alerts) != 1 || strings.Contains(result.Alerts[0].Comment, "s3cr3t") || result.Alerts[0].Key != key {
			t.Errorf("%s: got %#v, want the alert, its comment left out", path, result.Alerts)
		}
	}
}

func TestValidateObjectConfigBoundsItsAlerts(t *testing.T) {
	long := strings.Repeat("é", maxConfigValidationCommentBytes)
	// Five errors after the warnings: the bound keeps them, sorted first.
	items := make([]string, 0, maxConfigValidationAlerts+10)
	for i := range maxConfigValidationAlerts + 5 {
		items = append(items, alert("warning", fmt.Sprintf("k%03d", i), "short"))
	}
	for i := range 5 {
		items = append(items, alert("error", fmt.Sprintf("e%d", i), "short"))
	}
	client := &readsClient{payloads: map[string]string{validateEndpoint: alertsPayload(items...)}}
	result, err := New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: "data/vol/lake", Node: "node-a"})
	if err != nil {
		t.Fatalf("validate object config: %v", err)
	}
	if len(result.Alerts) != maxConfigValidationAlerts || !result.Truncated || result.WarningCount != maxConfigValidationAlerts+5 || result.ErrorCount != 5 {
		t.Fatalf("got %d alerts, truncated %v, %d warnings, %d errors", len(result.Alerts), result.Truncated, result.WarningCount, result.ErrorCount)
	}
	for _, kept := range result.Alerts[:5] {
		if kept.Level != "error" {
			t.Fatalf("got %+v first, want the errors kept first", result.Alerts[:5])
		}
	}

	// Long comments: each cut and counted, all of them bounded together.
	items = items[:0]
	for i := range 100 {
		items = append(items, alert("warning", fmt.Sprintf("k%03d", i), long))
	}
	client = &readsClient{payloads: map[string]string{validateEndpoint: alertsPayload(items...)}}
	result, err = New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: "data/vol/lake", Node: "node-a"})
	if err != nil {
		t.Fatalf("validate object config: %v", err)
	}
	if !result.Truncated || result.CommentsTruncated != 100 || len(result.Alerts) == 0 {
		t.Fatalf("got %d alerts, truncated %v, %d comments truncated, want the comments bounded together", len(result.Alerts), result.Truncated, result.CommentsTruncated)
	}
	size := 0
	for _, kept := range result.Alerts {
		size += len(kept.Comment)
		if len(kept.Comment) > maxConfigValidationCommentBytes || !strings.HasPrefix(kept.Comment, "é") {
			t.Fatalf("got a comment of %d bytes, want at most %d, cut on a character", len(kept.Comment), maxConfigValidationCommentBytes)
		}
	}
	if size > maxConfigValidationCommentsBytes {
		t.Errorf("got %d bytes of comments, want at most %d", size, maxConfigValidationCommentsBytes)
	}

	// A long key is cut too.
	client = &readsClient{payloads: map[string]string{validateEndpoint: alertsPayload(alert("warning", strings.Repeat("k", 1000), "x"))}}
	result, err = New(client).ValidateObjectConfig(context.Background(), ValidateObjectConfigOptions{Path: "data/vol/lake", Node: "node-a"})
	if err != nil || len(result.Alerts[0].Key) != maxConfigValidationFieldBytes {
		t.Fatalf("got %v, %+v, want the key cut at %d bytes", err, result.Alerts, maxConfigValidationFieldBytes)
	}
}
