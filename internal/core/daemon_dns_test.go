package core

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

// The dev5 zone of a failover address up on node-a, plus a service record,
// another object, and the cluster records of two nameservers.
const dnsZonePayload = `[
	{"qname":"vip.system.svc.node-b.node.dev5.","qtype":"AAAA","ttl":60,"content":"2001:db8::100","domain_id":-1},
	{"qname":"0.vip.system.svc.node-b.node.dev5.","qtype":"AAAA","ttl":60,"content":"2001:db8::100","domain_id":-1},
	{"qname":"0.0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.","qtype":"PTR","ttl":60,"content":"0.vip.system.svc.node-b.node.dev5.","domain_id":-1},
	{"qname":"vip.system.svc.dev5.","qtype":"AAAA","ttl":60,"content":"2001:db8::100","domain_id":-1},
	{"qname":"0.vip.system.svc.dev5.","qtype":"AAAA","ttl":60,"content":"2001:db8::100","domain_id":-1},
	{"qname":"0.0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.","qtype":"PTR","ttl":60,"content":"0.vip.system.svc.dev5.","domain_id":-1},
	{"qname":"_443._tcp.web.lab.svc.dev5.","qtype":"SRV","ttl":60,"content":"10 100 8443 web.lab.svc.node-a.node.dev5.","domain_id":-1},
	{"qname":"web.lab.svc.dev5.","qtype":"A","ttl":60,"content":"10.22.0.5","domain_id":-1},
	{"qname":"vip.lab.svc.dev5.","qtype":"A","ttl":60,"content":"10.22.0.6","domain_id":-1},
	{"qname":"dev5.","qtype":"SOA","ttl":60,"content":"dns.dev5. contact@opensvc.com 1 7200 3600 432000 86400","domain_id":-1},
	{"qname":"dev5.","qtype":"SOA","ttl":60,"content":"dns.dev5. contact@opensvc.com 1 7200 3600 432000 86400","domain_id":-1},
	{"qname":"ns1.dev5.","qtype":"A","ttl":60,"content":"10.0.0.1","domain_id":-1},
	{"qname":"dev5.","qtype":"NS","ttl":3600,"content":"ns1.dev5.","domain_id":-1}
]`

func dnsRecordNames(list DNSRecordList) string {
	names := make([]string, 0, len(list.Records))
	for _, record := range list.Records {
		names = append(names, record.Type+" "+record.Name)
	}
	return strings.Join(names, ",")
}

