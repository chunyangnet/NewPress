package httpserver

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"newpress.local/newpress/internal/auth"
	"newpress.local/newpress/internal/idempotency"
	"newpress.local/newpress/internal/navigation"
)

func (s *Server) createNavigationAPISubmission(writer http.ResponseWriter, request *http.Request, principal auth.Principal, input navigation.MenuInput, body []byte, key string) {
	// Keep the original navigation identity/operation/raw-JSON namespace.
	namespace := principal.Username + "\x00navigation:create\x00" + key
	fingerprint := sha256.Sum256(body)
	creation := navigation.Creation{
		Key: fmt.Sprintf("%x", sha256.Sum256([]byte(namespace))), Fingerprint: fmt.Sprintf("%x", fingerprint),
		Input: input, Actor: navigationAPIActor(principal),
	}
	if s.replayNavigationCreation(writer, request, creation) {
		return
	}
	result, err := s.navigation.CreateOnce(request.Context(), creation)
	if err != nil {
		switch {
		case errors.Is(err, idempotency.ErrConflict), errors.Is(err, idempotency.ErrCapacity):
			s.handleIdempotencyAPIError(writer, request, err)
		case errors.Is(err, navigation.ErrSlugConflict), errors.Is(err, navigation.ErrLocationConflict), len(navigation.FieldErrors(err)) > 0:
			s.handleNavigationAPIError(writer, request, 0, err)
		default:
			s.writeNavigationCreationUnconfirmed(writer, request, err)
		}
		return
	}
	s.writeNavigationCreationResult(writer, request, result.Receipt.MenuID, result.Replayed)
}

func (s *Server) replayNavigationCreation(writer http.ResponseWriter, request *http.Request, creation navigation.Creation) bool {
	result, err := s.navigation.FindCreation(request.Context(), creation.Key, creation.Fingerprint)
	if errors.Is(err, navigation.ErrNotFound) {
		return false
	}
	if errors.Is(err, idempotency.ErrConflict) {
		s.handleIdempotencyAPIError(writer, request, err)
	} else if err != nil {
		s.writeNavigationCreationUnconfirmed(writer, request, err)
	} else {
		s.writeNavigationCreationResult(writer, request, result.Receipt.MenuID, true)
	}
	return true
}

func (s *Server) writeNavigationCreationResult(writer http.ResponseWriter, request *http.Request, id int64, replayed bool) {
	menu, err := s.navigation.Get(request.Context(), id)
	if err != nil {
		slog.Error("load idempotent navigation result", "id", id, "error", err, "request_id", request.Header.Get("X-Request-ID"))
		s.writeAPIError(writer, request, http.StatusConflict, "request.idempotency_result_unavailable", "幂等请求已完成，但原菜单暂时不可读取", map[string]any{"resource_id": id})
		return
	}
	if replayed {
		writer.Header().Set("Idempotency-Replayed", "true")
	}
	s.writeNavigationMenuAPISuccess(writer, http.StatusCreated, menu, true)
}

func (s *Server) writeNavigationCreationUnconfirmed(writer http.ResponseWriter, request *http.Request, err error) {
	slog.Error("navigation creation not confirmed", "error", err, "request_id", request.Header.Get("X-Request-ID"))
	s.writeAPIError(writer, request, http.StatusServiceUnavailable, "request.idempotency_result_unconfirmed", "菜单创建结果暂时无法确认，请使用原 Idempotency-Key 和原始请求体重试", nil)
}
