package lighter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
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
	dialIPTimeout := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: timeoutErr{},
	}
	connReset := &net.OpError{
		Op:  "read",
		Net: "tcp",
		Err: syscall.ECONNRESET,
	}
	brokenPipe := &net.OpError{
		Op:  "write",
		Net: "tcp",
		Err: errors.New("write: broken pipe"),
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
		{"net timeout read-phase", timeoutErr{}, true},
		{"url net timeout", &url.Error{Op: "Post", URL: "https://example", Err: timeoutErr{}}, true},
		{"dns i/o timeout", dnsTimeout, false},
		{"dial ip timeout", dialIPTimeout, false},
		{"url lookup string", &url.Error{Op: "Post", URL: "https://example", Err: errors.New("dial tcp: lookup mainnet.zklighter.elliot.ai: i/o timeout")}, false},
		{"eof", io.EOF, true},
		{"econnreset", syscall.ECONNRESET, true},
		{"epipe", syscall.EPIPE, true},
		{"op econnreset", connReset, true},
		{"op broken pipe", brokenPipe, true},
		{"url connection reset", &url.Error{Op: "Post", URL: "https://example", Err: errors.New("read: connection reset by peer")}, true},
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

func TestIsPreConnectFail(t *testing.T) {
	err := fmt.Errorf(`Post "https://mainnet.zklighter.elliot.ai/api/v1/sendTx": dial tcp: lookup mainnet.zklighter.elliot.ai: i/o timeout`)
	if !IsPreConnectFail(err) {
		t.Fatal("expected pre-connect")
	}
	if IsAmbiguousSubmit(err) {
		t.Fatal("DNS lookup must not be ambiguous")
	}
}
