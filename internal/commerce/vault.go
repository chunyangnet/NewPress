package commerce

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"newpress.local/newpress/internal/packagefs"
	"newpress.local/newpress/internal/payment"
	"os"
	"path/filepath"
)

type Vault struct{ aead cipher.AEAD }

func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, ErrInvalid
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(b)
	return &Vault{a}, e
}

// OpenVault keeps the installation key outside orders and never replaces a lost
// key. Back up this file together with the site's data.
func OpenVault(path string) (*Vault, error) {
	f, _, err := packagefs.OpenRegularNoSymlink(path)
	if err == nil {
		defer f.Close()
		b, e := io.ReadAll(io.LimitReader(f, 33))
		if e != nil {
			return nil, e
		}
		return NewVault(b)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err = packagefs.ValidateRegularFileTargetNoSymlink(path); err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return nil, err
	}
	f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return NewVault(b)
}
func (v *Vault) Seal(c payment.Config) (string, error) {
	b, e := json.Marshal(c)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return "", e
	}
	return base64.RawStdEncoding.EncodeToString(v.aead.Seal(nonce, nonce, b, []byte("newpress-payment-v1"))), nil
}
func (v *Vault) Open(sealed string) (payment.Config, error) {
	var c payment.Config
	b, e := base64.RawStdEncoding.DecodeString(sealed)
	n := v.aead.NonceSize()
	if e != nil || len(b) < n {
		return c, ErrUnavailable
	}
	plain, e := v.aead.Open(nil, b[:n], b[n:], []byte("newpress-payment-v1"))
	if e != nil {
		return c, ErrUnavailable
	}
	if e = json.Unmarshal(plain, &c); e != nil {
		return c, ErrUnavailable
	}
	return c, nil
}
