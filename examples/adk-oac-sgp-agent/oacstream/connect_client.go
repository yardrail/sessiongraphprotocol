package oacstream

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

const (
	envelopeHeaderLen = 5
	flagData          = byte(0)
)

// Handler processes one orchestrator envelope and returns a harness response envelope.
type Handler func(context.Context, OrchestratorEnvelope) (HarnessEnvelope, error)

// ConnectClient maintains the OAC bidirectional stream over Connect protocol.
type ConnectClient struct {
	url        string
	authToken  string
	httpClient *http.Client
}

// TLSConfig controls optional TLS and mTLS settings for the outbound stream.
// If all fields are empty, default system TLS settings are used.
type TLSConfig struct {
	CACertPath     string
	ClientCertPath string
	ClientKeyPath  string
}

// NewConnectClient creates a Connect stream client for the OAC orchestrator service endpoint.
func NewConnectClient(url, authToken string) (*ConnectClient, error) {
	return NewConnectClientWithTLS(url, authToken, TLSConfig{})
}

// NewConnectClientWithTLS creates a Connect stream client with optional TLS/mTLS settings.
func NewConnectClientWithTLS(url, authToken string, tlsCfg TLSConfig) (*ConnectClient, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, errors.New("orchestrator URL is required")
	}

	httpClient, err := newHTTPClient(tlsCfg)
	if err != nil {
		return nil, err
	}

	return NewConnectClientWithHTTPClient(url, authToken, httpClient)
}

// NewConnectClientWithHTTPClient creates a Connect stream client using a caller-provided HTTP client.
func NewConnectClientWithHTTPClient(
	url, authToken string,
	httpClient *http.Client,
) (*ConnectClient, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, errors.New("orchestrator URL is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	return &ConnectClient{
		url:        url,
		authToken:  strings.TrimSpace(authToken),
		httpClient: httpClient,
	}, nil
}

func newHTTPClient(tlsCfg TLSConfig) (*http.Client, error) {
	if strings.TrimSpace(tlsCfg.CACertPath) == "" &&
		strings.TrimSpace(tlsCfg.ClientCertPath) == "" &&
		strings.TrimSpace(tlsCfg.ClientKeyPath) == "" {
		return &http.Client{}, nil
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	if strings.TrimSpace(tlsCfg.CACertPath) != "" {
		caPEM, err := os.ReadFile(strings.TrimSpace(tlsCfg.CACertPath))
		if err != nil {
			return nil, fmt.Errorf("read CA certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("parse CA certificate: no certificates found")
		}
		tlsConfig.RootCAs = pool
	}

	certPath := strings.TrimSpace(tlsCfg.ClientCertPath)
	keyPath := strings.TrimSpace(tlsCfg.ClientKeyPath)
	if certPath != "" || keyPath != "" {
		if certPath == "" || keyPath == "" {
			return nil, errors.New("both client cert and client key are required for mTLS")
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("load client certificate/key: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig

	return &http.Client{Transport: transport}, nil
}

// Run opens the stream and processes envelopes until context cancellation or stream closure.
func (client *ConnectClient) Run(ctx context.Context, handle Handler) error {
	if handle == nil {
		return errors.New("handler is required")
	}

	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.url, pr)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/connect+proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	if client.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+client.authToken)
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		_ = pw.CloseWithError(err)
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = pw.CloseWithError(fmt.Errorf("connect stream status %d", resp.StatusCode))
		return fmt.Errorf("connect stream status %d", resp.StatusCode)
	}

	writerMu := sync.Mutex{}
	writeEnvelope := func(envelope HarnessEnvelope) error {
		msg, wErr := MarshalHarnessEnvelope(envelope)
		if wErr != nil {
			return wErr
		}
		frame := make([]byte, envelopeHeaderLen+len(msg))
		frame[0] = flagData
		binary.BigEndian.PutUint32(frame[1:5], uint32(len(msg)))
		copy(frame[5:], msg)
		writerMu.Lock()
		defer writerMu.Unlock()
		_, wErr = pw.Write(frame)
		return wErr
	}

	reader := bufio.NewReader(resp.Body)
	for {
		select {
		case <-ctx.Done():
			_ = pw.CloseWithError(ctx.Err())
			return ctx.Err()
		default:
		}

		msg, readErr := readEnvelopeFrame(reader)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				_ = pw.Close()
				return nil
			}
			_ = pw.CloseWithError(readErr)
			return readErr
		}

		incoming, parseErr := UnmarshalOrchestratorEnvelope(msg)
		if parseErr != nil {
			_ = pw.CloseWithError(parseErr)
			return parseErr
		}

		outgoing, handleErr := handle(ctx, incoming)
		if handleErr != nil {
			outgoing = HarnessEnvelope{
				SessionID: incoming.SessionID,
				Result: &EventResult{
					Success:      false,
					ErrorMessage: handleErr.Error(),
				},
			}
		}

		if outgoing.SessionID == "" {
			outgoing.SessionID = incoming.SessionID
		}
		if outgoing.Result == nil {
			outgoing.Result = &EventResult{Success: true}
		}

		if err = writeEnvelope(outgoing); err != nil {
			_ = pw.CloseWithError(err)
			return err
		}

		if incoming.SessionEnd {
			_ = pw.Close()
			return nil
		}
	}
}

func readEnvelopeFrame(reader *bufio.Reader) ([]byte, error) {
	header := make([]byte, envelopeHeaderLen)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}

	flags := header[0]
	if flags&0x01 == 0x01 {
		return nil, errors.New("connect end-stream envelope received unexpectedly")
	}

	size := binary.BigEndian.Uint32(header[1:5])
	msg := make([]byte, int(size))
	if _, err := io.ReadFull(reader, msg); err != nil {
		return nil, err
	}

	return msg, nil
}
