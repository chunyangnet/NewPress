// Package plugindispatch defines durable, single-attempt HTTPS webhook intents.
// Acceptance, transport confirmation and human review are different facts.
package plugindispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugineffects"
)

const (
	MaxBodyBytes     = 16 << 10
	MaxURLBytes      = 2048
	MaxRecordBytes   = 32 << 10
	MaxRecords       = 10000
	MaxTotalBytes    = 64 << 20
	MaxSlotsPerTask  = 32
	AttemptDuration  = 30 * time.Second
	SendTimeout      = 10 * time.Second
	MaxResponseBytes = 32 << 10
)

var (
	ErrInvalid     = errors.New("Webhook 参数无效")
	ErrConflict    = errors.New("Webhook 操作身份或版本冲突")
	ErrCapacity    = errors.New("Webhook 永久记录保留配额已满")
	ErrCorrupt     = errors.New("Webhook 持久记录不一致，请停机核对")
	ErrUnavailable = errors.New("Webhook 发送暂不可用")
	slotPattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
)

type Request struct {
	Authorization plugineffects.Authorization
	Slot          string
	URL           string
	Body          json.RawMessage
}

// Accepted is immutable, even after cancellation or dispatch. It makes no
// assertion about delivery. Replaying a slot always returns this original fact.
type Accepted struct {
	OperationID string    `json:"operation_id"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

// Intent is private storage, never an HTTP response. The source fingerprint
// links an archived terminal task to the actor/epoch retained here.
type Intent struct {
	Schema            int             `json:"schema"`
	ID                string          `json:"id"`
	JobID             string          `json:"job_id"`
	SourceFingerprint string          `json:"source_fingerprint"`
	Plugin            string          `json:"plugin"`
	Kind              string          `json:"kind"`
	Epoch             string          `json:"epoch"`
	ActorID           int64           `json:"actor_id"`
	ManifestDigest    string          `json:"manifest_digest"`
	ModuleDigest      string          `json:"module_digest"`
	Slot              string          `json:"slot"`
	Fingerprint       string          `json:"fingerprint"`
	URL               string          `json:"url"`
	Body              json.RawMessage `json:"body"`
	Version           int64           `json:"version"`
	State             string          `json:"state"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	AttemptToken      string          `json:"attempt_token,omitempty"`
	AttemptAt         time.Time       `json:"attempt_at,omitzero"`
	AttemptUntil      time.Time       `json:"attempt_until,omitzero"`
	Outcome           Outcome         `json:"outcome"`
	ResultAt          time.Time       `json:"result_at,omitzero"`
	Review            *Review         `json:"review,omitempty"`
}

