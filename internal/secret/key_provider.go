package secret

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"strings"
)

type FileKeyProvider struct {
	path    string
	version int
}

func NewFileKeyProvider(path string, version int) *FileKeyProvider {
	return &FileKeyProvider{path: strings.TrimSpace(path), version: version}
}

func (p *FileKeyProvider) Current(ctx context.Context) (MasterKey, error) {
	return p.ByVersion(ctx, p.version)
}

func (p *FileKeyProvider) ByVersion(_ context.Context, version int) (MasterKey, error) {
	if p == nil || p.path == "" || version <= 0 || version != p.version {
		return MasterKey{}, ErrKeyUnavailable
	}
	body, err := os.ReadFile(p.path)
	if err != nil {
		return MasterKey{}, ErrKeyUnavailable
	}
	defer clear(body)
	material := bytes.TrimSpace(body)
	var decoded []byte
	if len(material) != 32 {
		var decodeErr error
		decoded, decodeErr = base64.StdEncoding.DecodeString(string(material))
		if decodeErr != nil {
			decoded, decodeErr = base64.RawStdEncoding.DecodeString(string(material))
		}
		if decodeErr != nil || len(decoded) != 32 {
			return MasterKey{}, ErrKeyUnavailable
		}
		material = decoded
	}
	defer clear(decoded)
	return MasterKey{Version: version, Material: append([]byte(nil), material...)}, nil
}

var _ KeyProvider = (*FileKeyProvider)(nil)
