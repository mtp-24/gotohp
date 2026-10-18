package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

// errUploadStalled marks an upload attempt that stopped making progress.
var errUploadStalled = errors.New("upload stalled")

// uploadStallTimeout is how long an upload may send no bytes before the attempt
// is abandoned and retried. A variable so tests can shorten it.
var uploadStallTimeout = 2 * time.Minute

// isNetworkError reports whether err looks like a lost or broken connection
// rather than a rejection by the server. Certificate problems are excluded:
// waiting for the network would never fix them.
func isNetworkError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var certErr *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	if errors.As(err, &certErr) || errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameErr) || errors.As(err, &invalidCert) {
		return false
	}
	if errors.Is(err, errUploadStalled) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETDOWN) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// probeGooglePhotos checks that the upload host answers at all. Any HTTP
// response, whatever its status, means the connection is back.
func probeGooglePhotos(ctx context.Context, proxy string) error {
	client, err := NewHTTPClientWithProxy(proxy)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://photos.googleapis.com/", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("google photos unreachable: %w", err)
	}
	_ = resp.Body.Close()
	return nil
}

// watchUploadProgress calls onStall when reader has made no progress for
// timeout while bytes remain to be sent. The returned stop ends the watch.
func watchUploadProgress(reader *ProgressReader, total int64, timeout time.Duration, onStall func()) (stop func()) {
	done := make(chan struct{})
	go func() {
		interval := max(timeout/8, 10*time.Millisecond)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		last := reader.BytesRead()
		lastChange := time.Now()
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				read := reader.BytesRead()
				if read >= total {
					// Everything is sent; the response wait is bounded by the
					// transport's ResponseHeaderTimeout instead.
					return
				}
				if read != last {
					last, lastChange = read, now
					continue
				}
				if now.Sub(lastChange) >= timeout {
					onStall()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}
