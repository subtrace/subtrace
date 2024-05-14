// Copyright (c) Subtrace, Inc.
// SPDX-License-Identifier: BSD-3-Clause

package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"time"

	"subtrace.dev/journal"
)

var (
	hostname      string
	ephemeralCert *x509.Certificate
	ephemeralKey  *ecdsa.PrivateKey
)

// GenerateEpehemeralCA creates an in-memory ephemeral CA certificate and
// private key that will be used to transparently intercept, decrypt and
// re-encrypt outgoing TLS requests.
func GenerateEpehemeralCA() error {
	if ephemeralCert != nil || ephemeralKey != nil {
		return fmt.Errorf("ephemeral CA already exists")
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate private key: %w", err)
	}

	hostname, err = os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	name := fmt.Sprintf("Subtrace Ephemeral CA (generated on host: %q)", hostname)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(0),
		Subject:               pkix.Name{Organization: []string{name}, CommonName: name},
		NotBefore:             time.Now().AddDate(-10, 0, 0),
		NotAfter:              time.Now().AddDate(+10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	cert, err := x509.CreateCertificate(rand.Reader, template, template, priv.Public(), priv)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	block, _ := pem.Decode(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}))
	ephemeralCert, err = x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse certificate: %w", err)
	}

	privDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}

	ephemeralKey, err = x509.ParseECPrivateKey(privDER)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	return nil
}

// ref: https://serverfault.com/a/722646
var knownPEM = []string{
	"/etc/ssl/certs/ca-certificates.crt",                // Debian/Ubuntu/Gentoo etc.
	"/etc/pki/tls/certs/ca-bundle.crt",                  // Fedora/RHEL 6
	"/etc/ssl/ca-bundle.pem",                            // OpenSUSE
	"/etc/pki/tls/cacert.pem",                           // OpenELEC
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem", // CentOS/RHEL 7
	"/etc/ssl/cert.pem",                                 // Alpine Linux
}

// IsKnownPath returns true if the given path is a known root CA certificate
// path on a Linux distro.
func IsKnownPath(path string) bool {
	for i := range knownPEM {
		if path == knownPEM[i] {
			return true
		}
	}

	// TODO: support /etc/gnutls/config
	return false
}

// GetEphemeralCA returns the PEM-encoded ephemeral CA certificate bytes
// that should be appended to the system root CA certificate file.
func GetEphemeralCABytes() []byte {
	var b []byte
	b = append(b, "\n"...)
	b = append(b, fmt.Sprintf("# Subtrace Ephemeral CA (generated on host: %q)", hostname)...)
	b = append(b, "\n"...)
	b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ephemeralCert.Raw})...)
	b = append(b, "\n"...)
	return b
}

// newLeafCertificate generates an ephemeral X.509 leaf certificate for a TLS
// server that's similar to the TLS certificate received by the upstream
// client. The new certificate will be signed by the in-memory CA generated at
// process initialization.
func newLeafCertificate(orig *x509.Certificate) (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate key: %w", err)
	}

	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal private key: %w", err)
	}

	pub, err := x509.MarshalPKIXPublicKey(priv.Public())
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal public key: %w", err)
	}

	template := &x509.Certificate{
		RawSubjectPublicKeyInfo: pub,

		SerialNumber: orig.SerialNumber,
		Issuer:       orig.Issuer,
		Subject:      orig.Subject,
		NotBefore:    orig.NotBefore,
		NotAfter:     orig.NotAfter,
		KeyUsage:     orig.KeyUsage,

		DNSNames:       orig.DNSNames,
		EmailAddresses: orig.EmailAddresses,
		IPAddresses:    orig.IPAddresses,
		URIs:           orig.URIs,

		PermittedDNSDomainsCritical: orig.PermittedDNSDomainsCritical,
		PermittedDNSDomains:         orig.PermittedDNSDomains,
		ExcludedDNSDomains:          orig.ExcludedDNSDomains,
		PermittedIPRanges:           orig.PermittedIPRanges,
		ExcludedIPRanges:            orig.ExcludedIPRanges,
		PermittedEmailAddresses:     orig.PermittedEmailAddresses,
		ExcludedEmailAddresses:      orig.ExcludedEmailAddresses,
		PermittedURIDomains:         orig.PermittedURIDomains,
		ExcludedURIDomains:          orig.ExcludedURIDomains,

		ExtKeyUsage: orig.ExtKeyUsage,
	}

	leaf, err := x509.CreateCertificate(rand.Reader, template, ephemeralCert, priv.Public(), ephemeralKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create certificate: %w", err)
	}

	ret, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
	)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("x509 key pair: %w", err)
	}
	return ret, nil
}