type Outcome struct {
	Code           string `json:"code,omitempty"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	ResponseDigest string `json:"response_digest,omitempty"`
	ResponseBytes  int    `json:"response_bytes,omitempty"`
}

type Review struct {
	Decision string    `json:"decision"`
	Reason   string    `json:"reason"`
	ActorID  int64     `json:"actor_id"`
	At       time.Time `json:"at"`
}

type ReviewRequest struct {
	ID       string
	Version  int64
	ActorID  int64
	Decision string
	Reason   string
}

type Store interface {
	Enqueue(context.Context, Request) (Accepted, error)
	Claim(context.Context) (Intent, bool, error)
	Finish(context.Context, Intent, Outcome) error
}

type Reviewer interface {
	List(context.Context, Query) (Page, error)
	Review(context.Context, ReviewRequest) (Summary, error)
}

func OperationID(jobID, slot string) (string, error) {
	if !plugineffects.IsDigest(jobID) || !slotPattern.MatchString(slot) {
		return "", ErrInvalid
	}
	return plugineffects.Digest([]byte("newpress.http.enqueue.v1\x00" + jobID + "\x00" + slot)), nil
}

// NormalizeBody preserves key order and numeric precision. A differently
// ordered or otherwise changed request occupies a conflicting operation slot.
func NormalizeBody(body []byte) (json.RawMessage, error) {
	if len(body) == 0 || len(body) > MaxBodyBytes || !utf8.Valid(body) || packagefs.ValidateUniqueJSON(body) != nil {
		return nil, ErrInvalid
	}
	b := bytes.TrimSpace(body)
	if len(b) == 0 || b[0] != '{' {
		return nil, ErrInvalid
	}
	raw, err := json.Marshal(json.RawMessage(b))
	if err != nil || len(raw) > MaxBodyBytes {
		return nil, ErrInvalid
	}
	return raw, nil
}

func (r Request) Identity() (string, string, json.RawMessage, error) {
	id, err := OperationID(r.Authorization.Claim.ID, r.Slot)
	if err != nil || !ValidURL(r.URL) || !plugineffects.IsDigest(r.Authorization.ManifestDigest) || !plugineffects.IsDigest(r.Authorization.ModuleDigest) {
		return "", "", nil, ErrInvalid
	}
	body, err := NormalizeBody(r.Body)
	if err != nil {
		return "", "", nil, err
	}
	raw, err := json.Marshal(struct {
		URL  string          `json:"url"`
		Body json.RawMessage `json:"body"`
	}{r.URL, body})
	if err != nil {
		return "", "", nil, ErrInvalid
	}
	return id, plugineffects.Digest(raw), body, nil
}

func (i Intent) Accepted() Accepted { return Accepted{i.ID, i.CreatedAt} }

// Reserve space for all later states and a maximum-size human review. Capacity
// cannot be consumed by another enqueue after an irreversible send starts.
func BudgetBytes(i Intent) int {
	encoded, _ := json.Marshal(i.URL)
	return len(encoded) + len(i.Body) + 8192
}

func ValidReason(reason string) bool {
	if reason != strings.TrimSpace(reason) || reason == "" || !utf8.ValidString(reason) || len(reason) > 2000 || len([]rune(reason)) > 500 {
		return false
	}
	for _, r := range reason {
		if r < 32 && r != '\n' && r != '\t' || r == 127 {
			return false
		}
	}
	return true
}

func ValidReview(r ReviewRequest) bool {
	return plugineffects.IsDigest(r.ID) && r.Version > 0 && r.ActorID > 0 && (r.Decision == "received" || r.Decision == "not_received") && ValidReason(r.Reason)
}

func ValidOutcome(o Outcome) bool {
	if o.HTTPStatus != 0 && (o.HTTPStatus < 100 || o.HTTPStatus > 599) || o.ResponseBytes < 0 || o.ResponseBytes > MaxResponseBytes || o.ResponseDigest != "" && !plugineffects.IsDigest(o.ResponseDigest) {
		return false
	}
	switch o.Code {
	case "http_2xx":
		return o.HTTPStatus >= 200 && o.HTTPStatus <= 299 && o.ResponseDigest != ""
	case "http_status":
		return o.HTTPStatus >= 100 && (o.HTTPStatus < 200 || o.HTTPStatus >= 300) && o.ResponseDigest != ""
	case "response_unconfirmed":
		return o.HTTPStatus >= 100 && o.ResponseDigest == "" && o.ResponseBytes == 0
	case "transport_unconfirmed", "worker_expired", "restored", "maintenance", "source_cancelled", "authority_changed":
		return o.HTTPStatus == 0 && o.ResponseDigest == "" && o.ResponseBytes == 0
	default:
		return false
	}
}

func Validate(i Intent) error {
	id, err := OperationID(i.JobID, i.Slot)
	if err != nil || i.ID != id || i.Schema != 1 || i.Version < 1 || i.Version > 4 || packagefs.ValidateSlug(i.Plugin) != nil ||
		(i.Kind != "task.execute.v1" && i.Kind != "event.deliver.v1") || i.ActorID < 1 || !plugineffects.IsDigest(i.Epoch) || !plugineffects.IsDigest(i.SourceFingerprint) ||
		!plugineffects.IsDigest(i.ManifestDigest) || !plugineffects.IsDigest(i.ModuleDigest) || i.CreatedAt.IsZero() || i.UpdatedAt.Before(i.CreatedAt) {
		return ErrCorrupt
	}
	r := Request{Authorization: plugineffects.Authorization{ManifestDigest: i.ManifestDigest, ModuleDigest: i.ModuleDigest}, Slot: i.Slot, URL: i.URL, Body: i.Body}
	r.Authorization.Claim.ID = i.JobID
	_, fp, body, err := r.Identity()
	if err != nil || fp != i.Fingerprint || !bytes.Equal(body, i.Body) {
		return ErrCorrupt
	}
	attempted := i.AttemptToken != "" && plugineffects.IsDigest(i.AttemptToken) && !i.AttemptAt.Before(i.CreatedAt) && i.AttemptUntil.Equal(i.AttemptAt.Add(AttemptDuration)) && !i.UpdatedAt.Before(i.AttemptAt)
	unattempted := i.AttemptToken == "" && i.AttemptAt.IsZero() && i.AttemptUntil.IsZero()
	switch i.State {
	case "pending":
		if i.Version != 1 || !unattempted || i.Outcome != (Outcome{}) || i.Review != nil || !i.CreatedAt.Equal(i.UpdatedAt) || !i.ResultAt.IsZero() {
			return ErrCorrupt
		}
	case "cancelled":
		if i.Version != 2 || !unattempted || i.Review != nil || !i.ResultAt.IsZero() || !ValidOutcome(i.Outcome) || (i.Outcome.Code != "restored" && i.Outcome.Code != "maintenance" && i.Outcome.Code != "source_cancelled" && i.Outcome.Code != "authority_changed") {
			return ErrCorrupt
		}
	case "sending":
		if i.Version != 2 || !attempted || i.Outcome != (Outcome{}) || i.Review != nil || !i.UpdatedAt.Equal(i.AttemptAt) || !i.ResultAt.IsZero() {
			return ErrCorrupt
		}
	case "http_confirmed", "uncertain", "reviewed_received", "reviewed_not_received":
		if !attempted || !ValidOutcome(i.Outcome) || i.ResultAt.Before(i.AttemptAt) || i.UpdatedAt.Before(i.ResultAt) || i.Outcome.Code == "worker_expired" && i.ResultAt.Before(i.AttemptUntil) || i.Outcome.Code == "source_cancelled" || i.Outcome.Code == "authority_changed" {
			return ErrCorrupt
		}
		if (i.State == "http_confirmed") != (i.Outcome.Code == "http_2xx") {
			return ErrCorrupt
		}
		if strings.HasPrefix(i.State, "reviewed_") {
			if i.Version != 4 || i.Review == nil || !ValidReview(ReviewRequest{i.ID, i.Version, i.Review.ActorID, i.Review.Decision, i.Review.Reason}) || i.Review.At.Before(i.ResultAt) || !i.UpdatedAt.Equal(i.Review.At) || i.State != "reviewed_"+i.Review.Decision {
				return ErrCorrupt
			}
		} else if i.Version != 3 || i.Review != nil || !i.UpdatedAt.Equal(i.ResultAt) {
			return ErrCorrupt
		}
	default:
		return ErrCorrupt
	}
	raw, err := json.Marshal(i)
	if err != nil || BudgetBytes(i) > MaxRecordBytes || len(raw) > MaxRecordBytes || len(raw) > BudgetBytes(i) {
		return ErrCorrupt
	}
	return nil
}

type Query struct {
	Plugin, JobID, State string
	Limit, Offset        int
}
type Summary struct {
	ID          string         `json:"id"`
	JobID       string         `json:"job_id"`
	Plugin      string         `json:"plugin"`
	Slot        string         `json:"slot"`
	Target      string         `json:"target"`
	Fingerprint string         `json:"fingerprint"`
	Version     int64          `json:"version"`
	State       string         `json:"state"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Outcome     Outcome        `json:"outcome"`
	ResultAt    time.Time      `json:"result_at,omitzero"`
	Review      *ReviewSummary `json:"review,omitempty"`
}
type ReviewSummary struct {
	Decision string    `json:"decision"`
	Reason   string    `json:"reason"`
	ActorID  string    `json:"actor_id"`
	At       time.Time `json:"at"`
}
type Page struct {
	Items  []Summary `json:"items"`
	Total  int       `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
}

func NormalizeQuery(q Query) (Query, error) {
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > MaxRecords || q.Plugin != "" && packagefs.ValidateSlug(q.Plugin) != nil || q.JobID != "" && !plugineffects.IsDigest(q.JobID) {
		return Query{}, ErrInvalid
	}
	switch q.State {
	case "", "pending", "sending", "http_confirmed", "uncertain", "cancelled", "reviewed_received", "reviewed_not_received":
	default:
		return Query{}, ErrInvalid
	}
	return q, nil
}

func TargetOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return "https://" + strings.ToLower(u.Hostname())
}
