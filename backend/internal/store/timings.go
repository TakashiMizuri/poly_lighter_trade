package store

import "encoding/json"

// Timings is the per-window latency budget in milliseconds (spans + gaps from UTC open).
type Timings struct {
	OpenUnix float64 `json:"open_unix"`

	MsLateStart float64 `json:"ms_late_start,omitempty"`
	MsArm       float64 `json:"ms_arm,omitempty"`
	MsReady     float64 `json:"ms_ready,omitempty"`
	MsWarmBook  float64 `json:"ms_warm_book,omitempty"`
	MsNonce     float64 `json:"ms_nonce,omitempty"`
	MsHasWindow float64 `json:"ms_has_window,omitempty"`
	MsQuote     float64 `json:"ms_quote,omitempty"`
	MsEquity    float64 `json:"ms_equity,omitempty"`
	MsSend      float64 `json:"ms_send,omitempty"`
	MsFillWait  float64 `json:"ms_fill_wait,omitempty"`
	MsPosCheck  float64 `json:"ms_pos_check,omitempty"`
	MsOrderGet  float64 `json:"ms_order_get,omitempty"`
	MsStop      float64 `json:"ms_stop,omitempty"`
	MsSqlite    float64 `json:"ms_sqlite,omitempty"`

	MsOpenToQuote    float64 `json:"ms_open_to_quote,omitempty"`
	MsOpenToDecide   float64 `json:"ms_open_to_decide,omitempty"`
	MsOpenToSend     float64 `json:"ms_open_to_send,omitempty"`
	MsOpenToExchange float64 `json:"ms_open_to_exchange,omitempty"`
	MsSendToExchange float64 `json:"ms_send_to_exchange,omitempty"`

	QuoteSource string `json:"quote_source,omitempty"`
}

func timingsArg(t *Timings) any {
	if t == nil {
		return nil
	}
	b, err := json.Marshal(t)
	if err != nil {
		return nil
	}
	return string(b)
}

func parseTimings(raw string) *Timings {
	if raw == "" {
		return nil
	}
	var t Timings
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return nil
	}
	return &t
}
