package pluginjobs

import (
	"context"
	"time"
)

// Summary is the only task representation intended for administrative output.
// It deliberately excludes payload, identity keys, epoch, actor and lease token.
type Summary struct {
	ID          string          `json:"id"`
	Plugin      string          `json:"plugin"`
	Kind        string          `json:"kind"`
	Status      string          `json:"status"`
	Attempts    int             `json:"attempts"`
	Version     int64           `json:"version"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	AvailableAt time.Time       `json:"available_at"`
	ErrorCode   string          `json:"error_code,omitempty"`
	ArchivedAt  time.Time       `json:"archived_at,omitzero"`
	Reissue     *ReissueSummary `json:"reissue,omitempty"`
}
type Query struct {
	Plugin   string
	Status   string
	Limit    int
	Offset   int
	Archived bool
}
type Page struct {
	Items []Summary `json:"items"`
	Total int       `json:"total"`
}

func NormalizeQuery(q Query) (Query, error) {
	maxOffset := MaxJobs
	if q.Archived {
		maxOffset = MaxArchivedJobs
	}
	if !name.MatchString(q.Plugin) || q.Offset < 0 || q.Offset > maxOffset {
		return Query{}, ErrInvalid
	}
	switch q.Status {
	case "", "pending", "running", "retry", "succeeded", "failed", "cancelled":
	default:
		return Query{}, ErrInvalid
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 100 {
		return Query{}, ErrInvalid
	}
	return q, nil
}
func Summarize(j Job) Summary {
	return Summary{ID: j.ID, Plugin: j.Plugin, Kind: j.Kind, Status: j.Status, Attempts: j.Attempts, Version: j.Version, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, AvailableAt: j.AvailableAt, ErrorCode: j.ErrorCode, ArchivedAt: j.ArchivedAt, Reissue: ReviewSummary(j.Review)}
}
func (q *Queue) List(ctx context.Context, query Query) (Page, error) {
	query, err := NormalizeQuery(query)
	if err != nil {
		return Page{}, err
	}
	var page Page
	if err := q.readState(ctx, func(s State) error {
		page = taskPage(s, query)
		return nil
	}); err != nil {
		return Page{}, err
	}
	return page, nil
}
