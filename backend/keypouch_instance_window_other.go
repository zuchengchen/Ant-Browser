//go:build (!linux && !darwin) || !cgo

package backend

func focusKeyPouchBrowserWindow(pid int) bool { return false }