func newConfigFromClientHello(chi *tls.ClientHelloInfo) *tls.Config {
	c := &tls.Config{
		ServerName: chi.ServerName,
		NextProtos: chi.SupportedProtos,
		MinVersion: uint16(0),
		MaxVersion: ^uint16(0),

		// It's not the proxy's job to verify the upstream server's certificate.
		// For example, if the upstream certificate has expired, the downstream
		// client would be rejecting it anyway.
		//
		// TODO: what if the upstream certificate is invalid for a different reason
		// (ex: signed by an unknown CA)? The ephemeral certificate we generate and
		// present to the downstream client would be valid.
		InsecureSkipVerify: true,
	}

	for _, v := range chi.SupportedVersions {
		if v < c.MinVersion {
			c.MinVersion = v
		}
		if v > c.MaxVersion {
			c.MaxVersion = v
		}
	}

	return c
}

// Handshake proxies a TLS handshake between upstream and downstream
// connections. It returns the plaintext version of each connection. It does
// not verify the validity of the TLS certificate presented by the upstream
// server.
func Handshake(downCipher, upCipher net.Conn) (*tls.Conn, *tls.Conn, *journal.TLSInfo, error) {
	info := &journal.TLSInfo{HandshakeBeginTime: time.Now().UnixNano()}

	var upPlain *tls.Conn
	downPlain := tls.Server(downCipher, &tls.Config{
		GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
			info.ServerName = chi.ServerName
			for _, cs := range chi.CipherSuites {
				info.ClientCipherSuites = append(info.ClientCipherSuites, int32(cs))
			}
			for _, v := range chi.SupportedVersions {
				info.ClientVersions = append(info.ClientVersions, int32(v))
			}

			slog.Debug("starting upstream TLS handshake", "serverName", chi.ServerName)
			upPlain = tls.Client(upCipher, newConfigFromClientHello(chi))
			if err := upPlain.Handshake(); err != nil {
				slog.Debug("upstream TLS handshake failed", "serverName", chi.ServerName, "error", err)
				return nil, fmt.Errorf("server handshake: %w", err)
			}
			slog.Debug("upstream TLS handshake complete", "serverName", chi.ServerName)

			cert, err := newLeafCertificate(upPlain.ConnectionState().PeerCertificates[0])
			if err != nil {
				return nil, fmt.Errorf("new server certificate: %w", err)
			}
			ret := &tls.Config{
				ServerName:   chi.ServerName,
				Certificates: []tls.Certificate{cert},
			}
			if proto := upPlain.ConnectionState().NegotiatedProtocol; proto != "" {
				ret.NextProtos = []string{proto}
			}
			if err := chi.SupportsCertificate(&cert); err != nil {
				return nil, fmt.Errorf("ClientHello does not support ephemeral server certificate: %w", err)
			}
			return ret, nil
		},
	})
	if err := downPlain.Handshake(); err != nil {
		return nil, nil, nil, fmt.Errorf("handshake downstream: %v", err)
	}

	info.HandshakeEndTime = time.Now().UnixNano()
	info.NegotiatedVersion = int32(upPlain.ConnectionState().Version)
	info.NegotiatedCipherSuite = int32(upPlain.ConnectionState().CipherSuite)
	info.NegotiatedProtocol = upPlain.ConnectionState().NegotiatedProtocol
	return downPlain, upPlain, info, nil
}
