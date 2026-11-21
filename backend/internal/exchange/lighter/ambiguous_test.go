package lighter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"testing"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestIsAmbiguousSubmit(t *testing.T) {
	dnsTimeout := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: &net.DNSError{Err: "i/o timeout", Name: "mainnet.zklighter.elliot.ai", IsTimeout: true},
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deadline", context.DeadlineExceeded, true},
		{"canceled", context.Canceled, true},
		{"wrapped deadline", fmt.Errorf("Post sendTx: %w", context.DeadlineExceeded), true},
		{"url deadline", &url.Error{Op: "Post", URL: "https://example/api/v1/sendTx", Err: context.DeadlineExceeded}, true},
		{"net timeout", timeoutErr{}, true},
		{"url net timeout", &url.Error{Op: "Post", URL: "https://example", Err: timeoutErr{}}, true},
		{"dns i/o timeout", dnsTimeout, true},
		{"eof", io.EOF, true},
		{"business code", fmt.Errorf("sendTx code=400 msg=invalid"), false},
		{"http status", fmt.Errorf("POST /api/v1/sendTx: HTTP 503: unavailable"), false},
		{"sign", fmt.Errorf("qty scales to 0"), false},
		{"plain", errors.New("nope"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAmbiguousSubmit(tc.err); got != tc.want {
				t.Fatalf("IsAmbiguousSubmit(%v)=%v want %v", tc.err, got, tc.want)
			}
		})
	}
}
