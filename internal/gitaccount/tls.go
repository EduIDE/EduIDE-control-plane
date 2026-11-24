package gitaccount

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// RootCAs returns the system roots plus the PEM bundles in files, for a self-hosted Git host with a private CA.
// Without files it returns nil, which means "system roots" to crypto/tls.
func RootCAs(files ...string) (*x509.CertPool, error) {
	var set []string
	for _, f := range files {
		if f != "" {
			set = append(set, f)
		}
	}
	if len(set) == 0 {
		return nil, nil
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	for _, f := range set {
		pem, err := os.ReadFile(f) // #nosec G304 -- the path comes from the deployment config
		if err != nil {
			return nil, err
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no certificate", f)
		}
	}
	return pool, nil
}

// HTTPClient is the client for calls to a provider host: it trusts the CA of caFile (if set) and follows no
// redirect, so a request that carries a secret never leaves the configured host.
func HTTPClient(caFile string, timeout time.Duration) (*http.Client, error) {
	pool, err := RootCAs(caFile)
	if err != nil {
		return nil, errors.Join(errors.New("git provider CA"), err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // the host is reached directly; an environment proxy would see the credential
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
