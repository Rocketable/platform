package webui

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

const certValidity = 365 * 24 * time.Hour

type tlsAssets struct {
	certFile string
	keyFile  string
	ips      []net.IP
}

func prepareTLSAssets(workspace, listenAddr, certFile, keyFile string, collectIPv4Addrs func() ([]net.IP, error)) (tlsAssets, error) {
	return prepareTLSAssetsIn(workspace, config.DefaultWorkDir, listenAddr, certFile, keyFile, collectIPv4Addrs)
}

func prepareTLSAssetsIn(workspace, workDir, listenAddr, certFile, keyFile string, collectIPv4Addrs func() ([]net.IP, error)) (tlsAssets, error) {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return tlsAssets{}, fmt.Errorf("parse web UI listen address: %w", err)
	}

	ips, err := webUIIPv4Addrs(host, collectIPv4Addrs)
	if err != nil {
		return tlsAssets{}, err
	}

	assets := tlsAssets{certFile: certFile, keyFile: keyFile, ips: ips}
	if certFile != "" && keyFile != "" {
		if err := validateCertificatePair(certFile, keyFile, ips); err != nil {
			return tlsAssets{}, fmt.Errorf("validate web UI TLS certificate: %w", err)
		}

		return assets, nil
	}

	if strings.TrimSpace(workDir) == "" {
		workDir = config.DefaultWorkDir
	}

	dir := filepath.Join(workspace, workDir)

	assets.certFile, assets.keyFile = filepath.Join(dir, fallbackCertFilename), filepath.Join(dir, fallbackKeyFilename)
	if err := prepareFallbackCertificate(dir, assets.certFile, assets.keyFile, ips); err != nil {
		return tlsAssets{}, err
	}

	return assets, nil
}

func prepareFallbackCertificate(dir, certFile, keyFile string, ips []net.IP) error {
	certInfo, errCert := os.Stat(certFile)

	keyInfo, errKey := os.Stat(keyFile)
	switch {
	case errCert == nil && errKey == nil:
		if certInfo.Mode().Perm() != 0o600 {
			if err := os.Chmod(certFile, 0o600); err != nil {
				return fmt.Errorf("chmod web UI TLS certificate: %w", err)
			}
		}

		if keyInfo.Mode().Perm() != 0o600 {
			if err := os.Chmod(keyFile, 0o600); err != nil {
				return fmt.Errorf("chmod web UI TLS key: %w", err)
			}
		}

		if err := validateFallbackCertificatePair(certFile, keyFile, ips); err == nil {
			return nil
		}
	case errors.Is(errCert, os.ErrNotExist) && errors.Is(errKey, os.ErrNotExist):
	case errors.Is(errCert, os.ErrNotExist) || errors.Is(errKey, os.ErrNotExist):
		return errors.New("web UI fallback TLS certificate and key must both exist or both be absent")
	case errCert != nil:
		return fmt.Errorf("stat web UI TLS certificate: %w", errCert)
	case errKey != nil:
		return fmt.Errorf("stat web UI TLS key: %w", errKey)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create web UI TLS directory: %w", err)
	}

	return writeSelfSignedCertificate(certFile, keyFile, ips)
}

func validateCertificatePair(certFile, keyFile string, ips []net.IP) error {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load certificate pair: %w", err)
	}

	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse certificate: %w", err)
	}

	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return errors.New("certificate is expired or not yet valid")
	}

	for _, ip := range ips {
		if err := cert.VerifyHostname(ip.String()); err != nil {
			return fmt.Errorf("certificate does not cover %s: %w", ip, err)
		}
	}

	return nil
}

func validateFallbackCertificatePair(certFile, keyFile string, ips []net.IP) error {
	if err := validateCertificatePair(certFile, keyFile, ips); err != nil {
		return err
	}

	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load certificate pair: %w", err)
	}

	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse certificate: %w", err)
	}

	if cert.PublicKeyAlgorithm != x509.RSA {
		return errors.New("fallback certificate must use RSA for broad browser compatibility")
	}

	return nil
}

func writeSelfSignedCertificate(certFile, keyFile string, ips []net.IP) error {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("generate web UI TLS key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate web UI TLS serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ips,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("create web UI TLS certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal web UI TLS key: %w", err)
	}

	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), 0o600); err != nil {
		return fmt.Errorf("write web UI TLS certificate: %w", err)
	}

	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return fmt.Errorf("write web UI TLS key: %w", err)
	}

	return nil
}

func webUIIPv4Addrs(listenHost string, collectIPv4Addrs func() ([]net.IP, error)) ([]net.IP, error) {
	listenHost = strings.TrimSpace(listenHost)
	if listenHost == "" || listenHost == "0.0.0.0" {
		ips, err := collectIPv4Addrs()
		if err != nil {
			return nil, err
		}

		return appendUniqueIPv4([]net.IP{net.IPv4(127, 0, 0, 1)}, ips...), nil
	}

	addr, err := netip.ParseAddr(listenHost)
	if err == nil {
		if addr.Is6() {
			return nil, errors.New("web UI listen address must be IPv4-only")
		}

		return []net.IP{net.IP(addr.AsSlice()).To4()}, nil
	}

	resolved, err := net.LookupIP(listenHost)
	if err != nil {
		return nil, fmt.Errorf("resolve web UI listen host: %w", err)
	}

	var ips []net.IP

	for _, ip := range resolved {
		if ip4 := ip.To4(); ip4 != nil {
			ips = appendUniqueIPv4(ips, ip4)
		}
	}

	if len(ips) == 0 {
		return nil, errors.New("web UI listen host has no IPv4 address")
	}

	return ips, nil
}

func collectSystemInterfaceIPv4Addrs() ([]net.IP, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}

	var ips []net.IP

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("list addresses for interface %s: %w", iface.Name, err)
		}

		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil {
				ips = appendUniqueIPv4(ips, ip)
			}
		}
	}

	return ips, nil
}

func appendUniqueIPv4(ips []net.IP, candidates ...net.IP) []net.IP {
	for _, candidate := range candidates {
		ip := candidate.To4()
		if ip != nil && !slices.ContainsFunc(ips, func(existing net.IP) bool { return existing.Equal(ip) }) {
			ips = append(ips, append(net.IP(nil), ip...))
		}
	}

	return ips
}

func voiceModeURLs(ips []net.IP, port string) []string {
	urls := make([]string, 0, len(ips))
	for _, ip := range ips {
		urls = append(urls, "https://"+net.JoinHostPort(ip.String(), port)+VoiceModePath)
	}

	return urls
}
