package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"newpress.local/newpress/internal/packagefs"
)

// The key is a deployment trust anchor. It must never come from the release repository.
const manifestSchema = 2
const LauncherGeneration = "2"
const CoreProduct = "newpress-core"

// Official updates trust the key shipped with the installed application. Never
// fetch a replacement trust anchor from the release being verified.
//
//go:embed official_ed25519.pub
var officialPublisherPublicKey string

type SignedManifest struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

type trustCheckpoint struct {
	Sequence int64  `json:"sequence"`
	Digest   string `json:"digest"`
}

func publisherKey(source Source) (ed25519.PublicKey, error) {
	encoded := strings.TrimSpace(os.Getenv("NEWPRESS_UPDATE_PUBLIC_KEY"))
	if encoded == "" && source.Official {
		encoded = strings.TrimSpace(officialPublisherPublicKey)
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("请在部署环境配置核心发布者的 Ed25519 公钥 NEWPRESS_UPDATE_PUBLIC_KEY")
	}
	return ed25519.PublicKey(key), nil
}

func decodeSigned(r io.Reader, key ed25519.PublicKey) (Available, error) {
	env, err := packagefs.DecodeJSON[SignedManifest](r, 128<<10, "core release")
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, env.Payload, env.Signature) {
		return Available{}, errors.New("核心发布清单签名无效")
	}
	manifest, err := packagefs.DecodeJSON[Manifest](bytes.NewReader(env.Payload), 64<<10, "core manifest")
	if err != nil || manifest.Schema != manifestSchema || manifest.Product != CoreProduct || manifest.Sequence < 1 || !manifest.Expires.After(time.Now()) {
		return Available{}, errors.New("核心发布清单格式、用途或有效期不正确")
	}
	return Available{Manifest: manifest, PayloadHash: digest(env.Payload), KeyID: digest(key)}, nil
}

// Pins the highest authentic sequence, including an announcement withdrawing a release.
// This is intentionally independent of tokens, branches, and the disposable check cache.
func (m *Manager) discover(ctx context.Context, s Source) (Available, error) {
	key, err := publisherKey(s)
	if err != nil {
		return Available{}, err
	}
	a, err := m.client.check(ctx, s, key)
	if err != nil {
		return Available{}, err
	}
	filename := filepath.Join(m.root, "trust-"+a.KeyID+".json")
	var old trustCheckpoint
	if err := readJSON(filename, &old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Available{}, errors.New("无法读取发布序号记录")
	}
	if old.Sequence > a.Manifest.Sequence || old.Sequence == a.Manifest.Sequence && old.Digest != a.PayloadHash {
		return Available{}, errors.New("发布序号已回退或同一序号的内容已改变")
	}
	if old.Sequence < a.Manifest.Sequence {
		if err := writeJSON(filename, trustCheckpoint{a.Manifest.Sequence, a.PayloadHash}); err != nil {
			return Available{}, errors.New("无法保存发布序号记录")
		}
	}
	if _, err := a.Manifest.artifact(); err != nil {
		return Available{}, err
	}
	return a, nil
}

func sourceFingerprint(s Source) string { body, _ := json.Marshal(s); return digest(body) }
func (a Available) revision() string {
	if len(a.Mirrors) > 0 {
		// Public mirrors may resolve different commits containing the same signed
		// release. Approval follows its authenticated bytes, not the fastest host.
		return digest([]byte(a.KeyID + "/" + a.PayloadHash))
	}
	return digest([]byte(a.KeyID + "/" + a.Commit + "/" + a.PayloadHash))
}
func (m *Manager) cached(s Source) (Available, bool) {
	var a Available
	key, err := publisherKey(s)
	if err != nil || readJSON(filepath.Join(m.root, "available.json"), &a) != nil || a.KeyID != digest(key) || a.Fingerprint != sourceFingerprint(s) || !a.Manifest.Expires.After(time.Now()) || !commitPattern.MatchString(a.Commit) || !digestPattern.MatchString(a.PayloadHash) || !newer(a.Manifest.Version, m.current) {
		return Available{}, false
	}
	var high trustCheckpoint
	if readJSON(filepath.Join(m.root, "trust-"+a.KeyID+".json"), &high) != nil || high.Sequence != a.Manifest.Sequence || high.Digest != a.PayloadHash {
		return Available{}, false
	}
	return a, true
}
func (m *Manager) invalidate() error {
	if err := os.Remove(filepath.Join(m.root, "available.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("无法撤销旧的安装授权")
	}
	return nil
}
