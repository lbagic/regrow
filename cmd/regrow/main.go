// Command regrow scans the disk for regenerable caches and junk,
// explains what everything is and how it comes back, and reclaims
// space reversibly (dry-run → trash → undo).
package main

import "fmt"

// version is overridden at release time via -ldflags.
var version = "0.0.0-dev"

func main() {
	fmt.Println("regrow", version)
}
