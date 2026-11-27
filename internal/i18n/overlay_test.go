package i18n

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChildOverlayBeforeFallback(t *testing.T) {
	parent, child := t.TempDir(), t.TempDir()
	write := func(dir, name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(parent, "en.json", `{"schema":1,"locale":"en","messages":{"hello":"parent English","keep":"kept"}}`)
	write(parent, "fr.json", `{"schema":1,"locale":"fr","messages":{"hello":"parent French"}}`)
	write(child, "fr.json", `{"schema":1,"locale":"fr","messages":{"hello":"child French"}}`)
	catalog, err := LoadOverlayDirectories(parent, child, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := NewTranslator("en", catalog).Lookup("hello"); got != "parent English" {
		t.Fatalf("default shadowed requested locale: %s", got)
	}
	write(child, "en.json", `{"schema":1,"locale":"en","messages":{"hello":"child English"}}`)
	catalog, err = LoadOverlayDirectories(parent, child, "fr")
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTranslator("en", catalog)
	if got, _ := tr.Lookup("hello"); got != "child English" {
		t.Fatal(got)
	}
	if got, _ := tr.Lookup("keep"); got != "kept" {
		t.Fatal(got)
	}
	if _, err := LoadOverlayDirectories(parent, child, "de"); err == nil {
		t.Fatal("missing default accepted")
	}
}
