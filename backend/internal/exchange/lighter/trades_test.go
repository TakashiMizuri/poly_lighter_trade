package lighter

import "testing"

func TestFillsMatchingClientAndTx(t *testing.T) {
	fills := []Fill{
		{Price: 59038, Size: 0.00042, Fee: 0.01, Timestamp: 1_795_199_700, TxHash: "38277f378e36eb24", BidClient: 199700006},
		{Price: 59072, Size: 0.00042, Timestamp: 1_795_199_711, TxHash: "90d77e53c97adf20", AskClient: 0},
	}
	got := FillsMatching(fills, 199700006, "")
	if px, qty, _, _ := VWAP(got); px != 59038 || qty != 0.00042 {
		t.Fatalf("client match: px=%v qty=%v n=%d", px, qty, len(got))
	}
	got = FillsMatching(fills, 0, "0x38277F378E36EB24")
	if px, qty, _, _ := VWAP(got); px != 59038 || qty != 0.00042 {
		t.Fatalf("tx match: px=%v qty=%v n=%d", px, qty, len(got))
	}
	if n := FillsMatching(fills, 1, "missing"); len(n) != 0 {
		t.Fatalf("expected no match, got %+v", n)
	}
}

func TestSameTxHash(t *testing.T) {
	if !sameTxHash("0xABC", "abc") {
		t.Fatal("prefix/case")
	}
	if sameTxHash("", "abc") {
		t.Fatal("empty")
	}
}

func TestCreatedUnixIgnoresWholeSeconds(t *testing.T) {
	window := int64(1_795_273_200)
	sec := ExchangeOrder{CreatedAt: window, Timestamp: window}
	if _, ok := sec.CreatedUnix(); ok {
		t.Fatal("whole-second stamp must not count as a lag")
	}
	ms := window*1000 + 495
	u, ok := ExchangeOrder{CreatedAt: ms}.CreatedUnix()
	if !ok {
		t.Fatal("millisecond stamp must be precise")
	}
	lag := (u - float64(window)) * 1000
	if lag < 494.5 || lag > 495.5 {
		t.Fatalf("lag=%v", lag)
	}
	us := window*1_000_000 + 730_000
	u, ok = ExchangeOrder{Timestamp: us}.CreatedUnix()
	if !ok {
		t.Fatal("microsecond stamp must be precise")
	}
	lag = (u - float64(window)) * 1000
	if lag < 729.5 || lag > 730.5 {
		t.Fatalf("us lag=%v", lag)
	}
}

func TestFillsClosingPositionSkipsEntry(t *testing.T) {
	fills := []Fill{
		{Price: 56125, Size: 0.00044, Timestamp: 1_000, TxHash: "entryhash", AskClient: 10},
		{Price: 56165.7, Size: 0.00044, Timestamp: 1_011, TxHash: "closehash", BidClient: 11},
	}
	got := FillsClosingPosition(fills, 10, "entryhash", 0.00044, 1_000)
	px, qty, _, _ := VWAP(got)
	if qty != 0.00044 || px != 56165.7 {
		t.Fatalf("close px=%v qty=%v n=%d", px, qty, len(got))
	}
}
