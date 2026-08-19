// Package human formats quantities for display.
package human

import (
	"fmt"
	"time"
)

// Bytes renders a byte count compactly (2.0 KB, 5.0 MB, 3.0 GB).
func Bytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", value/unit)
}

// Time renders a timestamp for humans, empty-safe.
func Time(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
