package lighter

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
)

// IsAmbiguousSubmit reports whether a submit/SendTx failure leaves exchange state
// unknown: the request may already have been accepted even though the client
// saw an error. Business rejects (HTTP 4xx/5xx body, sendTx code=…) are not
// ambiguous — those mean the venue answered.
func IsAmbiguousSubmit(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return IsAmbiguousSubmit(ue.Err)
	}
	return false
}
