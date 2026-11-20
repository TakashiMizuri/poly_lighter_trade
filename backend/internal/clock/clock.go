package clock

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// MaxTrustedRTT: a 5s API hang produces a fake multi-second offset via midpoint.
const MaxTrustedRTT = 800 * time.Millisecond

const WindowSec int64 = 300

func WindowTS(unix int64) int64 {
	if unix < 0 {
		return 0
	}
	return unix - unix%WindowSec
}

func WindowOf(t time.Time) int64 {
	return WindowTS(t.Unix())
}

func NextWindow(t time.Time) int64 {
	ts := WindowOf(t)
	if t.Unix() == ts {
		return ts
	}
	return ts + WindowSec
}

// SyncedNow is local wall clock plus measured offset (server - local).
type SyncedNow struct {
	Offset time.Duration
}

func (s SyncedNow) Now() time.Time {
	return time.Now().Add(s.Offset)
}

func (s SyncedNow) Unix() float64 {
	return float64(s.Now().UnixNano()) / 1e9
}

func SleepUntil(ctx context.Context, deadline time.Time, nowFn func() time.Time) error {
	if nowFn == nil {
		nowFn = time.Now
	}
	for {
		n := nowFn()
		if !n.Before(deadline) {
			return nil
		}
		d := deadline.Sub(n)
		if d > 250*time.Millisecond {
			d = d - 80*time.Millisecond
			if d > 500*time.Millisecond {
				d = 500 * time.Millisecond
			}
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// OffsetFromHTTPDate parses the Date header as an estimate of server-local offset.
func OffsetFromHTTPDate(dateHdr string, local time.Time) (time.Duration, error) {
	if dateHdr == "" {
		return 0, errNoDate
	}
	t, err := http.ParseTime(dateHdr)
	if err != nil {
		return 0, err
	}
	return t.UTC().Sub(local.UTC()), nil
}

var errNoDate = errString("empty Date header")

type errString string

func (e errString) Error() string { return string(e) }

type OffsetSample struct {
	Offset time.Duration
	RTT    time.Duration
}

func (s OffsetSample) Trusted() bool {
	return s.RTT > 0 && s.RTT <= MaxTrustedRTT
}

func defaultClockHTTP() *http.Client {
	return &http.Client{Timeout: 1200 * time.Millisecond}
}

func MeasureBinanceOffset(ctx context.Context, client *http.Client, baseURL string) (OffsetSample, error) {
	if client == nil {
		client = defaultClockHTTP()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v3/time", nil)
	if err != nil {
		return OffsetSample{}, err
	}
	t0 := time.Now()
	resp, err := client.Do(req)
	t1 := time.Now()
	rtt := t1.Sub(t0)
	if err != nil {
		return OffsetSample{RTT: rtt}, err
	}
	defer resp.Body.Close()
	var buf [128]byte
	n, _ := resp.Body.Read(buf[:])
	ms, err := parseServerTimeMS(string(buf[:n]))
	if err != nil {
		off, err2 := OffsetFromHTTPDate(resp.Header.Get("Date"), t0.Add(rtt/2))
		return OffsetSample{Offset: off, RTT: rtt}, err2
	}
	mid := t0.Add(rtt / 2)
	server := time.UnixMilli(ms)
	return OffsetSample{Offset: server.Sub(mid), RTT: rtt}, nil
}

func MedianTrustedOffset(samples []OffsetSample) (OffsetSample, bool) {
	var good []OffsetSample
	for _, s := range samples {
		if s.Trusted() {
			good = append(good, s)
		}
	}
	if len(good) == 0 {
		return OffsetSample{}, false
	}
	sort.Slice(good, func(i, j int) bool { return good[i].Offset < good[j].Offset })
	return good[len(good)/2], true
}

// MeasureBinanceOffsetStable takes n probes and returns the median of those with RTT ≤ MaxTrustedRTT.
func MeasureBinanceOffsetStable(ctx context.Context, client *http.Client, baseURL string, n int) (OffsetSample, error) {
	if n < 1 {
		n = 1
	}
	samples := make([]OffsetSample, 0, n)
	var last error
	for i := 0; i < n; i++ {
		s, err := MeasureBinanceOffset(ctx, client, baseURL)
		if err != nil {
			last = err
			continue
		}
		samples = append(samples, s)
	}
	med, ok := MedianTrustedOffset(samples)
	if !ok {
		if last != nil {
			return OffsetSample{}, last
		}
		return OffsetSample{}, errString("binance time RTT untrusted")
	}
	return med, nil
}

func parseServerTimeMS(s string) (int64, error) {
	const key = `"serverTime":`
	i := indexOf(s, key)
	if i < 0 {
		return 0, errString("no serverTime")
	}
	s = s[i+len(key):]
	j := 0
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	s = s[j:]
	k := 0
	for k < len(s) && s[k] >= '0' && s[k] <= '9' {
		k++
	}
	return strconv.ParseInt(s[:k], 10, 64)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
