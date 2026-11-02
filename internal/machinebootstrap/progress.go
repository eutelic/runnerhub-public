package machinebootstrap

import "context"

type progressKey struct{}

// WithProgress adds a best-effort lifecycle reporter. Only fixed stage names
// are reported: never send remote SSH output or credentials into progress.
func WithProgress(ctx context.Context, report func(string)) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, progressKey{}, report)
}

func reportProgress(ctx context.Context, stage string) {
	if report, ok := ctx.Value(progressKey{}).(func(string)); ok && report != nil {
		report(stage)
	}
}
