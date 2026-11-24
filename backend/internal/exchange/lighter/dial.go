package lighter

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// goDNS is used when the VPS resolver does not answer. 1.1.1.1 first, 8.8.8.8 if that dial fails.
var goDNS = &net.Resolver{
	PreferGo: true,
	Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: 800 * time.Millisecond}
		c, err := d.DialContext(ctx, "udp", "1.1.1.1:53")
		if err == nil {
			return c, nil
		}
		return d.DialContext(ctx, "udp", "8.8.8.8:53")
	},
}

var ipCache struct {
	mu sync.Mutex
	ip map[string]string
}

func rememberIP(host, ip string) {
	if host == "" || ip == "" {
		return
	}
	ipCache.mu.Lock()
	defer ipCache.mu.Unlock()
	if ipCache.ip == nil {
		ipCache.ip = map[string]string{}
	}
	ipCache.ip[host] = ip
}

func cachedIP(host string) (string, bool) {
	ipCache.mu.Lock()
	defer ipCache.mu.Unlock()
	ip, ok := ipCache.ip[host]
	return ip, ok && ip != ""
}

func resolveHost(ctx context.Context, host string) (string, error) {
	sctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	ips, err := net.DefaultResolver.LookupHost(sctx, host)
	cancel()
	if err != nil || len(ips) == 0 {
		ictx, cancel2 := context.WithTimeout(ctx, 1200*time.Millisecond)
		ips, err = goDNS.LookupHost(ictx, host)
		cancel2()
	}
	if len(ips) == 0 {
		if ip, ok := cachedIP(host); ok {
			return ip, nil
		}
		if err == nil {
			err = fmt.Errorf("no addresses for %s", host)
		}
		return "", err
	}
	rememberIP(host, ips[0])
	return ips[0], nil
}

// DialContext resolves host with a short system lookup, then public DNS, then the last good IP.
// The final TCP connect uses an 800ms dial timeout. IP literals are dialed as-is.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	target := addr
	if net.ParseIP(host) == nil {
		ip, rerr := resolveHost(ctx, host)
		if rerr != nil {
			return nil, rerr
		}
		target = net.JoinHostPort(ip, port)
	}
	d := net.Dialer{Timeout: 800 * time.Millisecond, KeepAlive: 30 * time.Second}
	return d.DialContext(ctx, network, target)
}
