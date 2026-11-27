package licensing

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriteNoticesRetainsFullLicenses(t *testing.T) {
	var b bytes.Buffer
	if err := WriteNotices(&b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{Apache20, ThirdPartyLicenses, FrontendLicenses, "Component: react", "Component: echarts", "golang.org/x/crypto", "github.com/tetratelabs/wazero", "Panayiotis Lipiridis", "B2"} {
		if want == "" || !strings.Contains(b.String(), want) {
			t.Fatalf("missing license notice: %.80s", want)
		}
	}
	err := errors.New("closed output")
	if got := WriteNotices(failingWriter{err}); !errors.Is(got, err) {
		t.Fatalf("output error not propagated: %v", got)
	}
}
