// +build !windows

package cache

// samePath checks two file paths for their equality based on the current filesystem
func samePath(a, b string) bool {
	return a == b
}
