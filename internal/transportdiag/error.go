package transportdiag

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

// Classify returns an allowlisted label, never raw error text.
// Network errors may contain credential-bearing proxy URLs or request data.
func Classify(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns_error"
	}
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &cert) || errors.As(err, &unknown) {
		return "tls_certificate_error"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection_refused"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "connection_reset"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected_eof"
	}
	if err == nil {
		return "stream_incomplete"
	}
	// Only fixed labels leave this function; the matching input is never logged.
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "tls handshake timeout") {
		return "tls_handshake_timeout"
	}
	if strings.Contains(message, "tls:") {
		return "tls_error"
	}
	if strings.Contains(message, "http2:") {
		return "http2_error"
	}
	if strings.Contains(message, "timeout awaiting response headers") {
		return "response_header_timeout"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return "dial_error"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "network_timeout"
	}
	return "transport_error"
}
