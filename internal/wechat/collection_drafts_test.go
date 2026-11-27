package wechat

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectionDraftPolicyFileCompatibility(t *testing.T) {
	old, err := json.Marshal(NewState())
	if err != nil || strings.Contains(string(old), "collect_drafts") || Defaults().CollectDrafts {
		t.Fatal("new opt-in must default off and preserve legacy encoding", err)
	}
	legacy, err := Decode(old)
	if err != nil || legacy.Config.CollectDrafts {
		t.Fatal(legacy, err)
	}
	path := filepath.Join(t.TempDir(), "wechat.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: store}
	for _, enabled := range []bool{true, false} {
		config, err := ReadConfig(t.Context(), store)
		if err != nil {
			t.Fatal(err)
		}
		config.CollectDrafts = enabled
		config.Secret = strings.Repeat("s", 32)
		result, err := svc.SaveConfig(t.Context(), config, false)
		if err != nil || result.CollectDrafts != enabled || result.Secret != "" || !result.HasSecret {
			t.Fatal(result, err)
		}
		store, err = NewFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		svc.Store = store
		snapshot, err := store.View(t.Context())
		if err != nil || snapshot.Config.CollectDrafts != enabled || snapshot.Config.Version != config.Version+1 {
			t.Fatal(snapshot, err)
		}
		encoded, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := Decode(encoded)
		if err != nil || restored.Config != snapshot.Config {
			t.Fatal("backup/restore codec lost policy", restored, err)
		}
	}
}
