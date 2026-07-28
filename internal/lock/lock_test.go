package lock

import (
	"errors"
	"testing"
)

func TestAcquireIsExclusive(t *testing.T) {
	root := t.TempDir()

	release, err := Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(root); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy while held, got %v", err)
	}

	release()
	release2, err := Acquire(root)
	if err != nil {
		t.Fatalf("expected reacquire after release: %v", err)
	}
	release2()
}
