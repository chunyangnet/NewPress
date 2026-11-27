package pluginjobs

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

func matchRepairReview(fields map[string]json.RawMessage, review *ReissueProvenance) error {
	raw, found, err := rawField(fields, "review")
	if err != nil {
		return err
	}
	if !found || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if review != nil {
			return ErrConflict
		}
		if raw, exists, err := rawField(fields, "reissue"); err != nil || exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrConflict
		}
		return nil
	}
	var original ReissueProvenance
	// Unknown fields may be the damage being repaired, but no duplicate or
	// ambiguous JSON field is permitted as an identity or audit anchor.
	if review == nil || packagefs.ValidateUniqueJSON(raw) != nil || json.Unmarshal(raw, &original) != nil {
		return ErrConflict
	}
	known, err := readJSONObject(raw)
	if err != nil {
		return ErrConflict
	}
	for _, key := range []string{"request_id", "request_fingerprint", "source_id", "source_fingerprint", "source_version", "source_status", "reviewer_id", "reason", "reviewed_at"} {
		if _, found, err := rawField(known, key); err != nil || !found {
			return ErrConflict
		}
	}
	if original.RequestID != review.RequestID || original.RequestFingerprint != review.RequestFingerprint ||
		original.SourceID != review.SourceID || original.SourceFingerprint != review.SourceFingerprint ||
		original.SourceVersion != review.SourceVersion || original.SourceStatus != review.SourceStatus ||
		original.ReviewerID != review.ReviewerID || original.Reason != review.Reason || !original.ReviewedAt.Equal(review.ReviewedAt) {
		return ErrConflict
	}
	return nil
}

func matchTaskRepair(original StoredRecord, value decodedRecord) (map[string]json.RawMessage, error) {
	anchor, fields, err := taskAnchor(original)
	if err != nil {
		return nil, err
	}
	var id, fingerprint, plugin, kind string
	if value.task != nil {
		j := value.task
		id, fingerprint, plugin, kind = j.ID, j.Fingerprint, j.Plugin, j.Kind
		if original.Type == RecordTask && (anchor.Epoch != j.Epoch || anchor.Key != j.Key || anchor.ActorID != j.ActorID) {
			return nil, ErrConflict
		}
	} else {
		j := value.taskHistory
		id, fingerprint, plugin, kind = j.ID, j.Fingerprint, j.Plugin, j.Kind
	}
	if anchor.ID != id || anchor.Fingerprint != fingerprint || anchor.Plugin != plugin || anchor.Kind != kind || matchRepairReview(fields, value.review()) != nil {
		return nil, ErrConflict
	}
	if at, err := readField[time.Time](fields, "created_at", true); err == nil {
		actual := value.taskHistory
		if value.task != nil {
			if !at.Equal(value.task.CreatedAt) {
				return nil, ErrConflict
			}
		} else if !at.Equal(actual.CreatedAt) {
			return nil, ErrConflict
		}
	}
	return fields, nil
}

func originalFanout(record StoredRecord) (FanoutReceipt, error) {
	if value, err := decodeRecord(record.Type, record.Data); err == nil {
		if value.fanout != nil {
			return *value.fanout, nil
		}
		return value.fanoutHistory.Receipt, nil
	}
	fields, err := readJSONObject(record.Data)
	if err != nil {
		return FanoutReceipt{}, err
	}
	if record.Type == RecordFanoutHistory {
		raw, found, err := rawField(fields, "receipt")
		if err != nil || !found {
			return FanoutReceipt{}, ErrInvalid
		}
		if fields, err = readJSONObject(raw); err != nil {
			return FanoutReceipt{}, err
		}
	}
	event, err := readField[Event](fields, "event", true)
	if err != nil {
		return FanoutReceipt{}, err
	}
	subs, err := readField[[]Subscription](fields, "subscriptions", true)
	if err != nil {
		return FanoutReceipt{}, err
	}
	delivered, err := readField[bool](fields, "delivered", false)
	if err != nil {
		return FanoutReceipt{}, err
	}
	cancelled, _ := readField[bool](fields, "cancelled", false)
	out := FanoutReceipt{Event: event, Subscriptions: subs, Delivered: delivered, Cancelled: cancelled}
	if ValidateFanout(out) != nil {
		return FanoutReceipt{}, ErrInvalid
	}
	return out, nil
}

