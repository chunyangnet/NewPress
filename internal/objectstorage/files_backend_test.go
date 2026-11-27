package objectstorage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteListAdapters(t *testing.T) {
	for _, provider := range []string{"s3-compatible", "oss", "webdav"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				if provider == "webdav" {
					if r.Method != "PROPFIND" || r.Header.Get("Depth") != "1" {
						t.Errorf("unexpected request %s", r.Method)
					}
					w.WriteHeader(207)
					fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/assets/file.txt</d:href><d:propstat><d:prop><d:getcontentlength>5</d:getcontentlength><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
				} else {
					if r.URL.Query().Get("prefix") != "assets/" || r.URL.Query().Get("delimiter") != "/" {
						t.Errorf("query %s", r.URL.RawQuery)
					}
					fmt.Fprint(w, `<ListBucketResult><Name>media</Name><IsTruncated>false</IsTruncated><Contents><Key>assets/file.txt</Key><Size>5</Size></Contents></ListBucketResult>`)
				}
			}))
			defer server.Close()
			p := Profile{ID: "test", Provider: provider, Endpoint: server.URL, Region: "us-east-1", Bucket: "media", PathStyle: true, AccessKey: "key", SecretKey: "secret", Username: "user", Password: "pass", AllowPrivate: true, AllowHTTP: true, TimeoutSeconds: 10}
			backend, err := NewBackend(p)
			if err != nil {
				t.Fatal(err)
			}
			page, err := backend.(remoteLister).List(context.Background(), "assets/", "")
			if err != nil || len(page.Files) != 1 || page.Files[0].Key != "assets/file.txt" || page.Files[0].Size != 5 {
				t.Fatalf("page=%+v err=%v", page, err)
			}
		})
	}
}
