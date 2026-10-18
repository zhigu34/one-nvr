package integration

import "github.com/zhigu34/one-nvr/internal/recording"

// scanBufferForTest exposes the package's directory scan to the integration
// tests without duplicating its parsing rules — a second implementation is how
// the parser and its tests drift apart.
func scanBufferForTest(root string) ([]recording.BufferFragment, error) {
	return recording.ScanBufferFragments(root)
}
