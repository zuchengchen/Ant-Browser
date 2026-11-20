//go:build !windows && (!linux || !cgo)
// +build !windows
// +build !linux !cgo

package backend

func profileWindowMarkerSupported() bool {
	return false
}

func runProfileWindowMarker(_ *App, marker *profileWindowMarker) {
	<-marker.stop
}
