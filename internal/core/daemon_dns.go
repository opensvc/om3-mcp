package core

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

const (
	defaultListDNSRecordsLimit = 100
	maxListDNSRecordsLimit     = 200
	maxDNSRecordItems          = 50000
	maxDNSRecordTextRunes      = 1024
	maxDNSNameLength           = 255
)

var dnsRecordTypes = map[string]bool{"A": true, "AAAA": true, "PTR": true, "SRV": true, "SOA": true, "NS": true}

type ListDNSRecordsOptions struct {
	Node    string
	Name    string
	Type    string
	Content string
	Object  string
	Limit   int
	Cursor  string
}

type DNSRecordList struct {
	Provenance    Provenance  `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node          string      `json:"node" jsonschema:"the exact requested OpenSVC node whose daemon zone was read"`
	NameFilter    string      `json:"name_filter,omitempty" jsonschema:"the optional exact record name filter, with its final dot"`
	TypeFilter    string      `json:"type_filter,omitempty" jsonschema:"the optional record type filter"`
	ContentFilter string      `json:"content_filter,omitempty" jsonschema:"the optional exact record content filter"`
	ObjectFilter  string      `json:"object_filter,omitempty" jsonschema:"the optional object path filter"`
	ReportedTotal int         `json:"reported_total" jsonschema:"the number of records in the zone before filtering"`
	Total         int         `json:"total" jsonschema:"the number of records matching the filters"`
	Count         int         `json:"count" jsonschema:"the number of records returned in this page"`
	Records       []DNSRecord `json:"records" jsonschema:"the zone records sorted by name, type and content"`
	NextCursor    string      `json:"next_cursor,omitempty" jsonschema:"the opaque cursor to pass with the same node and filters for the next page"`
	Truncated     bool        `json:"truncated" jsonschema:"whether matching records remain after this page"`
}

type DNSRecord struct {
	Name    string `json:"name" jsonschema:"the fully qualified record name, with its final dot"`
	Type    string `json:"type" jsonschema:"the record type, such as A, AAAA, PTR, SRV, SOA or NS"`
	TTL     int    `json:"ttl" jsonschema:"the record time to live in seconds"`
	Content string `json:"content" jsonschema:"the record content: an address, a name, or the SRV and SOA fields"`
}

type daemonDNSRecord struct {
	Name    string `json:"qname"`
	Type    string `json:"qtype"`
	TTL     int    `json:"ttl"`
	Content string `json:"content"`
}

type dnsRecordEntry struct {
	record DNSRecord
	key    string
}

// ListDNSRecords reads the cluster DNS zone one daemon serves. The daemon
// builds it from the instance status of the cluster; the MCP reports it as is.
func (s *Service) ListDNSRecords(ctx context.Context, options ListDNSRecordsOptions) (DNSRecordList, error) {
	node := options.Node
	if err := validateNodeTarget(node); err != nil {
		return DNSRecordList{}, err
	}
	name, err := validateDNSName(options.Name)
	if err != nil {
		return DNSRecordList{}, err
	}
	recordType := strings.ToUpper(options.Type)
	if recordType != "" && !dnsRecordTypes[recordType] {
		return DNSRecordList{}, fmt.Errorf("DNS record type must be one of A, AAAA, PTR, SRV, SOA or NS")
	}
	content := options.Content
	if content != "" && (len(content) > maxDNSRecordTextRunes || strings.TrimSpace(content) != content || containsControl(content)) {
		return DNSRecordList{}, fmt.Errorf("DNS record content must be one exact value of at most %d characters", maxDNSRecordTextRunes)
	}
	content = canonicalDNSContent(content)
	var object *ClusterObjectReference
	if options.Object != "" {
		if strings.ContainsAny(options.Object, "*?[],\\") {
			return DNSRecordList{}, fmt.Errorf("object must be one exact OpenSVC object path, without wildcard or selector")
		}
		reference, err := validateExactObjectPath(options.Object)
		if err != nil {
			return DNSRecordList{}, err
		}
		object = &reference
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultListDNSRecordsLimit
	}
	if limit < 1 || limit > maxListDNSRecordsLimit {
		return DNSRecordList{}, fmt.Errorf("DNS record list limit must be between 1 and %d", maxListDNSRecordsLimit)
	}
	cursorKey, err := decodeDNSRecordCursor(options.Cursor)
	if err != nil {
		return DNSRecordList{}, err
	}

	var response daemonItems[daemonDNSRecord]
	if err := s.client.GetJSON(ctx, fmt.Sprintf("/api/node/name/%s/daemon/dns/dump", node), url.Values{}, &response); err != nil {
		return DNSRecordList{}, fmt.Errorf("list DNS records: %w", err)
	}
	if len(response) > maxDNSRecordItems {
		return DNSRecordList{}, fmt.Errorf("list DNS records: zone contains %d records, limit is %d", len(response), maxDNSRecordItems)
	}

	matching := make([]dnsRecordEntry, 0, len(response))
	for index, raw := range response {
		if raw.Name == "" || raw.Type == "" {
			return DNSRecordList{}, fmt.Errorf("list DNS records: record %d has no name or type", index)
		}
		record := DNSRecord{
			Name: boundedDNSText(raw.Name), Type: boundedDNSText(raw.Type), TTL: raw.TTL, Content: boundedDNSText(raw.Content),
		}
		if name != "" && !strings.EqualFold(record.Name, name) || recordType != "" && record.Type != recordType ||
			content != "" && canonicalDNSContent(record.Content) != content || object != nil && !dnsRecordNamesObject(record, *object) {
			continue
		}
		matching = append(matching, dnsRecordEntry{record: record, key: record.Name + "\x00" + record.Type + "\x00" + record.Content})
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].key < matching[j].key })
	// Records repeated by the daemon, such as one SOA per nameserver, are
	// listed once.
	unique := matching[:0]
	for _, entry := range matching {
		if len(unique) == 0 || unique[len(unique)-1].key != entry.key {
			unique = append(unique, entry)
		}
	}
	start := sort.Search(len(unique), func(i int) bool { return unique[i].key > cursorKey })
	end := min(start+limit, len(unique))
	page := make([]DNSRecord, 0, end-start)
	for _, entry := range unique[start:end] {
		page = append(page, entry.record)
	}
	result := DNSRecordList{
		Node:          node,
		NameFilter:    name,
		TypeFilter:    recordType,
		ContentFilter: content,
		ObjectFilter:  options.Object,
		ReportedTotal: len(response),
		Total:         len(unique),
		Count:         len(page),
		Records:       page,
		Truncated:     end < len(unique),
	}
	if result.Truncated {
		result.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(unique[end-1].key))
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

// dnsRecordNamesObject tells whether a record is one of the names the daemon
// publishes for an object: <name>.<namespace>.<kind>. starts the object
// name, after a resource label for a resource name, or after the
// _<port>._<network> labels of a service record. A reverse record names the
// object in its content.
func dnsRecordNamesObject(record DNSRecord, object ClusterObjectReference) bool {
	name := record.Name
	if record.Type == "PTR" {
		name = record.Content
	}
	labels := strings.Split(strings.TrimSuffix(strings.ToLower(name), "."), ".")
	want := []string{strings.ToLower(object.Name), strings.ToLower(object.Namespace), strings.ToLower(object.Kind)}
	for offset := 0; offset <= 2 && offset+len(want) < len(labels); offset++ {
		if offset == 2 && !(strings.HasPrefix(labels[0], "_") && strings.HasPrefix(labels[1], "_")) {
			continue
		}
		if labels[offset] == want[0] && labels[offset+1] == want[1] && labels[offset+2] == want[2] {
			return true
		}
	}
	return false
}

// validateDNSName accepts an empty name or one exact name, completed with its
// final dot as the zone writes it.
func validateDNSName(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > maxDNSNameLength || strings.TrimSpace(value) != value || strings.ContainsAny(value, "*? ") || containsControl(value) {
		return "", fmt.Errorf("DNS record name must be one exact name of at most %d characters, without wildcard", maxDNSNameLength)
	}
	if !strings.HasSuffix(value, ".") {
		value += "."
	}
	return value, nil
}

// canonicalDNSContent writes an address in its canonical form, so that an
// address matches whatever its spelling.
func canonicalDNSContent(value string) string {
	if address, err := netip.ParseAddr(value); err == nil {
		return address.String()
	}
	return value
}

func boundedDNSText(value string) string {
	bounded, _ := boundedRunes(value, maxDNSRecordTextRunes)
	return bounded
}

func decodeDNSRecordCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 4096 {
		return "", fmt.Errorf("DNS record cursor exceeds 4096 characters")
	}
	value, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("invalid DNS record cursor")
	}
	return string(value), nil
}
