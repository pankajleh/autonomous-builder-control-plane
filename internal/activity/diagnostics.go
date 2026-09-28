package activity

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

// stepError names the collector or binding step that rejected provider detail.
// It only annotates diagnostics: errors.Is still reaches the wrapped sentinel,
// so the fail-closed classification of every path is unchanged.
type stepError struct {
	step string
	err  error
}

func (e stepError) Error() string { return e.step + ": " + e.err.Error() }
func (e stepError) Unwrap() error { return e.err }

func atStep(step string, err error) error {
	if err == nil {
		return nil
	}
	return stepError{step: step, err: err}
}

// annotate records step plus any steps already recorded on cause, while
// classifying the result as class exactly as the unannotated path did.
func annotate(step string, cause, class error) error {
	if inner := diagnosticStep(cause); inner != "" {
		step += "/" + inner
	}
	return stepError{step: step, err: class}
}

// diagnosticStep returns the nested step names recorded on err, outermost
// first, or "" when none were recorded.
func diagnosticStep(err error) string {
	var parts []string
	for err != nil {
		var s stepError
		if !errors.As(err, &s) {
			break
		}
		parts = append(parts, s.step)
		err = s.err
	}
	return strings.Join(parts, "/")
}

func diagnosticClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, ErrIntegrity):
		return "integrity"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	case errors.Is(err, ErrExhausted):
		return "exhausted"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "interrupted"
	}
	return "other"
}

// diagnose writes one operator diagnostic line when a controller marker is
// recorded. Every field is a controller-owned token: no provider text, paths,
// credentials or raw error strings are emitted.
func (s *Service) diagnose(run, marker string, err error) {
	if s.diagnostics == nil {
		return
	}
	step := diagnosticStep(err)
	if step == "" {
		step = "unspecified"
	}
	if !runtimecatalog.ValidIdentifier(run) {
		run = "invalid"
	}
	fmt.Fprintf(s.diagnostics, "abcp activity marker at=%s run=%s marker=%s step=%s class=%s\n",
		stamp(s.now()), run, marker, step, diagnosticClass(err))
}

// noteGap logs a provider replay gap by source event number only; no provider
// text is written. last is math.MaxUint64 before the first event.
func (s *Service) noteGap(run string, last, got uint64, confirmed bool) {
	if s.diagnostics == nil {
		return
	}
	if !runtimecatalog.ValidIdentifier(run) {
		run = "invalid"
	}
	after := "none"
	if last != math.MaxUint64 {
		after = strconv.FormatUint(last, 10)
	}
	fmt.Fprintf(s.diagnostics, "abcp activity replay gap at=%s run=%s after=%s got=%d confirmed=%t\n", stamp(s.now()), run, after, got, confirmed)
}
