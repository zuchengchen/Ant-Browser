//go:build !linux

package backend

import "context"

func browserPasswordAutosaveSupported() bool                   { return false }
func browserPasswordAutosaveLaunchArgs(args []string) []string { return args }
func (a *App) watchBrowserPasswordAutosave(context.Context)    {}
