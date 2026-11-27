package articlewidgets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestComponentCodeConfigSurvivesServerNormalization(t *testing.T) {
	c := example("code")
	c.Text = strings.Repeat("<div>&</div>\n", 1800)
	// Browser JSON.stringify leaves HTML characters unescaped. The server
	// must return a configuration that its own decoder can still read.
	var browserJSON bytes.Buffer
	enc := json.NewEncoder(&browserJSON)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c); err != nil {
		t.Fatal(err)
	}
	if browserJSON.Len() > MaxConfigBytes {
		t.Fatal("fixture exceeds the input budget")
	}
	decoded, err := Decode(base64.RawURLEncoding.EncodeToString(browserJSON.Bytes()))
	if err != nil {
		t.Fatal("valid browser configuration rejected", err)
	}
	roundTrip, err := Decode(decoded.Encode())
	if err != nil || roundTrip.Text != c.Text {
		t.Fatalf("server returned an unreadable configuration: %v", err)
	}
}

func TestComponentValidationIncludesTotalJSONBudget(t *testing.T) {
	for _, kind := range []string{"table", "code"} {
		t.Run(kind, func(t *testing.T) {
			c := example(kind)
			if kind == "table" {
				c.Rows = make([][]string, 50)
				for i := range c.Rows {
					c.Rows[i] = []string{strings.Repeat("a", 1000), strings.Repeat("b", 1000)}
				}
			} else {
				c.Text = strings.Repeat("\x01", 11000)
			}
			if c.Validate() == nil {
				t.Fatal("fields fit individually but total JSON exceeds 64 KiB")
			}
			if strings.Contains(Render(c, 12, "chunnel"), "data-np-config=") {
				t.Fatal("renderer emitted a configuration that cannot be decoded")
			}
		})
	}
}
