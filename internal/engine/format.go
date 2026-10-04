package engine

import "fmt"

// HumanBytes renders a byte count the way the product sketch does:
// "20.1 GiB". Lives in engine so every layer (TUI, CLI printers,
// scanner labels) formats sizes identically.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