// BuildRepairedRecord uses either the intact JSON authority or an explicitly
// supplied, checksum-confirmed replacement. It preserves request identities,
// review provenance and frozen cohorts. Active work is cancelled; repairing an
// index or record never grants permission to resume it.
func BuildRepairedRecord(record QuarantinedRecord, request RepairRequest, now time.Time) (StoredRecord, RepairAudit, error) {
	request, err := PrepareRepairRequest(request)
	if err != nil || ValidateQuarantine(record) != nil || now.IsZero() || now.Before(record.CapturedAt) || record.ID != request.ID {
		return StoredRecord{}, RepairAudit{}, ErrInvalid
	}
	if request.KeepReference != "" {
		return StoredRecord{}, RepairAudit{}, ErrInvalid
	}
	raw := request.Replacement
	if len(raw) == 0 {
		raw = record.Original.Data
	}
	value, err := decodeRecord(record.Original.Type, raw)
	if err != nil {
		return StoredRecord{}, RepairAudit{}, err
	}
	disposition := "terminal"
	if record.Original.Type.IsTask() {
		fields, err := matchTaskRepair(record.Original, value)
		if err != nil {
			return StoredRecord{}, RepairAudit{}, err
		}
		status, statusErr := readField[string](fields, "status", true)
		if record.Original.Type == RecordTaskHistory {
			if statusErr != nil || status != "succeeded" && status != "cancelled" || status != value.taskHistory.Status {
				return StoredRecord{}, RepairAudit{}, ErrConflict
			}
			// History is a terminal fact, not a way to rewrite attempts or versions
			// using an older backup of the same request.
			version, err := readField[int64](fields, "version", true)
			attempts, err2 := readField[int](fields, "attempts", true)
			if err != nil || err2 != nil || version != value.taskHistory.Version || attempts != value.taskHistory.Attempts {
				return StoredRecord{}, RepairAudit{}, ErrConflict
			}
			for key, actual := range map[string]time.Time{"updated_at": value.taskHistory.UpdatedAt, "available_at": value.taskHistory.AvailableAt, "archived_at": value.taskHistory.ArchivedAt} {
				if at, err := readField[time.Time](fields, key, true); err == nil && !at.Equal(actual) {
					return StoredRecord{}, RepairAudit{}, ErrConflict
				}
			}
		} else if statusErr == nil && (status == "succeeded" || status == "cancelled") {
			if value.task.Status != status {
				return StoredRecord{}, RepairAudit{}, ErrConflict
			}
			if version, err := readField[int64](fields, "version", true); err != nil || version != value.task.Version {
				return StoredRecord{}, RepairAudit{}, ErrConflict
			}
		} else {
			j := value.task
			if at, err := readField[time.Time](fields, "updated_at", true); err == nil && at.After(j.UpdatedAt) {
				j.UpdatedAt = at
			}
			if attempts, err := readField[int](fields, "attempts", true); err == nil && attempts > j.Attempts && attempts <= MaxAttempts {
				j.Attempts = attempts
			}
			if version, err := readField[int64](fields, "version", true); err == nil && version > j.Version {
				j.Version = version
			}
			if j.Version == math.MaxInt64 {
				return StoredRecord{}, RepairAudit{}, ErrInvalid
			}
			j.Version++
			j.Status, j.ErrorCode = "cancelled", "authorization_changed"
			j.LeaseToken, j.LeaseUntil = "", time.Time{}
			if !now.Before(j.UpdatedAt) {
				j.UpdatedAt = now.UTC()
			}
			disposition = "cancelled"
		}
	} else {
		original, err := originalFanout(record.Original)
		if err != nil {
			return StoredRecord{}, RepairAudit{}, err
		}
		var candidate *FanoutReceipt
		if value.fanout != nil {
			candidate = value.fanout
		} else {
			candidate = &value.fanoutHistory.Receipt
		}
		if !SameEvent(original.Event, candidate.Event) || !reflect.DeepEqual(original.Subscriptions, candidate.Subscriptions) || original.Delivered != candidate.Delivered {
			return StoredRecord{}, RepairAudit{}, ErrConflict
		}
		candidate.Cancelled = true
		if record.Original.Type == RecordFanoutHistory {
			fields, err := readJSONObject(record.Original.Data)
			if err != nil {
				return StoredRecord{}, RepairAudit{}, err
			}
			if at, err := readField[time.Time](fields, "archived_at", true); err == nil && !at.Equal(value.fanoutHistory.ArchivedAt) {
				return StoredRecord{}, RepairAudit{}, ErrConflict
			}
		}
		disposition = "fanout_cancelled"
	}
	var canonical []byte
	switch record.Original.Type {
	case RecordTask:
		canonical, err = json.Marshal(value.task)
	case RecordTaskHistory:
		canonical, err = json.Marshal(value.taskHistory)
	case RecordFanout:
		canonical, err = json.Marshal(value.fanout)
	case RecordFanoutHistory:
		canonical, err = json.Marshal(value.fanoutHistory)
	}
	if err != nil {
		return StoredRecord{}, RepairAudit{}, err
	}
	if _, err = decodeRecord(record.Original.Type, canonical); err != nil {
		return StoredRecord{}, RepairAudit{}, err
	}
	out := StoredRecord{Type: record.Original.Type, Key: value.key(), Data: canonical}
	audit := RepairAudit{At: now.UTC(), Operator: request.Operator, Reason: request.Reason, Replacement: hash(canonical), InputDigest: request.ReplacementDigest,
		Disposition: disposition, RestoredKeyID: hash([]byte(out.Key))}
	return out, audit, nil
}

