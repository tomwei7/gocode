// +build windows

package cache

import (
	"strings"
)

// samePath checks two file paths for their equality based on the current filesystem
func samePath(a, b string) bool {
	return strings.EqualFold(a, b)
}
