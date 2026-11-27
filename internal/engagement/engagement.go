// Package engagement owns account-scoped bookmarks, likes and follows.
package engagement

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	Bookmark = "bookmark"
	Like     = "like"
	Follow   = "follow"
)

var (
	ErrInvalid = errors.New("invalid engagement")
	ErrLimit   = errors.New("engagement capacity reached")
)

type Entry struct {
	UserID     int64     `json:"user_id"`
	TargetID   int64     `json:"target_id"`
	Kind       string    `json:"kind"`
	Collection string    `json:"collection,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type Input struct {
	UserID, TargetID int64
	Kind, Collection string
	Active           bool
}

type State struct {
	TargetID   int64  `json:"target_id"`
	Count      int    `json:"count"`
	Active     bool   `json:"active"`
	Collection string `json:"collection,omitempty"`
}

type Store interface {
	Ready(context.Context) error
	Set(context.Context, Input) error
	// Entries returns at most Capacity(kind) records, newest saved first.
	Entries(context.Context, int64, string) ([]Entry, error)
	States(context.Context, int64, string, []int64) ([]State, error)
	Merge(context.Context, int64, []Input, bool) (MergeResult, error)
}

type MergeResult struct {
	Added    int `json:"added"`
	Existing int `json:"existing"`
}

// NormalizeMerge only accepts additive account-bound changes. Existing
// records (including their collection and original timestamp) are preserved.
func NormalizeMerge(user int64, inputs []Input) ([]Input, error) {
	if user <= 0 || len(inputs) > 900 {
		return nil, ErrInvalid
	}
	result := make([]Input, 0, len(inputs))
	seen := map[entryKey]bool{}
	for _, input := range inputs {
		normalized, err := Normalize(input)
		key := entryKey{user, input.TargetID, input.Kind}
		if err != nil || input.UserID != user || !input.Active {
			return nil, ErrInvalid
		}
		if !seen[key] {
			seen[key] = true
			result = append(result, normalized)
		}
	}
	return result, nil
}

func Capacity(kind string) int {
	switch kind {
	case Bookmark:
		return 1000
	case Like:
		return 2000
	case Follow:
		return 500
	default:
		return 0
	}
}

func Normalize(input Input) (Input, error) {
	input.Collection = strings.TrimSpace(input.Collection)
	if input.UserID <= 0 || input.TargetID <= 0 || Capacity(input.Kind) == 0 ||
		(input.Kind == Follow && input.UserID == input.TargetID) ||
		(input.Kind != Bookmark && input.Collection != "") || !ValidCollection(input.Collection) {
		return input, ErrInvalid
	}
	return input, nil
}

func ValidCollection(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 40 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func ValidTargets(user int64, kind string, ids []int64) bool {
	if user < 0 || Capacity(kind) == 0 || len(ids) > 100 {
		return false
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
