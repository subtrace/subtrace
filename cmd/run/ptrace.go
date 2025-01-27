//go:build !linux

package run

func hasSysPtrace() (bool, error) {
	return false, nil
}
