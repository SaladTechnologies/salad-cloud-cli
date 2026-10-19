package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// now is swapped in tests.
var now = time.Now

// transportErr is the error for a request that got no response: the
// canceled-request error when Ctrl-C canceled ctx (it is not a network
// failure), otherwise a network error.
func transportErr(ctx context.Context, prefix string, err error) *clierr.Error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return clierr.Canceled()
	}
	return networkErr(prefix, err)
}

// readErr is transportErr for a response body that could not be read.
func readErr(ctx context.Context, what string, err error) *clierr.Error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return clierr.Canceled()
	}
	return clierr.New(clierr.CodeNetwork, clierr.KindNetwork, "network error reading %s: %v", what, err)
}

// networkErr wraps a transport failure as a network error. A certificate
// outside its validity window gets a message naming the host and the date:
// the Windows system verifier leaves x509's detail empty, which otherwise
// prints as a bare "certificate has expired or is not yet valid: ".
func networkErr(prefix string, err error) *clierr.Error {
	if msg, ok := certWindowMessage(err); ok {
		return clierr.New(clierr.CodeNetwork, clierr.KindNetwork, "%s: %s", prefix, msg)
	}
	return clierr.New(clierr.CodeNetwork, clierr.KindNetwork, "%s: %v", prefix, err)
}

func certWindowMessage(err error) (string, bool) {
	var verr *tls.CertificateVerificationError
	if !errors.As(err, &verr) || len(verr.UnverifiedCertificates) == 0 {
		return "", false
	}
	leaf := verr.UnverifiedCertificates[0]
	host := leaf.Subject.CommonName
	if len(leaf.DNSNames) > 0 {
		host = leaf.DNSNames[0]
	}
	where := ""
	var uerr *url.Error
	if errors.As(err, &uerr) {
		where = fmt.Sprintf(`%s "%s": `, uerr.Op, uerr.URL)
	}
	t := now().UTC()
	switch {
	case t.After(leaf.NotAfter):
		return fmt.Sprintf(
			"%sTLS certificate for %s expired %s (%s ago); the server must renew it",
			where,
			host,
			leaf.NotAfter.UTC().Format(time.RFC3339),
			strings.TrimSuffix(t.Sub(leaf.NotAfter).Round(time.Minute).String(), "0s"),
		), true
	case t.Before(leaf.NotBefore):
		return fmt.Sprintf("%sTLS certificate for %s is not valid until %s; check this machine's clock",
			where, host, leaf.NotBefore.UTC().Format(time.RFC3339)), true
	}
	return "", false
}
