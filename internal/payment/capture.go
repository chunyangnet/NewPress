package payment

import "context"

type captureCheckKey struct{}

type captureCheck func(context.Context, Notification) error

// WithCaptureCheck adds application validation immediately before a provider
// captures money. The notification contains the provider's original terms.
func WithCaptureCheck(ctx context.Context, check func(context.Context, Notification) error) context.Context {
	return context.WithValue(ctx, captureCheckKey{}, captureCheck(check))
}

func checkCapture(ctx context.Context, n Notification) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if check, ok := ctx.Value(captureCheckKey{}).(captureCheck); ok && check != nil {
		return check(ctx, n)
	}
	return nil
}
