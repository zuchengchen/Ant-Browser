//go:build !windows

package keypouch

// The parent directory and the seed file are accessible only to the current user.
func protectDeviceSeed(seed []byte) ([]byte, error)   { return seed, nil }
func unprotectDeviceSeed(seed []byte) ([]byte, error) { return seed, nil }
