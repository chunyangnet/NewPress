package aijobs

import (
	"encoding/json"
	"newpress.local/newpress/internal/plugin"
	"path/filepath"
	"testing"
)

func TestQueueRejectsRetiredPolicyFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.json")
	for _, raw := range []string{
		`{"Schema":1,"ControlVersion":1,"Rules":{"MemberOnly":true,"TaskCredits":75},"Grants":[]}`,
		`{"Schema":1,"ControlVersion":1,"Jobs":[{"ID":"retired-job","FreeTicket":true}]}`,
		`{"Schema":1,"Rules":{"UnexpectedField":true}}`,
	} {
		if err := plugin.WritePrivate(path, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := plugin.ReadPrivate[State](path, maxStateBytes); err == nil {
			t.Fatal("accepted retired queue policy")
		}
	}
}
