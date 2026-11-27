package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type integrityJSONItem struct {
	Key string `json:"key"`
}

type integrityJSONRequest struct {
	Title  string                     `json:"title"`
	Item   integrityJSONItem          `json:"item"`
	Items  []integrityJSONItem        `json:"items"`
	Values map[int64]string           `json:"values"`
	Extra  map[string]json.RawMessage `json:"extra"`
}

func TestStrictJSONRejectsAmbiguousAndLossyInputsBeforeMutation(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"field case alias", `{"title":"first","TITLE":"second"}`},
		{"nested Unicode alias", `{"item":{"key":"first","Key":"second"}}`},
		{"array field alias", `{"items":[{"key":"first","KEY":"second"}]}`},
		{"integer key alias", `{"values":{"1":"first","01":"second"}}`},
		{"signed zero alias", `{"values":{"0":"first","-0":"second"}}`},
		{"invalid value UTF8", "{\"title\":\"private-\xff\"}"},
		{"invalid key UTF8", "{\"extra\":{\"private-\xff\":1}}"},
		{"unpaired high surrogate", `{"title":"private-\ud800"}`},
		{"unpaired low surrogate", `{"title":"private-\udfff"}`},
		{"surrogate raw message", `{"extra":{"nested":[{"text":"\ud800"}]}}`},
		{"surrogate raw key", `{"extra":{"\ud800":1}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, route := range []string{"admin", "comment"} {
				t.Run(route, func(t *testing.T) {
					s := new(Server)
					r := httptest.NewRequest(http.MethodPost, "https://example.test/test", strings.NewReader(test.body))
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					target := integrityJSONRequest{Title: "unchanged", Item: integrityJSONItem{Key: "default"}}
					before := target
					var ok bool
					if route == "admin" {
						body, accepted := s.decodeStrictAPIJSON(w, r, &target)
						ok = accepted
						if len(body) != 0 {
							t.Error("rejected request returned a usable payload")
						}
					} else {
						ok = s.decodeStrictCommentJSON(w, r, &target)
					}
					if ok || w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"request.invalid_json"`) {
						t.Errorf("ambiguous or lossy request accepted: ok=%v status=%d body=%s", ok, w.Code, w.Body.String())
					}
					if !reflect.DeepEqual(before, target) {
						t.Error("rejected request changed the destination")
					}
					if strings.Contains(w.Body.String(), "private-") {
						t.Error("Unicode rejection exposed request contents")
					}
				})
			}
		})
	}
}

func TestStrictJSONPreservesCompatibleInputsAndRawBody(t *testing.T) {
	for _, body := range []string{
		` {"TITLE":"发布\ud83d\ude00","item":{"Key":"value"}} `,
		`{"title":"�\ufffd","extra":{"Key":1,"key":2,"literal":"\\ud800"}}`,
		`{"values":{"01":"one","+2":"two","-3":"three"},"items":[{"key":"a"},{"KEY":"b"}]}`,
		`{"title":"escaped \" quote and \\ slash","extra":{"large":9007199254740993}}`,
	} {
		t.Run(body, func(t *testing.T) {
			var expected integrityJSONRequest
			if err := json.Unmarshal([]byte(body), &expected); err != nil {
				t.Fatal(err)
			}
			for _, route := range []string{"admin", "comment"} {
				t.Run(route, func(t *testing.T) {
					s := new(Server)
					r := httptest.NewRequest(http.MethodPost, "https://example.test/test", strings.NewReader(body))
					r.Header.Set("Content-Type", "application/json; charset=utf-8")
					w := httptest.NewRecorder()
					var target integrityJSONRequest
					if route == "admin" {
						raw, ok := s.decodeStrictAPIJSON(w, r, &target)
						if !ok || !bytes.Equal(raw, []byte(body)) {
							t.Fatalf("valid request or its fingerprint bytes changed: %d %s", w.Code, w.Body.String())
						}
					} else if !s.decodeStrictCommentJSON(w, r, &target) {
						t.Fatalf("valid comment JSON rejected: %d %s", w.Code, w.Body.String())
					}
					if !reflect.DeepEqual(target, expected) {
						t.Fatalf("decoded value changed: got %#v want %#v", target, expected)
					}
				})
			}
		})
	}
}

func TestStrictJSONKeepsObjectSyntaxAndProtocolLimits(t *testing.T) {
	for _, route := range []string{"admin", "comment"} {
		t.Run(route, func(t *testing.T) {
			for _, test := range []struct {
				name, body, contentType string
				status                  int
			}{
				{"null", `null`, "application/json", 400},
				{"array", `[]`, "application/json", 400},
				{"scalar", `"text"`, "application/json", 400},
				{"unknown", `{"unknown":true}`, "application/json", 400},
				{"duplicate", `{"title":"a","title":"b"}`, "application/json", 400},
				{"trailing", `{} {}`, "application/json", 400},
				{"truncated", `{"title":"unfinished`, "application/json", 400},
				{"empty", " \n\t", "application/json", 400},
				{"wrong type", `{}`, "text/plain", 415},
				{"wrong charset", `{}`, "application/json; charset=iso-8859-1", 415},
				{"too large", `{"title":"` + strings.Repeat("x", publicCommentRequestMaxBytes+1) + `"}`, "application/json", 413},
			} {
				t.Run(test.name, func(t *testing.T) {
					s := new(Server)
					r := httptest.NewRequest(http.MethodPost, "https://example.test/test", strings.NewReader(test.body))
					r.Header.Set("Content-Type", test.contentType)
					w := httptest.NewRecorder()
					var target integrityJSONRequest
					var ok bool
					if route == "admin" {
						_, ok = s.decodeStrictAPIJSONLimit(w, r, &target, publicCommentRequestMaxBytes, "32 KiB")
					} else {
						ok = s.decodeStrictCommentJSON(w, r, &target)
					}
					if ok || w.Code != test.status {
						t.Fatalf("protocol boundary changed: ok=%v status=%d want=%d", ok, w.Code, test.status)
					}
				})
			}
		})
	}
}
