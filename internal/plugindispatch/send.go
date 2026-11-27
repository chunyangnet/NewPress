package plugindispatch

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/plugineffects"
	"newpress.local/newpress/internal/safehttp"
)

func ValidURL(raw string) bool {
	if len(raw) < 1 || len(raw) > MaxURLBytes || !utf8.ValidString(raw) {
		return false
	}
	_, err := safehttp.ValidateURL(raw)
	return err == nil
}

type Sender interface {
	Send(context.Context, Intent) Outcome
}
type PublicSender struct{}

func (PublicSender) Send(ctx context.Context, i Intent) Outcome {
	client := safehttp.SingleAttemptClient(SendTimeout)
	defer client.CloseIdleConnections()
	return send(ctx, client, i)
}

// send never exports remote error strings, response text, URL paths or queries.
// NopCloser prevents NewRequest from installing GetBody. An operation header
// helps people correlate a request; it is not an Idempotency-Key contract.
func send(ctx context.Context, client *http.Client, i Intent) Outcome {
	unknown := Outcome{Code: "transport_unconfirmed"}
	if Validate(i) != nil || i.State != "sending" || !time.Now().Before(i.AttemptUntil) {
		return unknown
	}
	ctx, cancel := context.WithDeadline(ctx, i.AttemptUntil)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.URL, io.NopCloser(bytes.NewReader(i.Body)))
	if err != nil {
		return unknown
	}
	req.ContentLength = int64(len(i.Body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "NewPress-Webhook/1")
	req.Header.Set("X-NewPress-Operation", i.ID)
	req.Close = true
	res, err := client.Do(req)
	if err != nil {
		return unknown
	}
	defer res.Body.Close()
	out := Outcome{Code: "response_unconfirmed", HTTPStatus: res.StatusCode}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if err != nil || len(body) > MaxResponseBytes {
		return out
	}
	out.Code, out.ResponseDigest, out.ResponseBytes = "http_status", plugineffects.Digest(body), len(body)
	if res.StatusCode >= 200 && res.StatusCode <= 299 {
		out.Code = "http_2xx"
	}
	return out
}
