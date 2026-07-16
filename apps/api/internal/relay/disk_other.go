//go:build !unix

package relay

func freeBytes(path string) (uint64, error) {
	return 0, nil
}
