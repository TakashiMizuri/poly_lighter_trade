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
