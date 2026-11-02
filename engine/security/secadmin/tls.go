/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package secadmin

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"time"

	"brique_engine/shared"
)

// The TLS material of the HTTPS interface lives with the keys, on the machine, in
// <machine>/tls: the engine looks there when the configuration of the interface names no file,
// so nothing that belongs to one machine is written in the (versioned) context.json.
const (
	TLSDirectory    = "tls"
	TLSCertFile     = "tls/cert.pem"
	TLSKeyFile      = "tls/key.pem"
	TLSTrustedFile  = "tls/trusted.pem"
	defaultTLSDays  = 365
	maxTLSDays      = 3650
	maxCertFileSize = 256 << 10
)

// TLSInfo describes the certificate of the machine.
type TLSInfo struct {
	CertPath, KeyPath string
	Hosts             []string
	NotAfter          time.Time
	Fingerprint       string
	Trusted           int
}

// TLSOptions configures TLSInit.
type TLSOptions struct {
	// Hosts are the names and addresses the certificate is valid for (at least one).
	Hosts []string
	// Days is the validity; defaultTLSDays when zero.
	Days int
	// Force replaces an existing certificate and key.
	Force bool
}

// TLSInit makes a self-signed certificate and its private key for the HTTPS interface. The
// key is created here and never leaves the machine; the certificate is what peers are given.
func (d *Dir) TLSInit(options TLSOptions) (*TLSInfo, error) {
	if err := d.requireInitialized(); err != nil {
		return nil, err
	}
	hosts, err := cleanHosts(options.Hosts)
	if err != nil {
		return nil, err
	}
	days := options.Days
	if days == 0 {
		days = defaultTLSDays
	}
	if days < 1 || days > maxTLSDays {
		return nil, fmt.Errorf("the validity must be between 1 and %d days", maxTLSDays)
	}
	if !options.Force {
		if _, err := os.Stat(d.abs(TLSCertFile)); err == nil {
			return nil, errors.New("a certificate already exists; --force replaces it (peers must trust the new one)")
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, err
	}
	now := d.now()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: hosts[0], Organization: []string{"Brique"}},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(0, 0, days),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	defer shared.ZeroBytes(keyDER)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	defer shared.ZeroBytes(keyPEM)
	// Key first: a reader that sees the new certificate always finds a key at least as new.
	if err := d.writeFile(TLSKeyFile, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := d.writeFile(TLSCertFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	return d.TLSShow()
}

// TLSShow describes the certificate of the machine.
func (d *Dir) TLSShow() (*TLSInfo, error) {
	content, err := d.readProtected(TLSCertFile, maxCertFileSize)
	if err != nil {
		return nil, fmt.Errorf("no certificate on this machine: %w", err)
	}
	certificates, err := parseCertificates(content)
	if err != nil || len(certificates) == 0 {
		return nil, errors.New("the certificate file holds no certificate")
	}
	leaf := certificates[0]
	info := &TLSInfo{CertPath: d.abs(TLSCertFile), KeyPath: d.abs(TLSKeyFile), NotAfter: leaf.NotAfter, Fingerprint: fingerprint(leaf)}
	info.Hosts = append(info.Hosts, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		info.Hosts = append(info.Hosts, ip.String())
	}
	if trusted, err := d.readProtected(TLSTrustedFile, maxCertFileSize); err == nil {
		if list, err := parseCertificates(trusted); err == nil {
			info.Trusted = len(list)
		}
	}
	return info, nil
}

// TLSTrust adds the certificates of a peer to the ones this machine accepts from the HTTPS
// interfaces it calls. It returns how many were new.
func (d *Dir) TLSTrust(certificateFile string) (added int, err error) {
	if err := d.requireInitialized(); err != nil {
		return 0, err
	}
	raw, err := os.ReadFile(certificateFile)
	if err != nil || len(raw) > maxCertFileSize {
		return 0, fmt.Errorf("cannot read the certificate file %q", certificateFile)
	}
	offered, err := parseCertificates(raw)
	if err != nil || len(offered) == 0 {
		return 0, fmt.Errorf("%q holds no certificate", certificateFile)
	}
	var kept []*x509.Certificate
	if existing, readErr := d.readProtected(TLSTrustedFile, maxCertFileSize); readErr == nil {
		if kept, err = parseCertificates(existing); err != nil {
			return 0, fmt.Errorf("the file of trusted certificates is unreadable: %w", err)
		}
	}
	known := map[string]bool{}
	for _, certificate := range kept {
		known[fingerprint(certificate)] = true
	}
	for _, certificate := range offered {
		if !known[fingerprint(certificate)] {
			known[fingerprint(certificate)] = true
			kept = append(kept, certificate)
			added++
		}
	}
	var out bytes.Buffer
	for _, certificate := range kept {
		out.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	}
	return added, d.writeFile(TLSTrustedFile, out.Bytes(), 0o600)
}

func (d *Dir) requireInitialized() error {
	if d == nil || d.Root == "" {
		return errors.New("the machine's directory is required")
	}
	if _, err := os.Stat(d.abs(d.bootstrapRel(""))); err != nil {
		return errors.New("this machine has no security directory yet: run init first")
	}
	return nil
}

func cleanHosts(hosts []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, entry := range hosts {
		for _, host := range strings.Split(entry, ",") {
			host = strings.TrimSpace(host)
			if host == "" || seen[host] {
				continue
			}
			if strings.ContainsAny(host, " /\\:") && net.ParseIP(host) == nil {
				return nil, fmt.Errorf("%q is not a host name or an address", host)
			}
			seen[host] = true
			out = append(out, host)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("give at least one --host: the name or address peers will use to reach this machine")
	}
	return out, nil
}

func parseCertificates(content []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	for {
		var block *pem.Block
		block, content = pem.Decode(content)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, certificate)
	}
	return out, nil
}

func fingerprint(certificate *x509.Certificate) string {
	sum := sha256.Sum256(certificate.Raw)
	return hex.EncodeToString(sum[:])
}
