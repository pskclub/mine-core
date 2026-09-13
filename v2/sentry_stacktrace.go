package core

import (
	"reflect"

	"github.com/getsentry/sentry-go"
)

// corePackagePath is this package's import path, which is what a frame of the
// framework carries as its module.
var corePackagePath = reflect.TypeOf(Error{}).PkgPath()

// withReporterStacktrace arranges for the event captured next on this scope to
// carry the stack of the code that reported the failure rather than the stack of
// the reporting itself.
//
// sentry-go builds a trace where CaptureException is called, for any error that
// does not carry one. By then the innermost frames are the framework —
// (*logger).Error, the log handler, the tracker — so the issue was named after
// sentry_log.go, the suspect commit was a commit of this repository, and the
// line that actually failed was five frames down. An *Error is unaffected: it
// captured its own stack where it was built (see captureStack), and carriesStack
// leaves it alone.
//
// The stack has to be taken here, at the report, and not inside the processor:
// by the time the processor runs, the call that reported is no longer on it.
func withReporterStacktrace(scope *sentry.Scope) {
	stack := reporterStacktrace()
	if stack == nil {
		return
	}
	scope.AddEventProcessor(func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
		useReporterStacktrace(event, hint, stack)
		return event
	})
}

// useReporterStacktrace puts stack on the event, unless the value it was built
// from carries one of its own.
func useReporterStacktrace(event *sentry.Event, hint *sentry.EventHint, stack *sentry.Stacktrace) {
	if event == nil {
		return
	}
	// a captured error is the hint's OriginalException, a panicked one its
	// RecoveredException; either may know where it came from
	if hint != nil && (carriesStack(hint.OriginalException) || carriesStack(hint.RecoveredException)) {
		return
	}

	// the outermost error is the one sentry-go fills a stack in for
	if n := len(event.Exception); n > 0 {
		event.Exception[n-1].Stacktrace = stack
		return
	}

	// a message event — CaptureMessage, or a panic whose value was not an error
	// — carries its stack on the thread instead
	for i := range event.Threads {
		if event.Threads[i].Stacktrace != nil {
			event.Threads[i].Stacktrace = stack
		}
	}
}

// carriesStack reports whether the value an event was built from brought a
// stack of its own — anything from go-errors, pkg/errors or this package's
// *Error. That one points at the failure; a stack taken at the report only
// points at the report.
func carriesStack(value any) bool {
	err, ok := value.(error)
	return ok && err != nil && sentry.ExtractStacktrace(err) != nil
}

// reporterStacktrace is the stack of the code that reported, with the
// framework's own frames below it dropped.
//
// Only the frames below the report go, and only up to the first frame that is
// not this package's: a failure raised inside the framework keeps the frames
// that led to it.
//
// A stack with no frame outside the framework at all — the job runner, the
// scheduler, an MQ consumer reporting from its own goroutine — returns nothing,
// which leaves the SDK's own trace in place. Trimming it to nothing would say
// less than that one does, and the frames this function itself stands on are no
// better a culprit than the ones it was meant to remove.
//
// On the panic path this is what puts the panicking line at the top: a deferred
// call runs on the stack that panicked, so the frames below the recovery are
// still there once the framework's own are gone.
func reporterStacktrace() *sentry.Stacktrace {
	stack := sentry.NewStacktrace()
	if stack == nil {
		return nil
	}
	// frames run oldest first, so the report is at the end
	end := len(stack.Frames)
	for end > 0 && stack.Frames[end-1].Module == corePackagePath {
		end--
	}
	if end == 0 {
		return nil
	}
	stack.Frames = stack.Frames[:end]
	return stack
}
