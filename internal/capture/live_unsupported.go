//go:build !cgo || (!darwin && !linux)

package capture

// OpenLive reports that this platform/build has no live capture backend.
func OpenLive(LiveConfig) (Source, error) {
	return nil, ErrLiveCaptureUnavailable
}

// Interfaces reports that this platform/build has no live capture backend.
func Interfaces() ([]Interface, error) {
	return nil, ErrLiveCaptureUnavailable
}
