package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const generatedTLSFile = "modemd-tls.pem"

func webTLSConfig(configPath string, enabled *bool, certFile, keyFile string, names []string) (*tls.Config, error) {
	useTLS := certFile != "" || keyFile != ""
	if enabled != nil {
		useTLS = *enabled
	}
	if !useTLS {
		return nil, nil
	}

	var cert tls.Certificate
	var err error
	switch {
	case certFile == "" && keyFile == "":
		cert, err = loadOrGenerateCertificate(filepath.Join(filepath.Dir(configPath), generatedTLSFile), names)
	case certFile != "" && keyFile != "":
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
	default:
		return nil, errors.New("set both [server] tls_cert_file and tls_key_file, or leave both empty")
	}
	if err != nil {
		return nil, fmt.Errorf("load [server] TLS key pair: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}, nil
}

func loadOrGenerateCertificate(path string, names []string) (tls.Certificate, error) {
	bundle, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := generateCertificate(path, names); err != nil {
			return tls.Certificate{}, err
		}
		bundle, err = os.ReadFile(path)
	}
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(bundle, bundle)
}

func generateCertificate(path string, names []string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber:          serial.Add(serial, big.NewInt(1)),
		Subject:               pkix.Name{CommonName: "modemd"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	names = append([]string{"localhost", "127.0.0.1", "::1"}, names...)
	if host, err := os.Hostname(); err == nil {
		names = append(names, host)
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			name, _, _ := strings.Cut(addr.String(), "/")
			names = append(names, name)
		}
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if ip := net.ParseIP(name); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, name)
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}

	// Publish the complete pair once, without replacing another process's pair.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".modemd-tls-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	for _, block := range []*pem.Block{{Type: "CERTIFICATE", Bytes: certDER}, {Type: "PRIVATE KEY", Bytes: keyDER}} {
		if err := pem.Encode(tmp, block); err != nil {
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}
