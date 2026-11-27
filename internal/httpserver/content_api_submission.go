package httpserver

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/idempotency"
)

func (s *Server) createContentAPISubmission(writer http.ResponseWriter, request *http.Request, principal auth.Principal, mutation adminContentMutationAPIRequest, body []byte, key string) {
	// Preserve the original API identity/path/raw-JSON contract. The editor uses
	// a separate namespace and intentionally returns its original saved version.
	namespace := principal.Username + "\x00" + request.URL.Path + "\x00" + key
	fingerprint := sha256.Sum256(body)
	submission := content.Submission{
		Key: fmt.Sprintf("%x", sha256.Sum256([]byte(namespace))), Fingerprint: fmt.Sprintf("%x", fingerprint),
		Action: "create", Actor: principal.Username,
	}
	// Authorization/JSON checks have already run; defaults, time and media must not reinterpret a saved request.
	if s.replayContentAPISubmission(writer, request, principal, submission) {
		return
	}
	input, fields := createAPIInput(mutation, principal, time.Now(), s.siteSettings.Snapshot().Settings.Writing)
	if len(fields) > 0 {
		s.writeContentValidationAPIError(writer, request, fields)
		return
	}
	submission.Input = input
	result, err := s.repository.Submit(request.Context(), submission)
	if err != nil {
		switch {
		case errors.Is(err, idempotency.ErrConflict), errors.Is(err, idempotency.ErrCapacity):
			s.handleIdempotencyAPIError(writer, request, err)
		case errors.Is(err, content.ErrSlugConflict), len(content.FieldErrors(err)) > 0:
			s.handleContentCreateAPIError(writer, request, err)
		default:
			// A lost commit acknowledgement cannot establish whether the operation
			// succeeded. The client must retry the same key and original body.
			s.writeContentAPISubmissionUnconfirmed(writer, request, err)
		}
		return
	}
	s.writeContentAPICreationResult(writer, request, principal, result.Receipt.PostID, result.Replayed)
}

// replayContentAPISubmission returns true whenever it writes a response. A
// failed receipt read must never fall through into an unprotected creation.
func (s *Server) replayContentAPISubmission(writer http.ResponseWriter, request *http.Request, principal auth.Principal, submission content.Submission) bool {
	result, err := s.repository.FindSubmission(request.Context(), submission.Key, submission.Fingerprint)
	if errors.Is(err, content.ErrNotFound) {
		return false
	}
	if errors.Is(err, idempotency.ErrConflict) {
		s.handleIdempotencyAPIError(writer, request, err)
	} else if err != nil {
		s.writeContentAPISubmissionUnconfirmed(writer, request, err)
	} else {
		s.writeContentAPICreationResult(writer, request, principal, result.Receipt.PostID, true)
	}
	return true
}

func (s *Server) writeContentAPICreationResult(writer http.ResponseWriter, request *http.Request, principal auth.Principal, id int64, replayed bool) {
	post, err := s.repository.GetByID(request.Context(), id)
	if err != nil {
		slog.Error("load idempotent content result", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusConflict, "request.idempotency_result_unavailable", "幂等请求已完成，但原资源暂时不可读取", map[string]any{"resource_id": id})
		return
	}
	if !s.requireContentAPIObjectWrite(writer, request, principal, post) {
		return
	}
	if replayed {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	s.writeContentAPISuccess(writer, http.StatusCreated, post, true)
}

func (s *Server) writeContentAPISubmissionUnconfirmed(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, content.ErrSnippetMaintenance) {
		s.handleIdempotencyAPIError(writer, request, err)
		return
	}
	slog.Error("content API submission not confirmed", "error", err, "request_id", request.Header.Get("X-Request-ID"))
	s.writeAPIError(writer, request, http.StatusServiceUnavailable, "request.idempotency_result_unconfirmed", "创建结果暂时无法确认，请使用原 Idempotency-Key 和原始请求体重试", nil)
}
