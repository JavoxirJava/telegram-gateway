//go:build !linux || !cgo

package tdlib

import "errors"

func newTransport() (transport, error) {
	return nil, errors.New("TDLib requires Linux with CGO enabled")
}
