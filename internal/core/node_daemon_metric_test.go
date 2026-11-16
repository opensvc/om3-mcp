package core

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type nodeDaemonMetricClient struct {
	t       *testing.T
	path    string
	query   url.Values
	payload string
	calls   int
}

func (f *nodeDaemonMetricClient) GetJSON(_ context.Context, path string, _ url.Values, _ any) error {
	recordRoute("GET", path)
	f.t.Helper()
	f.t.Fatal("unexpected JSON request")
	return nil
}

func (f *nodeDaemonMetricClient) GetText(_ context.Context, path string, query url.Values) ([]byte, error) {
	recordRoute("GET", path)
	f.t.Helper()
	f.calls++
	if path != f.path {
		f.t.Errorf("got path %q, want %q", path, f.path)
	}
	if !reflect.DeepEqual(query, f.query) {
		f.t.Errorf("got query %#v, want %#v", query, f.query)
	}
	return []byte(f.payload), nil
}

const nodeDaemonMetricFixture = `# HELP go_goroutines Number of goroutines.
# TYPE go_goroutines gauge
go_goroutines 42
# HELP opensvc_api_requests_total API requests.
# TYPE opensvc_api_requests_total counter
opensvc_api_requests_total{method="GET",code="200"} 12
opensvc_api_requests_total{method="POST",code="500"} 2
# HELP opensvc_api_request_duration_seconds API latency.
# TYPE opensvc_api_request_duration_seconds histogram
opensvc_api_request_duration_seconds_bucket{method="GET",le="0.1"} 2
opensvc_api_request_duration_seconds_bucket{method="GET",le="+Inf"} 3
opensvc_api_request_duration_seconds_sum{method="GET"} 0.25
opensvc_api_request_duration_seconds_count{method="GET"} 3
# HELP process_gc_seconds GC time.
# TYPE process_gc_seconds summary
process_gc_seconds{quantile="0.5"} 0.01
process_gc_seconds{quantile="0.9"} 0.02
process_gc_seconds_sum 0.03
process_gc_seconds_count 2
`

