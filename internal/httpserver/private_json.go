package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
)

// Encode before the final access check. Successful headers and response bytes
// are committed only after it; the shared writer checks bounded output too.
func writePrivateJSON(w http.ResponseWriter, r *http.Request, status int, value any, headers func(), access func() error, onError func(error)) {
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(value); err != nil {
		onError(err)
		return
	}
	if err := access(); err != nil {
		onError(err)
		return
	}
	if headers != nil {
		headers()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(encoded.Len()))
	output := privateResponse(w, r, access)
	output.WriteHeader(status)
	if _, err := output.Write(encoded.Bytes()); err != nil {
		panic(http.ErrAbortHandler)
	}
}