// ReconcileQuarantinedRecord explicitly retains an already validated terminal
// receipt for the same identity. The duplicate original remains in quarantine
// evidence; no executable record is restored or removed by this operation.
func ReconcileQuarantinedRecord(q QuarantinedRecord, request RepairRequest, survivor StoredRecord, now time.Time) (RepairAudit, error) {
	request, err := PrepareRepairRequest(request)
	if err != nil || ValidateQuarantine(q) != nil || q.ID != request.ID || now.IsZero() || now.Before(q.CapturedAt) ||
		request.KeepReference == "" || RecordReference(survivor) != request.KeepReference || q.Original.Type.IsTask() != survivor.Type.IsTask() {
		return RepairAudit{}, ErrInvalid
	}
	value, err := decodeRecord(survivor.Type, survivor.Data)
	if err != nil || !recordIndexMatches(survivor, value) {
		return RepairAudit{}, ErrConflict
	}
	disposition := "reconciled_terminal"
	if survivor.Type.IsTask() {
		if value.task != nil && value.task.Status != "succeeded" && value.task.Status != "cancelled" {
			return RepairAudit{}, ErrConflict
		}
		fields, err := matchTaskRepair(q.Original, value)
		if err != nil {
			return RepairAudit{}, err
		}
		var summary Summary
		if value.task != nil {
			summary = Summarize(*value.task)
		} else {
			summary = value.taskHistory.Summary
		}
		status, statusErr := readField[string](fields, "status", true)
		if statusErr == nil && (status == "succeeded" || status == "cancelled") && status != summary.Status {
			return RepairAudit{}, ErrConflict
		}
		if version, err := readField[int64](fields, "version", true); err == nil &&
			(version > summary.Version || q.Original.Type == RecordTaskHistory && version != summary.Version) {
			return RepairAudit{}, ErrConflict
		}
		if q.Original.Type == RecordTaskHistory {
			attempts, err := readField[int](fields, "attempts", true)
			if err != nil || attempts != summary.Attempts {
				return RepairAudit{}, ErrConflict
			}
		}
	} else {
		original, err := originalFanout(q.Original)
		if err != nil {
			return RepairAudit{}, err
		}
		var candidate FanoutReceipt
		if value.fanout != nil {
			candidate = *value.fanout
		} else {
			candidate = value.fanoutHistory.Receipt
		}
		if !candidate.Delivered && !candidate.Cancelled || original.Delivered && !candidate.Delivered || original.Cancelled && !candidate.Cancelled ||
			!SameEvent(original.Event, candidate.Event) || !reflect.DeepEqual(original.Subscriptions, candidate.Subscriptions) {
			return RepairAudit{}, ErrConflict
		}
		disposition = "reconciled_fanout"
	}
	return RepairAudit{At: now.UTC(), Operator: request.Operator, Reason: request.Reason, Replacement: hash(survivor.Data),
		KeptReference: request.KeepReference, Disposition: disposition, RestoredKeyID: hash([]byte(value.key()))}, nil
}

// A restored receipt cannot overwrite an existing row or collide with any
// other provable logical/request identity, including an incorrectly keyed row.
// Remaining unprovable records retain their existing runtime/admission gates.
func ValidateRepairTarget(candidate StoredRecord, records []StoredRecord) error {
	if err := ValidateStoredRecord(candidate); err != nil {
		return err
	}
	value, _ := decodeRecord(candidate.Type, candidate.Data)
	for _, record := range records {
		if candidate.Type.IsTask() != record.Type.IsTask() {
			continue
		}
		if record.Key == candidate.Key {
			return ErrConflict
		}
		if other, err := decodeRecord(record.Type, record.Data); err == nil {
			if other.key() == value.key() || value.review() != nil && other.review() != nil && value.review().RequestID == other.review().RequestID {
				return ErrConflict
			}
		} else if record.Type.IsTask() {
			if anchor, fields, err := taskAnchor(record); err == nil {
				if anchor.ID == value.key() {
					return ErrConflict
				}
				if review, err := readField[ReissueProvenance](fields, "review", false); err == nil && value.review() != nil && review.RequestID == value.review().RequestID {
					return ErrConflict
				}
			}
		} else {
			if receipt, err := originalFanout(record); err == nil && receipt.Event.Key == value.key() {
				return ErrConflict
			}
		}
	}
	return nil
}
