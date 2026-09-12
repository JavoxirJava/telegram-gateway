//go:build !tdlib || !cgo || !linux

package tdjson

import "errors"

func OpenNative(string) (Transport, error) {
	return nil, errors.New("native TDLib requires Linux, CGO_ENABLED=1 and -tags tdlib")
}
