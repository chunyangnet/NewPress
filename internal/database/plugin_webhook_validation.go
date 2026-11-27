package database

import (
	"bytes"
	"encoding/binary"
	"slices"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/plugindispatch"
)

// This memo retains parsing results, never database visibility or permission.
// Every transaction checks current row tokens, SQL indexes and all count/byte/
// slot budgets. Large unchanged bodies can omit transfer only when the token,
// tuple version and verified trigger contract match the retained parse. Changed or
// first-seen bytes are strictly decoded even if the indexed version is unchanged.
// Restore and maintenance use their independent, uncached validation paths.
type webhookValidationMemo struct {
	mu    sync.RWMutex
	rows  map[string]*validatedWebhook
	stamp string
	hints *webhookValidationHints
}

// Binary array parameters are encoded once for an immutable snapshot. They
// retain their PostgreSQL element types and are passed as bound parameters.
type webhookValidationHints struct {
	once                sync.Once
	rows                map[string]*validatedWebhook
	ids, tokens, tuples []byte
}

func (h *webhookValidationHints) parameters() ([]byte, []byte, []byte) {
	if h == nil {
		return nil, nil, nil
	}
	h.once.Do(func() {
		tupleSize := 0
		for _, row := range h.rows {
			tupleSize += 4 + len(row.tuple)
		}
		ids := webhookArrayHeader(pgtype.TextOID, len(h.rows), len(h.rows)*68)
		tokens := webhookArrayHeader(pgtype.UUIDOID, len(h.rows), len(h.rows)*20)
		tuples := webhookArrayHeader(pgtype.TextOID, len(h.rows), tupleSize)
		for id, row := range h.rows {
			ids = binary.BigEndian.AppendUint32(ids, uint32(len(id)))
			ids = append(ids, id...)
			tokens = binary.BigEndian.AppendUint32(tokens, 16)
			tokens = append(tokens, row.token[:]...)
			tuples = binary.BigEndian.AppendUint32(tuples, uint32(len(row.tuple)))
			tuples = append(tuples, row.tuple...)
		}
		h.ids, h.tokens, h.tuples = ids, tokens, tuples
	})
	return h.ids, h.tokens, h.tuples
}

func webhookArrayHeader(oid uint32, count, size int) []byte {
	// One dimension, no nulls, element type, element count, lower bound 1.
	buf := make([]byte, 0, 20+size)
	for _, n := range []uint32{1, 0, oid, uint32(count), 1} {
		buf = binary.BigEndian.AppendUint32(buf, n)
	}
	return buf
}

type validatedWebhook struct {
	raw    []byte
	intent plugindispatch.Intent
	budget int
	token  [16]byte
	tuple  string
}

func (m *webhookValidationMemo) snapshot() map[string]*validatedWebhook {
	rows, _ := m.snapshotWithStamp()
	return rows
}

func (m *webhookValidationMemo) snapshotWithStamp() (map[string]*validatedWebhook, string) {
	rows, stamp, _ := m.snapshotWithHints()
	return rows, stamp
}

func (m *webhookValidationMemo) snapshotWithHints() (map[string]*validatedWebhook, string, *webhookValidationHints) {
	if m == nil {
		return nil, "", nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rows, m.stamp, m.hints
}

func (m *webhookValidationMemo) publish(rows map[string]*validatedWebhook, stamp string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if stamp == m.stamp && len(rows) == len(m.rows) {
		unchanged := true
		for id, row := range rows {
			if row != m.rows[id] {
				unchanged = false
				break
			}
		}
		if unchanged {
			return
		}
	}
	// A concurrent older snapshot may publish last. This only affects parsing
	// reuse: the next transaction checks its own current tokens and contract,
	// transferring bytes again whenever they do not match that older snapshot.
	m.rows = rows
	m.stamp = stamp
	m.hints = &webhookValidationHints{rows: rows}
}

func cloneWebhook(i plugindispatch.Intent) plugindispatch.Intent {
	i.Body = slices.Clone(i.Body)
	if i.Review != nil {
		r := *i.Review
		i.Review = &r
	}
	return i
}

func validateWebhookBytes(raw []byte, cached *validatedWebhook) (*validatedWebhook, error) {
	if cached != nil && bytes.Equal(cached.raw, raw) {
		return cached, nil
	}
	i, err := packagefs.DecodeJSON[plugindispatch.Intent](bytes.NewReader(raw), plugindispatch.MaxRecordBytes, "plugin webhook intent")
	if err != nil || plugindispatch.Validate(i) != nil {
		return nil, plugindispatch.ErrCorrupt
	}
	return &validatedWebhook{raw: slices.Clone(raw), intent: i, budget: plugindispatch.BudgetBytes(i)}, nil
}
