package human

import (
	"testing"
	"time"
)

func TestBytes(t *testing.T) {
	cases := map[int64]string{
		512:           "512 B",
		2048:          "2.0 KB",
		5 << 20:       "5.0 MB",
		3 << 30:       "3.0 GB",
		0:             "0 B",
		1<<20 + 1<<19: "1.5 MB",
	}
	for in, want := range cases {
		if got := Bytes(in); got != want {
			t.Errorf("Bytes(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestTime(t *testing.T) {
	if got := Time(time.Time{}); got != "" {
		t.Fatalf("zero time must be empty, got %q", got)
	}
	stamp := time.Date(2026, 8, 1, 12, 30, 45, 0, time.Local)
	if got := Time(stamp); got != "2026-08-01 12:30:45" {
		t.Fatalf("Time = %q", got)
	}
}
