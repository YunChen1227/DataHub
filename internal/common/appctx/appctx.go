// Package appctx propagates the全链路追踪 requestId (DESIGN §9) through
// context.Context so every layer/goroutine can prefix logs with it.
package appctx

import "context"

type ctxKey int

const (
	requestIDKey ctxKey = iota
	clientIPKey
)

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

// WithClientIP returns a child context carrying the caller's source IP (DESIGN §16.4).
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey, ip)
}

// ClientIP extracts the caller's source IP, or "" when absent.
func ClientIP(ctx context.Context) string {
	if v, ok := ctx.Value(clientIPKey).(string); ok {
		return v
	}
	return ""
}
