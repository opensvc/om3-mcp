package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// dataClient answers the list of the keys, the read of a value, the PATCH
// of the data and the certificate routes, and records the writes.
type dataClient struct {
	keys      string
	blob      []byte
	mediaType string
	whole     bool
	patchErr  error
	postErr   error
	renewal   string
	patches   []any
	patchPath string
	query     url.Values
	posts     []string
	gets      []string
}

func (c *dataClient) GetJSON(_ context.Context, path string, _ url.Values, output any) error {
	recordRoute("GET", path)
	c.gets = append(c.gets, path)
	return json.Unmarshal([]byte(`{"kind":"DataKeyList","items":[`+c.keys+`]}`), output)
}

func (c *dataClient) GetBlob(_ context.Context, path string, query url.Values) ([]byte, string, bool, error) {
	recordRoute("GET", path)
	c.gets = append(c.gets, path+"?"+query.Encode())
	return c.blob, c.mediaType, c.whole, nil
}

func (c *dataClient) PatchJSONBody(_ context.Context, path string, query url.Values, input, _ any) (http.Header, error) {
	recordRoute("PATCH", path)
	c.patchPath, c.query = path, query
	data, _ := json.Marshal(input)
	var decoded []any
	_ = json.Unmarshal(data, &decoded)
	c.patches = decoded
	return http.Header{"Om-Last-Modified": {"2026-10-22T12:00:00Z"}}, c.patchErr
}

func (c *dataClient) PostJSON(_ context.Context, path string, _ url.Values, _, output any) error {
	recordRoute("POST", path)
	c.posts = append(c.posts, path)
	if c.postErr != nil {
		return c.postErr
	}
	if output == nil {
		return nil
	}
	return json.Unmarshal([]byte(c.renewal), output)
}

func dataKeyItem(object, name string, size int) string {
	data, _ := json.Marshal(map[string]any{"object": object, "node": "n1", "name": name, "size": size})
	return string(data)
}

func TestGetObjectDataKeyReadsACfgOnly(t *testing.T) {
	client := &dataClient{blob: []byte("listen 80;\n"), mediaType: "text/plain", whole: true}
	value, err := New(client).GetObjectDataKey(context.Background(), "test/cfg/nginx", "nginx.conf")
	if err != nil || value.Value != "listen 80;\n" || value.SizeBytes != 11 || value.Binary || value.Truncated || client.gets[0] != "/api/object/path/test/cfg/nginx/data/key?name=nginx.conf" {
		t.Fatalf("%+v %v %v", value, err, client.gets)
	}
	// A binary value is told by its size; a long text is cut.
	client = &dataClient{blob: []byte{0xff, 0, 1}, mediaType: "application/octet-stream", whole: true}
	if value, err := New(client).GetObjectDataKey(context.Background(), "test/cfg/nginx", "logo.png"); err != nil || !value.Binary || value.Value != "" || value.SizeBytes != 3 {
		t.Fatalf("binary: %+v %v", value, err)
	}
	client = &dataClient{blob: []byte(strings.Repeat("é", 40000)), mediaType: "text/plain", whole: true}
	if value, err := New(client).GetObjectDataKey(context.Background(), "test/cfg/nginx", "big"); err != nil || !value.Truncated || len(value.Value) != maxObjectDataValueBytes || value.SizeBytes != 80000 {
		t.Fatalf("long: %d %v %v", len(value.Value), value.Truncated, err)
	}
	// The daemon answers an object no node holds with an empty 200.
	client = &dataClient{whole: true}
	if _, err := New(client).GetObjectDataKey(context.Background(), "test/cfg/gone", "k"); err == nil || !strings.Contains(err.Error(), "object not found") {
		t.Fatalf("gone: %v", err)
	}
	for _, path := range []string{"test/sec/db", "test/usr/jane", "test/svc/web", "test/cfg/*"} {
		client := &dataClient{}
		if _, err := New(client).GetObjectDataKey(context.Background(), path, "k"); err == nil || len(client.gets) != 0 {
			t.Fatalf("%s read: %v", path, err)
		}
	}
}

