package strategy

import "testing"

func TestStopPxNotLevered(t *testing.T) {
	px := StopPx(100_000, SideLong, 0.00125)
	if px < 99874.9 || px > 99875.1 {
		t.Fatalf("long stop %v", px)
	}
	px = StopPx(100_000, SideShort, 0.00125)
	if px < 100124.9 || px > 100125.1 {
		t.Fatalf("short stop %v", px)
	}
}

func TestSLSlip(t *testing.T) {
	// exact -12.5 bps
	if s := SLSlipBps(100_000, 99_875, SideLong, 0.00125); s < -0.05 || s > 0.05 {
		t.Fatalf("exact %v", s)
	}
	// worse: exit 99_850
	if s := SLSlipBps(100_000, 99_850, SideLong, 0.00125); s < 2.4 || s > 2.6 {
		t.Fatalf("worse %v", s)
	}
}

func TestStopHit(t *testing.T) {
	if !StopHit(99_870, SideLong, 99_875) {
		t.Fatal("long")
	}
	if StopHit(99_880, SideLong, 99_875) {
		t.Fatal("long not yet")
	}
}
