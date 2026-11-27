package safehttp

import (
	"testing"
	"time"
)

func TestSingleAttemptClientRetainsPublicPolicyWithoutReuseOrHTTP2(t *testing.T) {
	c := SingleAttemptClient(time.Second)
	tr := c.Transport.(*publicTransport).transport
	if !tr.DisableKeepAlives || !tr.DisableCompression || tr.Protocols == nil || !tr.Protocols.HTTP1() || tr.Protocols.HTTP2() || tr.Protocols.UnencryptedHTTP2() || tr.Proxy != nil || tr.DialContext == nil || c.CheckRedirect == nil {
		t.Fatal("single-attempt public policy weakened")
	}
}
