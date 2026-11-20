package clock

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

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

func MeasureBinanceOffset(ctx context.Context, client *http.Client, baseURL string) (time.Duration, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v3/time", nil)
	if err != nil {
		return 0, err
	}
	t0 := time.Now()
	resp, err := client.Do(req)
	t1 := time.Now()
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var buf [128]byte
	n, _ := resp.Body.Read(buf[:])
	ms, err := parseServerTimeMS(string(buf[:n]))
	if err != nil {
		off, err2 := OffsetFromHTTPDate(resp.Header.Get("Date"), t0.Add(t1.Sub(t0)/2))
		return off, err2
	}
	mid := t0.Add(t1.Sub(t0) / 2)
	server := time.UnixMilli(ms)
	return server.Sub(mid), nil
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
