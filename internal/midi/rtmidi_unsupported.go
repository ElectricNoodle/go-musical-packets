//go:build !cgo || (!darwin && !linux)

package midi

func newNativeDriver() (Driver, error) {
	return nil, ErrDriverUnavailable
}
