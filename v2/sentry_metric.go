package core

import (
	"context"
	"fmt"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
)

// Units a metric can be recorded in. These are the ones a backend actually
// reaches for; any string sentry-go accepts works too.
const (
	UnitMillisecond = sentry.UnitMillisecond
	UnitSecond      = sentry.UnitSecond
	UnitByte        = sentry.UnitByte
	UnitKilobyte    = sentry.UnitKilobyte
	UnitMegabyte    = sentry.UnitMegabyte
	UnitPercent     = sentry.UnitPercent
	UnitRatio       = sentry.UnitRatio
)

// IMeter records the numbers a service watches over time — orders paid, queue
// depth, upload size. They arrive in Sentry alongside the traces and logs of the
// same request, so a spike can be opened and read rather than just seen.
//
// Like every other capability it is reached through the context
// (`ctx.Meter()`), and it is never nil: with metrics off every call is a no-op,
// so call sites never guard.
type IMeter interface {
	// Enabled reports whether metrics actually leave the process.
	Enabled() bool
	// WithContext binds the meter to ctx, so what it records carries that
	// request or run's trace.
	WithContext(ctx context.Context) IMeter

	// Count adds n to a counter — "how many times did this happen".
	Count(name string, n int64, opts ...MetricOption)
	// Gauge records a value that moves up and down — queue depth, pool size.
	// Only the latest value of an interval means anything.
	Gauge(name string, value float64, opts ...MetricOption)
	// Distribution records one sample of something whose spread matters —
	// latency, payload size. Sentry keeps the percentiles, not just the mean.
	Distribution(name string, sample float64, opts ...MetricOption)
	// Duration is Distribution for a length of time, recorded in milliseconds
	// so every timing in the org is comparable.
	Duration(name string, d time.Duration, opts ...MetricOption)
}

// MetricOption tunes a single measurement.
type MetricOption func(*metricOptions)

type metricOptions struct {
	unit  string
	attrs map[string]any
}

// MetricUnit states what the value is measured in, so Sentry renders "250ms"
// instead of "250".
func MetricUnit(unit string) MetricOption {
	return func(o *metricOptions) { o.unit = unit }
}

// MetricAttr adds one dimension to break the metric down by. Keep the values
// bounded — a customer id makes as many series as you have customers.
func MetricAttr(key string, value any) MetricOption {
	return func(o *metricOptions) {
		if o.attrs == nil {
			o.attrs = map[string]any{}
		}
		o.attrs[key] = value
	}
}

// MetricAttrs adds several dimensions at once.
func MetricAttrs(attrs map[string]any) MetricOption {
	return func(o *metricOptions) {
		if o.attrs == nil {
			o.attrs = make(map[string]any, len(attrs))
		}
		for k, v := range attrs {
			o.attrs[k] = v
		}
	}
}

func buildMetricOptions(opts []MetricOption) metricOptions {
	var o metricOptions
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

// meter is the working implementation, bound to a tracker and (optionally) to
// the context of the request or run doing the measuring.
type meter struct {
	tracker *sentryTracker
	ctx     context.Context
}

var _ IMeter = (*meter)(nil)

func (m *meter) Enabled() bool { return m.tracker.cfg.metrics }

func (m *meter) WithContext(ctx context.Context) IMeter {
	if ctx == nil {
		return m
	}
	cp := *m
	cp.ctx = ctx
	return &cp
}

func (m *meter) Count(name string, n int64, opts ...MetricOption) {
	if sm := m.sentryMeter(name); sm != nil {
		sm.Count(name, n, m.options(opts)...)
	}
}

func (m *meter) Gauge(name string, value float64, opts ...MetricOption) {
	if sm := m.sentryMeter(name); sm != nil {
		sm.Gauge(name, value, m.options(opts)...)
	}
}

func (m *meter) Distribution(name string, sample float64, opts ...MetricOption) {
	if sm := m.sentryMeter(name); sm != nil {
		sm.Distribution(name, sample, m.options(opts)...)
	}
}

func (m *meter) Duration(name string, d time.Duration, opts ...MetricOption) {
	// milliseconds, always: a timing you cannot compare with the next one is a
	// number, not a measurement
	m.Distribution(name, float64(d.Nanoseconds())/1e6, append(opts, MetricUnit(UnitMillisecond))...)
}

// sentryMeter builds the SDK meter for this measurement, or nil when there is
// nothing to record to.
func (m *meter) sentryMeter(name string) sentry.Meter {
	if !m.tracker.cfg.metrics || name == "" {
		return nil
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// the same fallback the log bridge uses: measured inside a request it lands
	// on that request's trace; measured at boot it still lands
	if sentry.GetHubFromContext(ctx) == nil {
		ctx = sentry.SetHubOnContext(ctx, m.tracker.hubFor())
	}
	return sentry.NewMeter(ctx)
}

// options translates the framework's options into the SDK's, masking attribute
// values on the way — a metric is as capable of carrying a secret as a log line.
func (m *meter) options(opts []MetricOption) []sentry.MeterOption {
	o := buildMetricOptions(opts)
	out := make([]sentry.MeterOption, 0, 2)
	if o.unit != "" {
		out = append(out, sentry.WithUnit(o.unit))
	}
	if len(o.attrs) > 0 {
		builders := make([]attribute.Builder, 0, len(o.attrs))
		for k, v := range o.attrs {
			if b, ok := metricAttr(m.tracker.cfg.scrubber, k, v); ok {
				builders = append(builders, b)
			}
		}
		if len(builders) > 0 {
			out = append(out, sentry.WithAttributes(builders...))
		}
	}
	return out
}

// metricAttr types one dimension for Sentry, so a number stays a number and can
// be compared rather than only matched.
func metricAttr(sc *scrubber, key string, value any) (attribute.Builder, bool) {
	if key == "" {
		return attribute.Builder{}, false
	}
	if sc != nil && sc.sensitive(key) {
		return attribute.String(key, redacted), true
	}
	mask := func(s string) string {
		if sc == nil {
			return s
		}
		return sc.maskSecrets(s)
	}
	switch v := value.(type) {
	case string:
		return attribute.String(key, mask(v)), true
	case bool:
		return attribute.Bool(key, v), true
	case int:
		return attribute.Int64(key, int64(v)), true
	case int32:
		return attribute.Int64(key, int64(v)), true
	case int64:
		return attribute.Int64(key, v), true
	case float32:
		return attribute.Float64(key, float64(v)), true
	case float64:
		return attribute.Float64(key, v), true
	case time.Duration:
		return attribute.String(key, v.String()), true
	case nil:
		return attribute.Builder{}, false
	case error:
		return attribute.String(key, mask(v.Error())), true
	case fmt.Stringer:
		return attribute.String(key, mask(v.String())), true
	default:
		return attribute.String(key, mask(fmt.Sprint(v))), true
	}
}

// noopMeter is what a service without metrics gets: every call is free and no
// call site branches.
type noopMeter struct{}

var _ IMeter = noopMeter{}

func (noopMeter) Enabled() bool                                 { return false }
func (n noopMeter) WithContext(context.Context) IMeter          { return n }
func (noopMeter) Count(string, int64, ...MetricOption)          {}
func (noopMeter) Gauge(string, float64, ...MetricOption)        {}
func (noopMeter) Distribution(string, float64, ...MetricOption) {}
func (noopMeter) Duration(string, time.Duration, ...MetricOption) {
}
