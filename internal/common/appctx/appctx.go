// Package appctx propagates the全链路追踪 requestId (DESIGN §9) through
// context.Context so every layer/goroutine can prefix logs with it.
package appctx

import "context"

type ctxKey int

const requestIDKey ctxKey = iota

// WithRequestID returns a child context carrying the requestId.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID extracts the requestId, or "-" when absent.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok && v != "" {
		return v
	}
	return "-"
}
