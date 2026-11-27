// Package plugineffects defines host-bound internal writes and durable results.
// A request identity survives retries and leases; it is never a lease token.
package plugineffects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"newpress.local/newpress/internal/content"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/pluginjobs"
)

const (
	MaxRequestBytes = 48 << 10
	MaxReceiptBytes = 2 << 10
	MaxReceipts     = 100000
	MaxTotalBytes   = 64 << 20
	MaxSlotsPerTask = 32
)

var (
	ErrInvalid     = errors.New("插件内容操作参数无效")
	ErrConflict    = errors.New("此插件操作槽位已提交其他请求")
	ErrCapacity    = errors.New("插件操作回执保留配额已满")
	ErrCorrupt     = errors.New("插件操作回执不一致，请停机核对")
	ErrUnavailable = errors.New("插件内容写入暂不可用")
	slotPattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
)

type ContentUpdate struct {
	ExpectedVersion int64             `json:"version"`
	Patch           content.TextPatch `json:"patch"`
}

// DecodeContentUpdate rejects null patch members rather than silently treating
// an explicitly supplied null as an omitted field.
func DecodeContentUpdate(raw []byte) (ContentUpdate, error) {
	update, err := packagefs.DecodeJSON[ContentUpdate](bytes.NewReader(raw), MaxRequestBytes, "plugin content update")
	if err != nil {
		return ContentUpdate{}, ErrInvalid
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return ContentUpdate{}, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(envelope["patch"], &fields); err != nil || len(fields) == 0 {
		return ContentUpdate{}, ErrInvalid
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ContentUpdate{}, ErrInvalid
		}
	}
	return update, nil
}

// Authorization is supplied by the host after local package verification.
// It must never be decoded from a guest payload.
type Authorization struct {
	Claim          pluginjobs.Job
	ManifestDigest string
	ModuleDigest   string
}

type Request struct {
	Authorization Authorization
	Slot          string
	PostID        int64
	Update        ContentUpdate
}

// Result contains no content, role snapshots or claim credentials. Replaying a
// result returns the originally accepted versions, not the current post version.
type Result struct {
	OperationID     string    `json:"operation_id"`
	PostID          int64     `json:"post_id"`
	PreviousVersion int64     `json:"previous_version"`
	Version         int64     `json:"version"`
	Changed         bool      `json:"changed"`
	CommittedAt     time.Time `json:"committed_at"`
}

type Receipt struct {
	Schema         int    `json:"schema"`
	JobID          string `json:"job_id"`
	Plugin         string `json:"plugin"`
	Slot           string `json:"slot"`
	Fingerprint    string `json:"fingerprint"`
	ActorID        int64  `json:"actor_id"`
	ManifestDigest string `json:"manifest_digest"`
	ModuleDigest   string `json:"module_digest"`
	Result         Result `json:"result"`
}

type Store interface {
	WriteContent(context.Context, Request) (Result, error)
}

func Digest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func IsDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func OperationID(jobID, slot string) (string, error) {
	if !IsDigest(jobID) || !slotPattern.MatchString(slot) {
		return "", ErrInvalid
	}
	return Digest([]byte("newpress.content.update.v1\x00" + jobID + "\x00" + slot)), nil
}

func (request Request) Identity() (string, string, error) {
	id, err := OperationID(request.Authorization.Claim.ID, request.Slot)
	if err != nil || request.PostID <= 0 || request.Update.ExpectedVersion <= 0 ||
		!IsDigest(request.Authorization.ManifestDigest) || !IsDigest(request.Authorization.ModuleDigest) {
		return "", "", ErrInvalid
	}
	raw, err := json.Marshal(struct {
		PostID int64         `json:"post_id"`
		Update ContentUpdate `json:"update"`
	}{request.PostID, request.Update})
	if err != nil || len(raw) > MaxRequestBytes {
		return "", "", ErrInvalid
	}
	if _, _, err := request.Update.Patch.Apply(content.Post{}); err != nil {
		return "", "", ErrInvalid
	}
	return id, Digest(raw), nil
}

func ValidateReceipt(receipt Receipt) error {
	id, err := OperationID(receipt.JobID, receipt.Slot)
	r := receipt.Result
	if err != nil || receipt.Schema != 1 || r.OperationID != id ||
		packagefs.ValidateSlug(receipt.Plugin) != nil || !IsDigest(receipt.Fingerprint) ||
		receipt.ActorID <= 0 || !IsDigest(receipt.ManifestDigest) || !IsDigest(receipt.ModuleDigest) ||
		r.PostID <= 0 || r.PreviousVersion <= 0 || r.Version < r.PreviousVersion ||
		r.CommittedAt.IsZero() || (r.Changed && r.Version-r.PreviousVersion != 1) ||
		(!r.Changed && r.Version != r.PreviousVersion) {
		return ErrCorrupt
	}
	raw, err := json.Marshal(receipt)
	if err != nil || len(raw) > MaxReceiptBytes {
		return ErrCorrupt
	}
	return nil
}
