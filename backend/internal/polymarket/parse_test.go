package polymarket

import "testing"

func TestParseEvent(t *testing.T) {
	raw := []byte(`{"markets":[{"outcomes":"[\"Up\",\"Down\"]","clobTokenIds":"[\"aaa\",\"bbb\"]"}]}`)
	m, err := parseEvent("btc-updown-5m-1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.TokenUp != "aaa" || m.TokenDown != "bbb" {
		t.Fatalf("%+v", m)
	}
}
