package lighter

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"
)

// IsPreConnectFail reports errors where the HTTP POST almost certainly never
// left the machine: DNS failure or TCP dial never completed. Those are clean
// rejects for submit idempotency — not ambiguous exchange state.
func IsPreConnectFail(err error) bool {
	if err == nil {
		return false
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		if oe.Op == "dial" {
			return true
		}
		if oe.Err != nil && IsPreConnectFail(oe.Err) {
			return true
		}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return IsPreConnectFail(ue.Err)
	}
	msg := strings.ToLower(err.Error())
	// Go dial errors: "dial tcp: lookup host: ..."
	if strings.Contains(msg, "lookup ") {
		return true
	}
	return false
}

// IsAmbiguousSubmit reports whether a submit/SendTx failure leaves exchange state
// unknown: the request may already have been accepted even though the client
// saw an error. Business rejects (HTTP 4xx/5xx body, sendTx code=…) are not
// ambiguous — those mean the venue answered. Pre-connect failures (DNS / dial)
// are also not ambiguous — the POST never reached the venue.
func IsAmbiguousSubmit(err error) bool {
	if err == nil {
		return false
	}
	if IsPreConnectFail(err) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNABORTED) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		if oe.Err != nil && IsAmbiguousSubmit(oe.Err) {
			return true
		}
		// OpError without Timeout() still often means the TCP stream died after
		// bytes may have been written (reset / broken pipe mid-response).
		if oe.Err != nil {
			msg := strings.ToLower(oe.Err.Error())
			if strings.Contains(msg, "connection reset") ||
				strings.Contains(msg, "broken pipe") ||
				strings.Contains(msg, "connection aborted") ||
				strings.Contains(msg, "use of closed network connection") {
				return true
			}
		}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return IsAmbiguousSubmit(ue.Err)
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection aborted") ||
		strings.Contains(msg, "tls: bad record mac") ||
		strings.Contains(msg, "use of closed network connection") {
		return true
	}
	return false
}
