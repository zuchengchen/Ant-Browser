//go:build !windows

package singleinstance

func grantExistingSingleInstanceForeground(pid int) {}

func ActivateExistingWindow(pid int) {}

func FocusExistingWindow(pid int) bool { return false }
