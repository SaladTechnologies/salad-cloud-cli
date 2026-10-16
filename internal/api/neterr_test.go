package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// tlsGetWithCert serves one TLS endpoint with a self-signed cert valid for
// [notBefore, notAfter] and returns the error a trusting client gets.
func tlsGetWithCert(t *testing.T, notBefore, notAfter time.Time) error {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gw.example.test"},
		DNSNames:              []string{"gw.example.test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := client.Get(srv.URL + "/v1/models")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected a TLS verification error")
	}
	return err
}

func TestNetworkErrExpiredCert(t *testing.T) {
	expired := time.Date(2026, time.September, 28, 16, 55, 34, 0, time.UTC)
	err := tlsGetWithCert(t, expired.Add(-90*24*time.Hour), expired)

	now = func() time.Time { return expired.Add(5*time.Hour + 20*time.Minute) }
	defer func() { now = time.Now }()

	got := networkErr("network error", err)
	var ce *clierr.Error
	if !errors.As(got, &ce) || ce.Code != clierr.CodeNetwork || ce.Kind != clierr.KindNetwork {
		t.Fatalf("want network/%d, got %+v", clierr.CodeNetwork, got)
	}
	for _, want := range []string{
		`network error: Get "https://127.0.0.1:`,
		"TLS certificate for gw.example.test expired 2026-09-28T16:55:34Z (5h20m ago)",
		"the server must renew it",
	} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message %q missing %q", got.Message, want)
		}
	}
}

func TestNetworkErrNotYetValidCert(t *testing.T) {
	start := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	err := tlsGetWithCert(t, start, start.Add(90*24*time.Hour))
	got := networkErr("network error", err)
	want := "TLS certificate for gw.example.test is not valid until " + start.UTC().
		Format(time.RFC3339) +
		"; check this machine's clock"
	if !strings.Contains(got.Message, want) {
		t.Errorf("message %q missing %q", got.Message, want)
	}
}

func TestNetworkErrOtherFailuresUnchanged(t *testing.T) {
	got := networkErr("network error", errors.New("dial tcp: connection refused"))
	if got.Message != "network error: dial tcp: connection refused" {
		t.Errorf("got %q", got.Message)
	}
}