func TestListDNSRecordsSortsDeduplicatesAndPaginates(t *testing.T) {
	client := &recordingJSONGetter{t: t, path: "/api/node/name/node-a/daemon/dns/dump", query: url.Values{}, payload: dnsZonePayload}
	service := New(client)
	first, err := service.ListDNSRecords(context.Background(), ListDNSRecordsOptions{Node: "node-a", Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if first.Node != "node-a" || first.ReportedTotal != 13 || first.Total != 12 || first.Count != 4 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("unexpected first page %+v", first)
	}
	if got := dnsRecordNames(first); got != "PTR 0.0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.,PTR 0.0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.,AAAA 0.vip.system.svc.dev5.,AAAA 0.vip.system.svc.node-b.node.dev5." {
		t.Fatalf("unexpected order %s", got)
	}
	if first.Records[2] != (DNSRecord{Name: "0.vip.system.svc.dev5.", Type: "AAAA", TTL: 60, Content: "2001:db8::100"}) {
		t.Fatalf("record not preserved: %+v", first.Records[2])
	}
	rest, err := service.ListDNSRecords(context.Background(), ListDNSRecordsOptions{Node: "node-a", Limit: 200, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if rest.Count != 8 || rest.Truncated || strings.Count(dnsRecordNames(rest), "SOA dev5.") != 1 {
		t.Fatalf("unexpected rest %s", dnsRecordNames(rest))
	}
}

func TestListDNSRecordsFilters(t *testing.T) {
	for _, tc := range []struct {
		options ListDNSRecordsOptions
		names   string
	}{
		{ListDNSRecordsOptions{Name: "VIP.system.svc.dev5"}, "AAAA vip.system.svc.dev5."},
		{ListDNSRecordsOptions{Type: "srv"}, "SRV _443._tcp.web.lab.svc.dev5."},
		{ListDNSRecordsOptions{Content: "2001:DB8:0::100", Name: "vip.system.svc.node-b.node.dev5."}, "AAAA vip.system.svc.node-b.node.dev5."},
		{ListDNSRecordsOptions{Content: "0.vip.system.svc.dev5."}, "PTR 0.0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa."},
		{ListDNSRecordsOptions{Object: "lab/svc/web"}, "SRV _443._tcp.web.lab.svc.dev5.,A web.lab.svc.dev5."},
		{ListDNSRecordsOptions{Object: "lab/svc/vip"}, "A vip.lab.svc.dev5."},
		{ListDNSRecordsOptions{Object: "system/svc/vip", Type: "PTR"}, "PTR 0.0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa.,PTR 0.0.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa."},
		{ListDNSRecordsOptions{Object: "lab/svc/absent"}, ""},
	} {
		tc.options.Node = "node-a"
		client := &recordingJSONGetter{t: t, path: "/api/node/name/node-a/daemon/dns/dump", query: url.Values{}, payload: dnsZonePayload}
		list, err := New(client).ListDNSRecords(context.Background(), tc.options)
		if err != nil {
			t.Fatal(err)
		}
		if got := dnsRecordNames(list); got != tc.names {
			t.Fatalf("options %+v gave %s", tc.options, got)
		}
	}
}

func TestListDNSRecordsMatchesEveryNameOfAnObject(t *testing.T) {
	client := &recordingJSONGetter{t: t, path: "/api/node/name/node-a/daemon/dns/dump", query: url.Values{}, payload: dnsZonePayload}
	list, err := New(client).ListDNSRecords(context.Background(), ListDNSRecordsOptions{Node: "node-a", Object: "system/svc/vip"})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 6 || strings.Contains(dnsRecordNames(list), "lab") {
		t.Fatalf("object names not matched: %s", dnsRecordNames(list))
	}
}

func TestListDNSRecordsRejectsInvalidInputBeforeTheDaemonCall(t *testing.T) {
	for _, options := range []ListDNSRecordsOptions{
		{}, {Node: "_"}, {Node: "node-a", Name: "*.dev5."}, {Node: "node-a", Name: " vip"}, {Node: "node-a", Type: "MX"},
		{Node: "node-a", Content: " 10.0.0.1"}, {Node: "node-a", Object: "lab/svc/*"}, {Node: "node-a", Object: "a/b/c/d"},
		{Node: "node-a", Limit: 201}, {Node: "node-a", Cursor: "not base64!"},
	} {
		client := &recordingJSONGetter{t: t}
		if _, err := New(client).ListDNSRecords(context.Background(), options); err == nil || client.calls != 0 {
			t.Fatalf("options %+v were accepted or reached the daemon", options)
		}
	}
}

func TestListDNSRecordsAcceptsAnEmptyZoneAndRejectsMalformedRecords(t *testing.T) {
	client := &recordingJSONGetter{t: t, path: "/api/node/name/node-a/daemon/dns/dump", query: url.Values{}, payload: `[]`}
	list, err := New(client).ListDNSRecords(context.Background(), ListDNSRecordsOptions{Node: "node-a"})
	if err != nil || list.Records == nil || list.Total != 0 {
		t.Fatalf("got %+v, %v", list, err)
	}
	client = &recordingJSONGetter{t: t, path: "/api/node/name/node-a/daemon/dns/dump", query: url.Values{}, payload: `[{"qname":"","qtype":"A"}]`}
	if _, err := New(client).ListDNSRecords(context.Background(), ListDNSRecordsOptions{Node: "node-a"}); err == nil {
		t.Fatal("a record without name was accepted")
	}
}

// Since om3 rc62 the daemon answers the zone as a list, {kind, items}: read the
// same as the bare array of the former daemons.
func TestListDNSRecordsReadsTheItemsOfAList(t *testing.T) {
	payload := `{"kind":"DNSRecordList","items":` + dnsZonePayload + `}`
	client := &recordingJSONGetter{t: t, path: "/api/node/name/node-a/daemon/dns/dump", query: url.Values{}, payload: payload}
	list, err := New(client).ListDNSRecords(context.Background(), ListDNSRecordsOptions{Node: "node-a", Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if list.ReportedTotal != 13 || list.Total != 12 || list.Count != 4 {
		t.Fatalf("unexpected page %+v", list)
	}
}
