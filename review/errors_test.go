package review

import (
	"errors"
	"fmt"
	"testing"
)

func TestWrapStep(t *testing.T) {
	t.Run("nil stays nil", func(t *testing.T) {
		if err := WrapStep(StepDiff, nil); err != nil {
			t.Fatalf("want nil, got: %v", err)
		}
	})

	t.Run("errors.Is works with a step name attached", func(t *testing.T) {
		err := WrapStep(StepDiff, fmt.Errorf("cannot resolve: %w", ErrRefNotFound))

		if !errors.Is(err, ErrRefNotFound) {
			t.Fatalf("cannot reach the sentinel error: %v", err)
		}
		if got := StepOf(err); got != StepDiff {
			t.Fatalf("StepOf = %q, want %q", got, StepDiff)
		}
	})

	t.Run("step name survives further wrapping", func(t *testing.T) {
		err := fmt.Errorf("pipeline: %w", WrapStep(StepPublish, errors.New("write failed")))

		if got := StepOf(err); got != StepPublish {
			t.Fatalf("StepOf = %q, want %q", got, StepPublish)
		}
	})
}

func TestStepOfWithoutStep(t *testing.T) {
	if got := StepOf(errors.New("plain error")); got != "" {
		t.Fatalf("StepOf = %q, want empty string", got)
	}
	if got := StepOf(nil); got != "" {
		t.Fatalf("StepOf(nil) = %q, want empty string", got)
	}
}

func TestStepErrorMessage(t *testing.T) {
	err := WrapStep(StepPrepare, errors.New("clone failed"))

	want := StepPrepare + " failed: clone failed"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}
