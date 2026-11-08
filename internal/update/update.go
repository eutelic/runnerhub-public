package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Manifest struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

func (m Manifest) signedPayload() []byte {
	return []byte(m.Version + "\n" + m.URL + "\n" + strings.ToLower(m.SHA256) + "\n")
}
func VerifyManifest(m Manifest, key ed25519.PublicKey) error {
	if len(key) != ed25519.PublicKeySize {
		return errors.New("invalid update public key")
	}
	if m.Version == "" || m.URL == "" || len(m.SHA256) != 64 {
		return errors.New("incomplete update manifest")
	}
	if _, err := hex.DecodeString(m.SHA256); err != nil {
		return errors.New("manifest sha256 is not valid hex")
	}
	u, err := url.Parse(m.URL)
	if err != nil || u.Host == "" {
		return errors.New("invalid update url")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return errors.New("update url must use https")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return errors.New("invalid manifest signature encoding")
	}
	if !ed25519.Verify(key, m.signedPayload(), sig) {
		return errors.New("invalid update manifest signature")
	}
	return nil
}
func ParseManifest(data []byte, key ed25519.PublicKey) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	return m, VerifyManifest(m, key)
}

type Stager struct {
	HTTP      *http.Client
	PublicKey ed25519.PublicKey
	MaxBytes  int64
}

func (s Stager) Stage(m Manifest, dst string) error {
	if err := VerifyManifest(m, s.PublicKey); err != nil {
		return err
	}
	h := s.HTTP
	if h == nil {
		h = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := h.Get(m.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("update download returned %d", resp.StatusCode)
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".runnerhub-update-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0700); err != nil {
		_ = f.Close()
		return err
	}
	max := s.MaxBytes
	if max <= 0 {
		max = 512 << 20
	}
	hash := sha256.New()
	n, cp := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, max+1))
	if cp != nil {
		_ = f.Close()
		return cp
	}
	if n > max {
		_ = f.Close()
		return errors.New("update artifact exceeds maximum size")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(m.SHA256) {
		return errors.New("update sha256 mismatch")
	}
	return os.Rename(tmp, dst)
}
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
