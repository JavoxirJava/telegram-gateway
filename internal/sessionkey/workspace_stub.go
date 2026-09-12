//go:build !linux

package sessionkey

import "errors"

type Workspace struct {
	DatabaseDir string
	FilesDir    string
	DatabaseKey []byte
}

func OpenWorkspace(string, string, *Keyring) (*Workspace, error) {
	return nil, errors.New("TDLib session workspaces currently require Linux")
}
func (*Workspace) Close() error { return nil }
