package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWebTLSConfigDefaultsToHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	disabled := false
	for _, enabled := range []*bool{nil, &disabled} {
		cfg, err := webTLSConfig(path, enabled, "", "", nil)
		if err != nil || cfg != nil {
			t.Fatalf("expected an HTTP configuration, got %v, %v", cfg, err)
		}
	}
	bundle := filepath.Join(filepath.Dir(path), generatedTLSFile)
	if _, err := os.Stat(bundle); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("HTTP should leave TLS files untouched, stat returned %v", err)
	}
}

func TestWebTLSConfigGeneratesAndReusesCertificate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	enabled := true
	cfg, err := webTLSConfig(path, &enabled, "", "", []string{"router.test", "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("minimum TLS version is %#x", cfg.MinVersion)
	}
	der := cfg.Certificates[0].Certificate[0]
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	for _, name := range []string{"localhost", "127.0.0.1", "::1", "router.test", "192.0.2.1"} {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Errorf("verify certificate for %q: %v", name, err)
		}
	}

	bundle := filepath.Join(filepath.Dir(path), generatedTLSFile)
	info, err := os.Stat(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("private key file mode is %o", info.Mode().Perm())
	}
	reused, err := webTLSConfig(path, &enabled, "", "", nil)
	if err != nil || !bytes.Equal(der, reused.Certificates[0].Certificate[0]) {
		t.Fatalf("restart should reuse the generated certificate, error: %v", err)
	}

	customPath := filepath.Join(t.TempDir(), "config.toml")
	custom, err := webTLSConfig(customPath, nil, bundle, bundle, nil)
	if err != nil || !bytes.Equal(der, custom.Certificates[0].Certificate[0]) {
		t.Fatalf("legacy custom key pair should enable TLS, error: %v", err)
	}
	if _, err := webTLSConfig(customPath, &enabled, bundle, "", nil); err == nil {
		t.Fatal("an incomplete custom key pair should fail")
	}
}
