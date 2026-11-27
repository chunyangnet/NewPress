package httpserver

import "newpress.local/newpress/internal/idempotency"

var (
	errIdempotencyConflict = idempotency.ErrConflict
	errIdempotencyCapacity = idempotency.ErrCapacity
)
