package oacstream

import "testing"

func TestNewConnectClientWithTLSRequiresCertAndKeyPair(t *testing.T) {
	t.Parallel()

	_, err := NewConnectClientWithTLS("https://example.com", "", TLSConfig{ClientCertPath: "/tmp/cert.pem"})
	if err == nil {
		t.Fatalf("expected error when only client cert is provided")
	}

	_, err = NewConnectClientWithTLS("https://example.com", "", TLSConfig{ClientKeyPath: "/tmp/key.pem"})
	if err == nil {
		t.Fatalf("expected error when only client key is provided")
	}
}
