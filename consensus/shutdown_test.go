package consensus

import (
	"context"
	"errors"
	"testing"
)

func TestEngineStoppedErrorPreservesCause(t *testing.T) {
	cause := context.Canceled
	err := NewEngineStoppedError(cause)
	if !errors.Is(err, ErrEngineStopped) {
		t.Fatal("typed shutdown abort does not match ErrEngineStopped")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("typed shutdown abort discarded its cancellation cause")
	}
	var typed *EngineStoppedError
	if !errors.As(err, &typed) {
		t.Fatalf("shutdown abort has type %T, want *EngineStoppedError", err)
	}
}

func TestEngineStoppedErrorDoesNotClassifyOtherFailures(t *testing.T) {
	if errors.Is(context.Canceled, ErrEngineStopped) {
		t.Fatal("a bare context cancellation is not proof of engine-owned shutdown")
	}
	if errors.Is(errors.New("invalid receipt root"), ErrEngineStopped) {
		t.Fatal("validation failure classified as engine shutdown")
	}
}

func TestEngineStoppedErrorHandlesNil(t *testing.T) {
	err := NewEngineStoppedError(nil)
	if !errors.Is(err, ErrEngineStopped) || err.Error() == "" {
		t.Fatal("nil cause did not produce a usable shutdown error")
	}
	var nilErr *EngineStoppedError
	if nilErr.Error() == "" {
		t.Fatal("nil receiver returned an empty error string")
	}
	if nilErr.Unwrap() != nil {
		t.Fatal("nil receiver returned a non-nil cause")
	}
}
