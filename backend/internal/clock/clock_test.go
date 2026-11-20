package clock

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWindowTS(t *testing.T) {
	if WindowTS(1795090500) != 1795090500 {
		t.Fatal("exact")
	}
	if WindowTS(1795090501) != 1795090500 {
		t.Fatal("floor")
	}
	if WindowTS(1795090799) != 1795090500 {
		t.Fatal("end")
	}
	if WindowTS(1795090800) != 1795090800 {
		t.Fatal("next")
	}
}

func TestSleepUntilAlreadyPast(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(100, 0)
	if err := SleepUntil(ctx, now.Add(-time.Second), func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
}

func TestOffsetFromHTTPDate(t *testing.T) {
	local := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	hdr := local.Add(2 * time.Second).UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	off, err := OffsetFromHTTPDate(hdr, local)
	if err != nil {
		t.Fatal(err)
	}
	if off < time.Second || off > 3*time.Second {
		t.Fatalf("offset %s", off)
	}
}

func TestMedianTrustedOffsetDropsSlowRTT(t *testing.T) {
	samples := []OffsetSample{
		{Offset: 10 * time.Millisecond, RTT: 240 * time.Millisecond},
		{Offset: 2500 * time.Millisecond, RTT: 5200 * time.Millisecond},
		{Offset: 8 * time.Millisecond, RTT: 230 * time.Millisecond},
	}
	med, ok := MedianTrustedOffset(samples)
	if !ok {
		t.Fatal("expected trusted")
	}
	if med.Offset < 5*time.Millisecond || med.Offset > 15*time.Millisecond {
		t.Fatalf("median %s", med.Offset)
	}
}

func TestMedianTrustedOffsetNone(t *testing.T) {
	if _, ok := MedianTrustedOffset([]OffsetSample{{Offset: time.Second, RTT: 5 * time.Second}}); ok {
		t.Fatal("slow RTT must not be trusted")
	}
}

func TestMeasureBinanceOffsetStable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int64{"serverTime": time.Now().UnixMilli()})
	}))
	t.Cleanup(srv.Close)
	s, err := MeasureBinanceOffsetStable(context.Background(), srv.Client(), srv.URL, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Trusted() {
		t.Fatalf("rtt %s", s.RTT)
	}
	if absDur(s.Offset) > 500*time.Millisecond {
		t.Fatalf("offset %s", s.Offset)
	}
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
