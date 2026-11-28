package httpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Article generation and workbench reports use identical frame/output checks.
// Only the handler goroutine writes; producers share the authority guard.
type aiEventStream struct {
	writer       http.ResponseWriter
	request      *http.Request
	controller   *http.ResponseController
	guard        *aiGenerationGuard
	firstPayload bool
	checkedBytes int
}

func (s *aiEventStream) write(event aiStreamEvent) error {
	encoded, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	force := event.Name == "ready" || event.Name == "done" || event.Name == "error" || s.firstPayload
	if err := s.guard.check(force); err != nil {
		return err
	}
	if force {
		s.checkedBytes = 0
	}
	if event.Name != "ready" && event.Name != "ping" {
		s.firstPayload = false
	}
	frame := make([]byte, 0, len(encoded)+len(event.Name)+16)
	frame = append(frame, "event: "...)
	frame = append(frame, event.Name...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, encoded...)
	frame = append(frame, '\n', '\n')
	written := 0
	for len(frame) > 0 {
		force = s.checkedBytes >= privateResponseCheckBytes
		if err := s.guard.check(force); err != nil {
			if written != 0 {
				// Do not append an error event inside partially written JSON.
				panic(http.ErrAbortHandler)
			}
			return err
		}
		if force {
			s.checkedBytes = 0
		}
		chunk := min(len(frame), 64<<10, privateResponseCheckBytes-s.checkedBytes)
		n, err := s.writer.Write(frame[:chunk])
		s.checkedBytes += n
		written += n
		if err != nil {
			s.guard.cancel(err)
			panic(http.ErrAbortHandler)
		}
		if n != chunk {
			s.guard.cancel(io.ErrShortWrite)
			panic(http.ErrAbortHandler)
		}
		frame = frame[n:]
	}
	if err := s.controller.Flush(); err != nil {
		s.guard.cancel(err)
		panic(http.ErrAbortHandler)
	}
	return nil
}

func (s *aiEventStream) stop(err error) {
	s.guard.cancel(err)
	if s.request.Context().Err() != nil {
		return
	}
	event := aiGenerationStopEvent(err)
	encoded, _ := json.Marshal(event.Data)
	frame := fmt.Sprintf("event: error\ndata: %s\n\n", encoded)
	if n, err := io.WriteString(s.writer, frame); err != nil || n != len(frame) {
		panic(http.ErrAbortHandler)
	}
	if err := s.controller.Flush(); err != nil {
		panic(http.ErrAbortHandler)
	}
}
