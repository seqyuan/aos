// Package human 提供字节数与速率的人类可读格式化。
package human

import (
	"fmt"
	"time"
)

// Size 将字节数格式化为可读字符串，例如 1.5MB。
func Size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Rate 每秒传输速率。
func Rate(bytes int64, d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return Size(int64(float64(bytes)/d.Seconds())) + "/s"
}