func TestGetNodeDaemonMetricsFiltersProjectsAndPaginates(t *testing.T) {
	client := &nodeDaemonMetricClient{
		t: t, path: "/api/node/name/node-a/metrics", query: url.Values{}, payload: nodeDaemonMetricFixture,
	}
	service := New(client)
	first, err := service.GetNodeDaemonMetrics(context.Background(), GetNodeDaemonMetricsOptions{
		Node: "node-a", Names: []string{"go_goroutines"}, Prefixes: []string{"opensvc_"}, Limit: 2,
	})
	if err != nil {
		t.Fatalf("get first metric page: %v", err)
	}
	if first.TargetNode != "node-a" || first.ReportedTotal != 4 || first.Total != 3 || first.SampleTotal != 4 || first.Count != 2 || first.ReturnedSampleCount != 2 || !first.Truncated {
		t.Fatalf("unexpected first page metadata: %#v", first)
	}
	if got := []string{first.Metrics[0].Name, first.Metrics[1].Name}; !reflect.DeepEqual(got, []string{"go_goroutines", "opensvc_api_request_duration_seconds"}) {
		t.Errorf("got metric order %#v", got)
	}
	if first.Metrics[0].Type != "gauge" || first.Metrics[0].Samples[0].Value != "42" {
		t.Errorf("unexpected gauge projection: %#v", first.Metrics[0])
	}
	histogram := first.Metrics[1].Samples[0]
	if histogram.Histogram == nil || histogram.Histogram.SampleCount != 3 || histogram.Histogram.SampleSum != "0.25" || len(histogram.Histogram.Buckets) != 2 || histogram.Histogram.Buckets[1].UpperBound != "+Inf" {
		t.Errorf("unexpected histogram projection: %#v", histogram)
	}
	if len(histogram.Labels) != 1 || histogram.Labels[0].Name != "method" || histogram.Labels[0].Value != "GET" {
		t.Errorf("unexpected histogram labels: %#v", histogram.Labels)
	}
	if first.NextCursor != "opensvc_api_request_duration_seconds" {
		t.Errorf("got next cursor %q", first.NextCursor)
	}

	second, err := service.GetNodeDaemonMetrics(context.Background(), GetNodeDaemonMetricsOptions{
		Node: "node-a", Names: []string{"go_goroutines"}, Prefixes: []string{"opensvc_"}, Limit: 2, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatalf("get second metric page: %v", err)
	}
	if second.Count != 1 || second.ReturnedSampleCount != 2 || second.Truncated || second.Metrics[0].Name != "opensvc_api_requests_total" {
		t.Errorf("unexpected second page: %#v", second)
	}
	if got := second.Metrics[0].Samples[0].Labels; len(got) != 2 || got[0].Name != "code" || got[1].Name != "method" {
		t.Errorf("labels are not sorted: %#v", got)
	}
	if client.calls != 2 {
		t.Errorf("got %d text calls, want 2", client.calls)
	}
}

func TestGetNodeDaemonMetricsProjectsSummary(t *testing.T) {
	client := &nodeDaemonMetricClient{
		t: t, path: "/api/node/name/node-a/metrics", query: url.Values{}, payload: nodeDaemonMetricFixture,
	}
	result, err := New(client).GetNodeDaemonMetrics(context.Background(), GetNodeDaemonMetricsOptions{Node: "node-a", Names: []string{"process_gc_seconds"}})
	if err != nil {
		t.Fatalf("get summary metric: %v", err)
	}
	if result.Count != 1 || result.Metrics[0].Type != "summary" || len(result.Metrics[0].Samples) != 1 {
		t.Fatalf("unexpected summary family: %#v", result)
	}
	summary := result.Metrics[0].Samples[0].Summary
	if summary == nil || summary.SampleCount != 2 || summary.SampleSum != "0.03" || len(summary.Quantiles) != 2 || summary.Quantiles[0].Quantile != "0.5" {
		t.Errorf("unexpected summary projection: %#v", summary)
	}
}

func TestGetNodeDaemonMetricsRejectsInvalidInputsBeforeCallingDaemon(t *testing.T) {
	tests := map[string]GetNodeDaemonMetricsOptions{
		"missing node":     {},
		"local alias":      {Node: "_"},
		"node selector":    {Node: "node*"},
		"invalid limit":    {Node: "node-a", Limit: maxNodeDaemonMetricLimit + 1},
		"empty name":       {Node: "node-a", Names: []string{""}},
		"spaced prefix":    {Node: "node-a", Prefixes: []string{"opensvc api"}},
		"too many filters": {Node: "node-a", Names: make([]string, maxNodeDaemonMetricFilters+1)},
		"invalid cursor":   {Node: "node-a", Cursor: "bad\ncursor"},
	}
	for name, options := range tests {
		t.Run(name, func(t *testing.T) {
			client := &nodeDaemonMetricClient{t: t}
			if _, err := New(client).GetNodeDaemonMetrics(context.Background(), options); err == nil {
				t.Fatal("expected validation error")
			}
			if client.calls != 0 {
				t.Errorf("daemon was called %d times", client.calls)
			}
		})
	}
}

func TestGetNodeDaemonMetricsRejectsMalformedOrOversizedData(t *testing.T) {
	tests := map[string]string{
		"invalid text":       "this is not prometheus text\n",
		"oversized response": strings.Repeat("x", maxNodeDaemonMetricPayloadBytes+1),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			client := &nodeDaemonMetricClient{t: t, path: "/api/node/name/node-a/metrics", query: url.Values{}, payload: payload}
			if _, err := New(client).GetNodeDaemonMetrics(context.Background(), GetNodeDaemonMetricsOptions{Node: "node-a"}); err == nil {
				t.Fatal("expected daemon data error")
			}
		})
	}
}

func TestGetNodeDaemonMetricsRejectsStaleCursor(t *testing.T) {
	client := &nodeDaemonMetricClient{t: t, path: "/api/node/name/node-a/metrics", query: url.Values{}, payload: nodeDaemonMetricFixture}
	_, err := New(client).GetNodeDaemonMetrics(context.Background(), GetNodeDaemonMetricsOptions{Node: "node-a", Cursor: "missing_metric"})
	if err == nil || !strings.Contains(err.Error(), "no longer present") {
		t.Fatalf("got stale cursor error %v", err)
	}
}

func TestGetNodeDaemonMetricsRequiresTextClient(t *testing.T) {
	client := &recordingJSONGetter{t: t}
	_, err := New(client).GetNodeDaemonMetrics(context.Background(), GetNodeDaemonMetricsOptions{Node: "node-a"})
	if err == nil || !strings.Contains(err.Error(), "does not support text") {
		t.Fatalf("got error %v", err)
	}
}

func TestGetNodeDaemonMetricsPreservesSpecialValues(t *testing.T) {
	payload := "# TYPE special gauge\nspecial NaN\n"
	client := &nodeDaemonMetricClient{t: t, path: "/api/node/name/node-a/metrics", query: url.Values{}, payload: payload}
	result, err := New(client).GetNodeDaemonMetrics(context.Background(), GetNodeDaemonMetricsOptions{Node: "node-a"})
	if err != nil {
		t.Fatalf("get special metric: %v", err)
	}
	if got := result.Metrics[0].Samples[0].Value; got != "NaN" {
		t.Errorf("got value %q, want NaN", got)
	}
}

var _ JSONGetter = (*nodeDaemonMetricClient)(nil)
var _ TextGetter = (*nodeDaemonMetricClient)(nil)
