package clock

import (
	"context"
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
