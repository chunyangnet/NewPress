package verification

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRejectedProviderProofsCannotExhaustReplayBudget(t *testing.T) {
	s := testService(t, "tencent")
	c := s.Config("comment", false, "")
	calls := 0
	mock(s, `{"Response":{"CaptchaCode":1}}`, func(*http.Request) { calls++ })
	// These tickets fail locally, without solving a challenge or contacting the
	// provider. They must not consume the budget reserved for accepted proofs.
	for i := range 8192 {
		proof := Proof{Token: "trerror-invalid-" + strconv.Itoa(i), Randstr: "random"}
		if token, err := s.Verify(t.Context(), c, "attacker", "192.0.2.1", "example.test", proof); token != "" || !errors.Is(err, ErrInvalid) {
			t.Fatalf("rejected ticket %d: token issued = %t, err = %v", i, token != "", err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid tickets reached provider %d times", calls)
	}
	proof := Proof{Token: "valid-ticket", Randstr: "random"}
	token, err := s.Verify(t.Context(), c, "visitor", "192.0.2.2", "example.test", proof)
	if err != nil || token == "" {
		t.Fatalf("invalid tickets denied a valid visitor: %v (retained proofs: %d)", err, len(s.used))
	}
	if calls != 1 || len(s.used) != 1 || s.Consume(c, "visitor", token) != nil {
		t.Fatal("valid proof or receipt was not retained correctly")
	}
}

func TestFailedProviderProofReleasesOnlyItsReservation(t *testing.T) {
	for _, failure := range []string{"invalid", "unavailable"} {
		t.Run(failure, func(t *testing.T) {
			s := testService(t, "turnstile")
			c := s.Config("comment", false, "")
			proof := Proof{Token: "retry-after-failure"}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var calls atomic.Int32
			s.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				body := `{"success":true,"hostname":"example.test","action":"comment"}`
				if calls.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
					if failure == "unavailable" {
						return nil, context.DeadlineExceeded
					}
					body = `{"success":false}`
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			done := make(chan error, 1)
			go func() {
				token, err := s.Verify(t.Context(), c, "source", "192.0.2.1", "example.test", proof)
				if token != "" {
					err = errors.New("failed proof minted a receipt")
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("provider was not called")
			}
			if _, err := s.Verify(t.Context(), c, "other-source", "192.0.2.2", "example.test", proof); !errors.Is(err, ErrInvalid) || calls.Load() != 1 {
				t.Fatal("concurrent proof bypassed in-flight reservation", err)
			}
			unblock()
			want := ErrInvalid
			if failure == "unavailable" {
				want = ErrUnavailable
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatal("unexpected initial failure", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("verification did not complete")
			}
			token, err := s.Verify(t.Context(), c, "source", "192.0.2.1", "example.test", proof)
			if err != nil || token == "" || calls.Load() != 2 {
				t.Fatal("failed reservation prevented fresh provider validation", err)
			}
			if _, err := s.Verify(t.Context(), c, "other-source", "192.0.2.2", "example.test", proof); !errors.Is(err, ErrInvalid) || calls.Load() != 2 {
				t.Fatal("successful proof became reusable", err)
			}
		})
	}
}

func TestSuccessfulProviderProofStaysUsedWhenReceiptsAreFull(t *testing.T) {
	s := testService(t, "turnstile")
	c := s.Config("comment", false, "")
	expires := time.Now().Add(time.Minute)
	for i := range 4096 {
		s.receipts[strconv.Itoa(i)] = ticket{expires: expires}
	}
	calls := 0
	mock(s, `{"success":true,"hostname":"example.test","action":"comment"}`, func(*http.Request) { calls++ })
	proof := Proof{Token: "accepted-with-full-receipts"}
	if _, err := s.Verify(t.Context(), c, "source", "192.0.2.1", "example.test", proof); !errors.Is(err, ErrUnavailable) {
		t.Fatal("receipt capacity was not enforced", err)
	}
	clear(s.receipts)
	if _, err := s.Verify(t.Context(), c, "source", "192.0.2.1", "example.test", proof); !errors.Is(err, ErrInvalid) || calls != 1 {
		t.Fatal("accepted proof reused after receipt failure", err)
	}
}
