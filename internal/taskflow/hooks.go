// Package taskflow carries optional execution hooks without coupling collectors to a queue.
package taskflow

import "context"

type hooks struct {
	report func(float64, string)
	yield  func(context.Context) error
}
type key struct{}

func WithHooks(ctx context.Context, report func(float64, string), yield func(context.Context) error) context.Context {
	return context.WithValue(ctx, key{}, hooks{report: report, yield: yield})
}

func Report(ctx context.Context, fraction float64, detail string) {
	if h, ok := ctx.Value(key{}).(hooks); ok && h.report != nil {
		h.report(fraction, detail)
	}
}

// Yield is called between requests, after releasing transactions and buffers.
func Yield(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h, ok := ctx.Value(key{}).(hooks); ok && h.yield != nil {
		return h.yield(ctx)
	}
	return nil
}