func TestSetObjectDataKeySendsOneChange(t *testing.T) {
	client := &dataClient{keys: dataKeyItem("test/sec/db", "password", 44)}
	value := "s3cret"
	result, err := New(client).SetObjectDataKey(context.Background(), SetObjectDataKeyOptions{Path: "test/sec/db", Name: "password", Value: &value, WaitSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	if client.patchPath != "/api/object/path/test/sec/db/data" || client.query.Get("wait") != "5s" || len(client.patches) != 1 {
		t.Fatalf("patch = %s %v %v", client.patchPath, client.query, client.patches)
	}
	if patch := client.patches[0].(map[string]any); patch["action"] != "change" || patch["name"] != "password" || patch["string"] != "s3cret" || patch["bytes"] != nil {
		t.Fatalf("patch = %v", patch)
	}
	if !result.Existed || result.Outcome != "set" || result.SizeBytes != 6 || result.LastModified != "2026-10-22T12:00:00Z" {
		t.Fatalf("result = %+v", result)
	}
	if out, _ := json.Marshal(result); strings.Contains(string(out), "s3cret") {
		t.Fatalf("the value was returned: %s", out)
	}
	// Bytes are sent as the daemon takes them, in base64.
	client = &dataClient{}
	encoded := "/wAB"
	result, err = New(client).SetObjectDataKey(context.Background(), SetObjectDataKeyOptions{Path: "test/cfg/web", Name: "logo", ValueBase64: &encoded})
	if patch := client.patches[0].(map[string]any); err != nil || patch["bytes"] != "/wAB" || patch["string"] != nil || result.Existed || result.SizeBytes != 3 {
		t.Fatalf("bytes: %v %+v %v", patch, result, err)
	}
	// A wait that expired is no failure of the write.
	client = &dataClient{patchErr: daemonProblemError{http.StatusRequestTimeout, "the keys of test/cfg/web are committed as of 2026-10-22T12:00:01Z, and have not reached n2, n3 before the wait expired"}}
	result, err = New(client).SetObjectDataKey(context.Background(), SetObjectDataKeyOptions{Path: "test/cfg/web", Name: "k", Value: &value, WaitSeconds: 5})
	if err != nil || strings.Join(result.PropagationPending, ",") != "n2,n3" {
		t.Fatalf("propagation: %+v %v", result, err)
	}
	// The refusal of the daemon is given as it says it.
	client = &dataClient{patchErr: daemonProblemError{http.StatusForbidden, "writing the keys of a user requires holding its grants, missing admin:prod"}}
	if _, err := New(client).SetObjectDataKey(context.Background(), SetObjectDataKeyOptions{Path: "system/usr/jane", Name: "password", Value: &value}); err == nil || !strings.Contains(err.Error(), "requires holding its grants") {
		t.Fatalf("refused: %v", err)
	}
	big := strings.Repeat("x", maxObjectDataValueBytes+1)
	bad := "not base64!"
	for label, options := range map[string]SetObjectDataKeyOptions{
		"no value":   {Path: "test/cfg/web", Name: "k"},
		"two values": {Path: "test/cfg/web", Name: "k", Value: &value, ValueBase64: &encoded},
		"too big":    {Path: "test/cfg/web", Name: "k", Value: &big},
		"bad base64": {Path: "test/cfg/web", Name: "k", ValueBase64: &bad},
		"kind":       {Path: "test/svc/web", Name: "k", Value: &value},
		"name":       {Path: "test/cfg/web", Name: " k", Value: &value},
		"wait":       {Path: "test/cfg/web", Name: "k", Value: &value, WaitSeconds: 121},
	} {
		client := &dataClient{}
		if _, err := New(client).SetObjectDataKey(context.Background(), options); err == nil || client.patchPath != "" {
			t.Fatalf("%s accepted", label)
		}
	}
}

func TestDeleteObjectDataKeyTellsWhetherItExisted(t *testing.T) {
	client := &dataClient{keys: dataKeyItem("test/cfg/web", "other", 3)}
	result, err := New(client).DeleteObjectDataKey(context.Background(), DeleteObjectDataKeyOptions{Path: "test/cfg/web", Name: "k"})
	if patch := client.patches[0].(map[string]any); err != nil || patch["action"] != "remove" || patch["name"] != "k" || result.Existed || result.Outcome != "deleted" {
		t.Fatalf("%v %+v %v", patch, result, err)
	}
}

func TestObjectCertificates(t *testing.T) {
	client := &dataClient{keys: strings.Join([]string{
		dataKeyItem("test/sec/web", "private_key", 2000), dataKeyItem("test/sec/web", "certificate", 1500),
		dataKeyItem("test/sec/web", "certificate_chain", 1500), dataKeyItem("test/sec/web", "fullpem", 3500),
		dataKeyItem("test/sec/web", "serial_number", 40), dataKeyItem("test/sec/web", "password", 30),
	}, ",")}
	created, err := New(client).CreateObjectCertificate(context.Background(), "test/sec/web")
	if err != nil || client.posts[0] != "/api/object/path/test/sec/web/certificate/create" || len(created.Keys) != 5 {
		t.Fatalf("%+v %v %v", created, err, client.posts)
	}
	client = &dataClient{renewal: `{"renewed":false,"reason":"not due before 2026-12-01","domains":["web.example.com"],"not_after":"2027-01-01T00:00:00Z"}`}
	renewed, err := New(client).RenewObjectCertificate(context.Background(), "test/sec/web")
	if err != nil || renewed.Renewed || renewed.Reason == "" || renewed.Domains[0] != "web.example.com" || client.posts[0] != "/api/object/path/test/sec/web/certificate/renew" {
		t.Fatalf("%+v %v", renewed, err)
	}
	client = &dataClient{postErr: daemonProblemError{http.StatusConflict, "the certificate was not issued by this sec"}}
	if _, err := New(client).RenewObjectCertificate(context.Background(), "test/sec/web"); err == nil || !strings.Contains(err.Error(), "not issued") {
		t.Fatalf("conflict: %v", err)
	}
	for _, path := range []string{"test/cfg/web", "test/usr/jane", "test/sec/*"} {
		client := &dataClient{}
		if _, err := New(client).CreateObjectCertificate(context.Background(), path); err == nil || len(client.posts) != 0 {
			t.Fatalf("%s accepted", path)
		}
	}
}

func TestGetObjectDataKeyTellsTextByItsBytes(t *testing.T) {
	// The value a peer holds comes as application/json.
	client := &dataClient{blob: []byte("listen 80"), mediaType: "application/json", whole: true}
	if value, err := New(client).GetObjectDataKey(context.Background(), "test/cfg/web", "app.conf"); err != nil || value.Binary || value.Value != "listen 80" {
		t.Fatalf("relayed: %+v %v", value, err)
	}
	// A text cut by the bound of the read, inside a character, stays text.
	cut := []byte(strings.Repeat("é", 10))
	cut = cut[:len(cut)-1]
	client = &dataClient{blob: cut, mediaType: "text/plain", whole: false}
	if value, err := New(client).GetObjectDataKey(context.Background(), "test/cfg/web", "big"); err != nil || value.Binary || !value.Truncated || value.Value != strings.Repeat("é", 9) {
		t.Fatalf("cut: %+v %v", value, err)
	}
	// The 408 of the data is read in either wording.
	value := "v"
	client = &dataClient{patchErr: daemonProblemError{http.StatusRequestTimeout, "the keys of test/cfg/web are committed as of 2026-10-22T12:00:01Z, and has not reached n2 before the wait expired"}}
	if result, err := New(client).SetObjectDataKey(context.Background(), SetObjectDataKeyOptions{Path: "test/cfg/web", Name: "k", Value: &value, WaitSeconds: 5}); err != nil || result.PropagationPending[0] != "n2" {
		t.Fatalf("has not reached: %+v %v", result, err)
	}
}
