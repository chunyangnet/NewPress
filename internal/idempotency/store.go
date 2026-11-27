// Package idempotency defines shared errors for atomic resource receipts.
package idempotency

import "errors"

var (
	ErrConflict = errors.New("idempotency key reused with a different request")
	ErrCapacity = errors.New("idempotency registry capacity reached")
)
