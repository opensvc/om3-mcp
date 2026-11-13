package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDaemonItemsDecodesBothShapes(t *testing.T) {
	for name, test := range map[string]struct {
		payload string
		want    []string
	}{
		"a bare array, before om3 rc62":   {payload: `["a/svc/x", "b/svc/y"]`, want: []string{"a/svc/x", "b/svc/y"}},
		"a list, since om3 rc62":          {payload: `{"kind":"ObjectPathList","items":["a/svc/x","b/svc/y"]}`, want: []string{"a/svc/x", "b/svc/y"}},
		"a list with no item":             {payload: `{"kind":"ObjectPathList","items":[]}`, want: []string{}},
		"a list with null items, as none": {payload: `{"kind":"ObjectPathList","items":null}`, want: nil},
		"a list without items, as none":   {payload: `{"kind":"ObjectPathList"}`, want: nil},
		"a bare array, spaces before it":  {payload: " \n[\"a/svc/x\"]", want: []string{"a/svc/x"}},
	} {
		t.Run(name, func(t *testing.T) {
			var got daemonItems[string]
			if err := json.Unmarshal([]byte(test.payload), &got); err != nil {
				t.Fatalf("decode %s: %v", test.payload, err)
			}
			if !reflect.DeepEqual([]string(got), test.want) {
				t.Errorf("got %#v, want %#v", []string(got), test.want)
			}
		})
	}
}

func TestDaemonItemsRefusesAnotherShape(t *testing.T) {
	for _, payload := range []string{`"a/svc/x"`, `{"items":"a/svc/x"}`, `[1, 2]`} {
		var got daemonItems[string]
		if err := json.Unmarshal([]byte(payload), &got); err == nil {
			t.Errorf("decoded %s as %#v, want an error", payload, []string(got))
		}
	}
}
