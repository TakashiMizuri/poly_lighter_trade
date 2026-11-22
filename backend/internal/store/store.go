package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil && dir != "." && dir != "" {
		return nil, err
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

const schemaVersion = 2

func (s *Store) migrate() error {
	_, err := s.DB.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS windows (
  window_ts INTEGER PRIMARY KEY,
  decision_ts REAL,
  quote_ts REAL,
  p_up REAL,
  p_down REAL,
  spread REAL,
  quote_source TEXT,
  side TEXT NOT NULL,
  skip_reason TEXT,
  lighter_entry_ts REAL,
  lighter_entry_px REAL,
  lighter_exit_ts REAL,
  lighter_exit_px REAL,
  qty REAL,
  notional REAL,
  equity_before REAL,
  equity_after REAL,
  pnl_usd REAL,
  ret_bps REAL,
  fees_usd REAL,
  entry_lag_ms REAL,
  fill_lag_ms REAL,
  shadow_binance_px_t1 REAL,
  shadow_ret_bps REAL,
  pm_fav REAL,
  state TEXT,
  payload TEXT
);
CREATE TABLE IF NOT EXISTS orders (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  window_ts INTEGER NOT NULL,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  client_order_index INTEGER,
  tx_hash TEXT,
  qty REAL,
  price REAL,
  created_at INTEGER NOT NULL,
  payload TEXT
);
CREATE TABLE IF NOT EXISTS equity_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  source TEXT NOT NULL,
  equity REAL NOT NULL,
  UNIQUE(ts, source)
);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  level TEXT NOT NULL,
  kind TEXT NOT NULL,
  message TEXT NOT NULL,
  payload TEXT
);
CREATE TABLE IF NOT EXISTS bot_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
`)
	if err != nil {
		return err
	}
	var ver int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&ver)
	if ver < 1 {
		for _, q := range []string{
			`ALTER TABLE windows ADD COLUMN stop_px REAL`,
			`ALTER TABLE windows ADD COLUMN exit_reason TEXT`,
			`ALTER TABLE windows ADD COLUMN sl_slip_bps REAL`,
			`ALTER TABLE windows ADD COLUMN clock_offset_ms REAL`,
			`ALTER TABLE windows ADD COLUMN clock_rtt_ms REAL`,
			`ALTER TABLE windows ADD COLUMN timings TEXT`,
		} {
			if _, err := s.DB.Exec(q); err != nil && !isSQLiteDuplicateColumn(err) {
				return fmt.Errorf("migrate v1: %w", err)
			}
		}
		if _, err := s.DB.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, ?)`, time.Now().Unix()); err != nil {
			return err
		}
		ver = 1
	}
	if ver < 2 {
		if _, err := s.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS orders_client_order_index_uq ON orders(client_order_index) WHERE client_order_index IS NOT NULL AND client_order_index != 0`); err != nil {
			return fmt.Errorf("migrate v2 unique client_order_index: %w", err)
		}
		if _, err := s.DB.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(2, ?)`, time.Now().Unix()); err != nil {
			return err
		}
	}
	_ = schemaVersion
	return nil
}

func isSQLiteDuplicateColumn(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")
}

type Window struct {
	WindowTS          int64           `json:"window_ts"`
	DecisionTS        float64         `json:"decision_ts"`
	QuoteTS           float64         `json:"quote_ts"`
	PUp               float64         `json:"p_up"`
	PDown             float64         `json:"p_down"`
	Spread            float64         `json:"spread"`
	QuoteSource       string          `json:"quote_source"`
	Side              string          `json:"side"`
	SkipReason        string          `json:"skip_reason"`
	LighterEntryTS    sql.NullFloat64 `json:"lighter_entry_ts"`
	LighterEntryPx    sql.NullFloat64 `json:"lighter_entry_px"`
	LighterExitTS     sql.NullFloat64 `json:"lighter_exit_ts"`
	LighterExitPx     sql.NullFloat64 `json:"lighter_exit_px"`
	Qty               sql.NullFloat64 `json:"qty"`
	Notional          sql.NullFloat64 `json:"notional"`
	EquityBefore      sql.NullFloat64 `json:"equity_before"`
	EquityAfter       sql.NullFloat64 `json:"equity_after"`
	PnLUSD            sql.NullFloat64 `json:"pnl_usd"`
	RetBps            sql.NullFloat64 `json:"ret_bps"`
	FeesUSD           sql.NullFloat64 `json:"fees_usd"`
	EntryLagMS        sql.NullFloat64 `json:"entry_lag_ms"`
	FillLagMS         sql.NullFloat64 `json:"fill_lag_ms"`
	ShadowBinancePxT1 sql.NullFloat64 `json:"shadow_binance_px_t1"`
	ShadowRetBps      sql.NullFloat64 `json:"shadow_ret_bps"`
	PMFav             sql.NullFloat64 `json:"pm_fav"`
	State             string          `json:"state"`
	StopPx            sql.NullFloat64 `json:"stop_px"`
	ExitReason        string          `json:"exit_reason"`
	SLSlipBps         sql.NullFloat64 `json:"sl_slip_bps"`
	ClockOffsetMS     sql.NullFloat64 `json:"clock_offset_ms"`
	ClockRTTMS        sql.NullFloat64 `json:"clock_rtt_ms"`
	Timings           *Timings        `json:"timings,omitempty"`
}

func nullF(v float64, valid bool) any {
	if !valid {
		return nil
	}
	return v
}

func (s *Store) InsertWindow(ctx context.Context, w Window) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO windows(
  window_ts, decision_ts, quote_ts, p_up, p_down, spread, quote_source, side, skip_reason,
  lighter_entry_ts, lighter_entry_px, lighter_exit_ts, lighter_exit_px,
  qty, notional, equity_before, equity_after, pnl_usd, ret_bps, fees_usd,
  entry_lag_ms, fill_lag_ms, shadow_binance_px_t1, shadow_ret_bps, pm_fav, state,
  stop_px, exit_reason, sl_slip_bps, clock_offset_ms, clock_rtt_ms, timings
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		w.WindowTS, w.DecisionTS, w.QuoteTS, w.PUp, w.PDown, w.Spread, w.QuoteSource, w.Side, w.SkipReason,
		nullF(w.LighterEntryTS.Float64, w.LighterEntryTS.Valid),
		nullF(w.LighterEntryPx.Float64, w.LighterEntryPx.Valid),
		nullF(w.LighterExitTS.Float64, w.LighterExitTS.Valid),
		nullF(w.LighterExitPx.Float64, w.LighterExitPx.Valid),
		nullF(w.Qty.Float64, w.Qty.Valid),
		nullF(w.Notional.Float64, w.Notional.Valid),
		nullF(w.EquityBefore.Float64, w.EquityBefore.Valid),
		nullF(w.EquityAfter.Float64, w.EquityAfter.Valid),
		nullF(w.PnLUSD.Float64, w.PnLUSD.Valid),
		nullF(w.RetBps.Float64, w.RetBps.Valid),
		nullF(w.FeesUSD.Float64, w.FeesUSD.Valid),
		nullF(w.EntryLagMS.Float64, w.EntryLagMS.Valid),
		nullF(w.FillLagMS.Float64, w.FillLagMS.Valid),
		nullF(w.ShadowBinancePxT1.Float64, w.ShadowBinancePxT1.Valid),
		nullF(w.ShadowRetBps.Float64, w.ShadowRetBps.Valid),
		nullF(w.PMFav.Float64, w.PMFav.Valid),
		w.State,
		nullF(w.StopPx.Float64, w.StopPx.Valid),
		w.ExitReason,
		nullF(w.SLSlipBps.Float64, w.SLSlipBps.Valid),
		nullF(w.ClockOffsetMS.Float64, w.ClockOffsetMS.Valid),
		nullF(w.ClockRTTMS.Float64, w.ClockRTTMS.Valid),
		timingsArg(w.Timings),
	)
	if err != nil {
		return fmt.Errorf("insert window %d: %w", w.WindowTS, err)
	}
	return nil
}

func (s *Store) HasWindow(ctx context.Context, ts int64) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM windows WHERE window_ts = ?`, ts).Scan(&n)
	return n > 0, err
}

func (s *Store) UpdateWindow(ctx context.Context, w Window) error {
	_, err := s.DB.ExecContext(ctx, `
UPDATE windows SET
  decision_ts=?, quote_ts=?, p_up=?, p_down=?, spread=?, quote_source=?, side=?, skip_reason=?,
  lighter_entry_ts=?, lighter_entry_px=?, lighter_exit_ts=?, lighter_exit_px=?,
  qty=?, notional=?, equity_before=?, equity_after=?, pnl_usd=?, ret_bps=?, fees_usd=?,
  entry_lag_ms=?, fill_lag_ms=COALESCE(?, fill_lag_ms), shadow_binance_px_t1=?, shadow_ret_bps=?, pm_fav=?, state=?,
  stop_px=?, exit_reason=?, sl_slip_bps=?, clock_offset_ms=?, clock_rtt_ms=?, timings=?
WHERE window_ts=?`,
		w.DecisionTS, w.QuoteTS, w.PUp, w.PDown, w.Spread, w.QuoteSource, w.Side, w.SkipReason,
		nullF(w.LighterEntryTS.Float64, w.LighterEntryTS.Valid),
		nullF(w.LighterEntryPx.Float64, w.LighterEntryPx.Valid),
		nullF(w.LighterExitTS.Float64, w.LighterExitTS.Valid),
		nullF(w.LighterExitPx.Float64, w.LighterExitPx.Valid),
		nullF(w.Qty.Float64, w.Qty.Valid),
		nullF(w.Notional.Float64, w.Notional.Valid),
		nullF(w.EquityBefore.Float64, w.EquityBefore.Valid),
		nullF(w.EquityAfter.Float64, w.EquityAfter.Valid),
		nullF(w.PnLUSD.Float64, w.PnLUSD.Valid),
		nullF(w.RetBps.Float64, w.RetBps.Valid),
		nullF(w.FeesUSD.Float64, w.FeesUSD.Valid),
		nullF(w.EntryLagMS.Float64, w.EntryLagMS.Valid),
		nullF(w.FillLagMS.Float64, w.FillLagMS.Valid),
		nullF(w.ShadowBinancePxT1.Float64, w.ShadowBinancePxT1.Valid),
		nullF(w.ShadowRetBps.Float64, w.ShadowRetBps.Valid),
		nullF(w.PMFav.Float64, w.PMFav.Valid),
		w.State,
		nullF(w.StopPx.Float64, w.StopPx.Valid),
		w.ExitReason,
		nullF(w.SLSlipBps.Float64, w.SLSlipBps.Valid),
		nullF(w.ClockOffsetMS.Float64, w.ClockOffsetMS.Valid),
		nullF(w.ClockRTTMS.Float64, w.ClockRTTMS.Valid),
		timingsArg(w.Timings),
		w.WindowTS,
	)
	return err
}

func (s *Store) GetWindow(ctx context.Context, ts int64) (Window, error) {
	row := s.DB.QueryRowContext(ctx, `
SELECT window_ts, IFNULL(decision_ts,0), IFNULL(quote_ts,0), IFNULL(p_up,0), IFNULL(p_down,0), IFNULL(spread,0),
  IFNULL(quote_source,''), side, IFNULL(skip_reason,''),
  lighter_entry_ts, lighter_entry_px, lighter_exit_ts, lighter_exit_px,
  qty, notional, equity_before, equity_after, pnl_usd, ret_bps, fees_usd,
  entry_lag_ms, fill_lag_ms, shadow_binance_px_t1, shadow_ret_bps, pm_fav, IFNULL(state,''),
  stop_px, IFNULL(exit_reason,''), sl_slip_bps, clock_offset_ms, clock_rtt_ms, IFNULL(timings,'')
FROM windows WHERE window_ts=?`, ts)
	var w Window
	var timingsRaw string
	err := row.Scan(&w.WindowTS, &w.DecisionTS, &w.QuoteTS, &w.PUp, &w.PDown, &w.Spread, &w.QuoteSource, &w.Side, &w.SkipReason,
		&w.LighterEntryTS, &w.LighterEntryPx, &w.LighterExitTS, &w.LighterExitPx,
		&w.Qty, &w.Notional, &w.EquityBefore, &w.EquityAfter, &w.PnLUSD, &w.RetBps, &w.FeesUSD,
		&w.EntryLagMS, &w.FillLagMS, &w.ShadowBinancePxT1, &w.ShadowRetBps, &w.PMFav, &w.State,
		&w.StopPx, &w.ExitReason, &w.SLSlipBps, &w.ClockOffsetMS, &w.ClockRTTMS, &timingsRaw)
	w.Timings = parseTimings(timingsRaw)
	return w, err
}

func (s *Store) ListWindows(ctx context.Context, limit int) ([]Window, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT window_ts, IFNULL(decision_ts,0), IFNULL(quote_ts,0), IFNULL(p_up,0), IFNULL(p_down,0), IFNULL(spread,0),
  IFNULL(quote_source,''), side, IFNULL(skip_reason,''),
  lighter_entry_ts, lighter_entry_px, lighter_exit_ts, lighter_exit_px,
  qty, notional, equity_before, equity_after, pnl_usd, ret_bps, fees_usd,
  entry_lag_ms, fill_lag_ms, shadow_binance_px_t1, shadow_ret_bps, pm_fav, IFNULL(state,''),
  stop_px, IFNULL(exit_reason,''), sl_slip_bps, clock_offset_ms, clock_rtt_ms, IFNULL(timings,'')
FROM windows ORDER BY window_ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Window
	for rows.Next() {
		var w Window
		var timingsRaw string
		if err := rows.Scan(&w.WindowTS, &w.DecisionTS, &w.QuoteTS, &w.PUp, &w.PDown, &w.Spread, &w.QuoteSource, &w.Side, &w.SkipReason,
			&w.LighterEntryTS, &w.LighterEntryPx, &w.LighterExitTS, &w.LighterExitPx,
			&w.Qty, &w.Notional, &w.EquityBefore, &w.EquityAfter, &w.PnLUSD, &w.RetBps, &w.FeesUSD,
			&w.EntryLagMS, &w.FillLagMS, &w.ShadowBinancePxT1, &w.ShadowRetBps, &w.PMFav, &w.State,
			&w.StopPx, &w.ExitReason, &w.SLSlipBps, &w.ClockOffsetMS, &w.ClockRTTMS, &timingsRaw); err != nil {
			return nil, err
		}
		w.Timings = parseTimings(timingsRaw)
		if w.Side == "skip" {
			w.EntryLagMS = sql.NullFloat64{}
			w.FillLagMS = sql.NullFloat64{}
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) InsertOrder(ctx context.Context, windowTS int64, kind, status string, clientIdx int64, txHash string, qty, px float64, payload any) error {
	var raw string
	if payload != nil {
		b, _ := json.Marshal(payload)
		raw = string(b)
	}
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO orders(window_ts, kind, status, client_order_index, tx_hash, qty, price, created_at, payload)
VALUES(?,?,?,?,?,?,?,?,?)`, windowTS, kind, status, clientIdx, txHash, qty, px, time.Now().Unix(), raw)
	return err
}

func (s *Store) ListOrders(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT id, window_ts, kind, status, IFNULL(client_order_index,0), IFNULL(tx_hash,''), IFNULL(qty,0), IFNULL(price,0), created_at
FROM orders ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, wts, created, client int64
		var kind, status, hash string
		var qty, px float64
		if err := rows.Scan(&id, &wts, &kind, &status, &client, &hash, &qty, &px, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id": id, "window_ts": wts, "kind": kind, "status": status,
			"client_order_index": client, "tx_hash": hash, "qty": qty, "price": px, "created_at": created,
		})
	}
	return out, rows.Err()
}

// UpdateOrderFill records a confirmed venue fill against the client order index.
func (s *Store) UpdateOrderFill(ctx context.Context, clientIdx int64, status string, px float64) error {
	if s == nil || clientIdx == 0 {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `
UPDATE orders SET status=?, price=? WHERE client_order_index=?`, status, px, clientIdx)
	return err
}

// CancelOpenStops marks resting stop orders canceled after a successful CancelAll.
func (s *Store) CancelOpenStops(ctx context.Context, windowTS int64) error {
	if s == nil {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `
UPDATE orders SET status='canceled'
WHERE window_ts=? AND kind='stop_loss' AND status='sent'`, windowTS)
	return err
}

// UpdateWindowFillLag records a precise exchange created_at as the entry lag.
// A whole-second or non-positive lag is ignored. A lag already stored is kept.
// Timings gain the exchange mark so the dashboard scale matches the column.
func (s *Store) UpdateWindowFillLag(ctx context.Context, windowTS int64, lagMS, entryUnix float64) error {
	if s == nil || windowTS == 0 || lagMS <= 0 {
		return nil
	}
	w, err := s.GetWindow(ctx, windowTS)
	if err != nil {
		return err
	}
	if w.FillLagMS.Valid && w.FillLagMS.Float64 > 0 {
		return nil
	}
	w.FillLagMS = sql.NullFloat64{Float64: lagMS, Valid: true}
	if entryUnix > 0 {
		w.LighterEntryTS = sql.NullFloat64{Float64: entryUnix, Valid: true}
	}
	if w.Timings == nil {
		w.Timings = &Timings{OpenUnix: float64(windowTS)}
	}
	w.Timings.MsOpenToExchange = lagMS
	if w.Timings.MsOpenToSend > 0 && lagMS >= w.Timings.MsOpenToSend {
		w.Timings.MsSendToExchange = lagMS - w.Timings.MsOpenToSend
	}
	return s.UpdateWindow(ctx, w)
}

// UpdateWindowExit writes only the confirmed close. Shadow columns stay untouched.
func (s *Store) UpdateWindowExit(ctx context.Context, windowTS int64, exitTS, exitPx, pnl, retBps, fees float64, state, exitReason string) error {
	if s == nil {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `
UPDATE windows SET
  lighter_exit_ts=?, lighter_exit_px=?, pnl_usd=?, ret_bps=?, fees_usd=?,
  state=?, exit_reason=?
WHERE window_ts=?`,
		exitTS, exitPx, pnl, retBps, fees, state, exitReason, windowTS)
	return err
}

// UpdateWindowShadow writes only the Binance shadow columns.
func (s *Store) UpdateWindowShadow(ctx context.Context, windowTS int64, px, ret sql.NullFloat64) error {
	if s == nil {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `
UPDATE windows SET
  shadow_binance_px_t1=COALESCE(?, shadow_binance_px_t1),
  shadow_ret_bps=COALESCE(?, shadow_ret_bps)
WHERE window_ts=?`,
		nullF(px.Float64, px.Valid), nullF(ret.Float64, ret.Valid), windowTS)
	return err
}

// UnconfirmedExit is a window_close whose booked exit is missing or copied from the entry.
type UnconfirmedExit struct {
	WindowTS  int64
	Side      string
	EntryPx   float64
	Notional  float64
	Fees      float64
	ClientIdx int64
	TxHash    string
}

// ListUnconfirmedExits returns recent window closes that still need a tape price.
// One row per window: the earliest exit order.
func (s *Store) ListUnconfirmedExits(ctx context.Context, since int64) ([]UnconfirmedExit, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT w.window_ts, w.side, IFNULL(w.lighter_entry_px,0), IFNULL(w.notional,0), IFNULL(w.fees_usd,0),
       o.client_order_index, IFNULL(o.tx_hash,'')
FROM windows w
JOIN orders o ON o.id = (
  SELECT MIN(id) FROM orders
  WHERE window_ts = w.window_ts AND kind = 'exit' AND IFNULL(client_order_index,0) != 0
)
WHERE w.window_ts >= ?
  AND w.exit_reason = 'window_close'
  AND w.side IN ('long','short')
  AND w.lighter_entry_px IS NOT NULL
  AND (
    w.lighter_exit_px IS NULL
    OR w.pnl_usd IS NULL
    OR ABS(w.lighter_exit_px - w.lighter_entry_px) < 1e-6
  )`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnconfirmedExit
	for rows.Next() {
		var row UnconfirmedExit
		if err := rows.Scan(&row.WindowTS, &row.Side, &row.EntryPx, &row.Notional, &row.Fees, &row.ClientIdx, &row.TxHash); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) InsertEvent(ctx context.Context, level, kind, message string, payload any) error {
	var raw string
	if payload != nil {
		b, _ := json.Marshal(payload)
		raw = string(b)
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO events(ts, level, kind, message, payload) VALUES(?,?,?,?,?)`,
		time.Now().Unix(), level, kind, message, raw)
	return err
}

func (s *Store) ListEvents(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 80
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT ts, level, kind, message FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var ts int64
		var level, kind, msg string
		if err := rows.Scan(&ts, &level, &kind, &msg); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"ts": ts, "level": level, "kind": kind, "message": msg})
	}
	return out, rows.Err()
}

func (s *Store) InsertEquity(ctx context.Context, ts int64, source string, eq float64) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT INTO equity_snapshots(ts, source, equity) VALUES(?,?,?)
ON CONFLICT(ts, source) DO UPDATE SET equity=excluded.equity`, ts, source, eq)
	return err
}

type EquityPoint struct {
	TS     int64   `json:"ts"`
	Live   float64 `json:"live"`
	Shadow float64 `json:"shadow"`
}

func (s *Store) ListCurve(ctx context.Context, limit int) ([]EquityPoint, error) {
	if limit <= 0 {
		limit = 4000
	}
	rows, err := s.DB.QueryContext(ctx, `
SELECT ts, source, equity FROM equity_snapshots ORDER BY ts DESC LIMIT ?`, limit*2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byTS := map[int64]*EquityPoint{}
	var order []int64
	for rows.Next() {
		var ts int64
		var source string
		var eq float64
		if err := rows.Scan(&ts, &source, &eq); err != nil {
			return nil, err
		}
		p, ok := byTS[ts]
		if !ok {
			p = &EquityPoint{TS: ts}
			byTS[ts] = p
			order = append(order, ts)
		}
		switch source {
		case "live":
			p.Live = eq
		case "shadow":
			p.Shadow = eq
		}
	}
	// reverse to chronological
	out := make([]EquityPoint, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- {
		out = append(out, *byTS[order[i]])
	}
	return out, rows.Err()
}

type GlobalState struct {
	KillSwitch     bool    `json:"kill_switch"`
	Halted         bool    `json:"halted"`
	HaltReason     string  `json:"halt_reason"`
	PeakEquity     float64 `json:"peak_equity"`
	StartEquity    float64 `json:"start_equity"`
	ShadowEquity   float64 `json:"shadow_equity"`
	LateStreak     int     `json:"late_streak"`
	ClientOrderSeq int64   `json:"client_order_seq"`
	LastError      string  `json:"last_error"`
	GoLiveTS       int64   `json:"go_live_ts"`
	// Version is a monotonic CAS token for bot_state last-write-wins protection.
	Version int64 `json:"version"`
}

func (s *Store) LoadGlobal(ctx context.Context) (GlobalState, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM bot_state WHERE key = ?`, "global").Scan(&raw)
	if err == sql.ErrNoRows {
		return GlobalState{}, nil
	}
	if err != nil {
		return GlobalState{}, err
	}
	var st GlobalState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return GlobalState{}, err
	}
	return st, nil
}

func (s *Store) SaveGlobal(ctx context.Context, st GlobalState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
INSERT INTO bot_state(key, value, updated_at) VALUES(?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		"global", string(b), time.Now().Unix())
	return err
}

// SaveGlobalCAS writes st only when the persisted Version equals expectedVersion.
// On success st.Version must already be expectedVersion+1 (caller increments).
func (s *Store) SaveGlobalCAS(ctx context.Context, st GlobalState, expectedVersion int64) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM bot_state WHERE key = ?`, "global").Scan(&raw)
	cur := GlobalState{}
	switch {
	case err == sql.ErrNoRows:
		if expectedVersion != 0 {
			return false, nil
		}
	case err != nil:
		return false, err
	default:
		if err := json.Unmarshal([]byte(raw), &cur); err != nil {
			return false, err
		}
		if cur.Version != expectedVersion {
			return false, nil
		}
	}
	if st.Version != expectedVersion+1 {
		st.Version = expectedVersion + 1
	}
	b, err := json.Marshal(st)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO bot_state(key, value, updated_at) VALUES(?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		"global", string(b), time.Now().Unix())
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

type Stats struct {
	NWindows      int      `json:"n_windows"`
	NQuoted       int      `json:"n_quoted"`
	NTrades       int      `json:"n_trades"`
	NSkip         int      `json:"n_skip"`
	NPaired       int      `json:"n_paired"`
	Coverage      float64  `json:"coverage"`
	WR            float64  `json:"wr"`
	MeanBps       float64  `json:"mean_bps"`
	MeanPnLUSD    float64  `json:"mean_pnl_usd"`
	ShadowMeanBps float64  `json:"shadow_mean_bps"`
	GapBps        float64  `json:"gap_bps"`
	MatchCoef     *float64 `json:"match_coef"`
	P50LagMS      float64  `json:"p50_lag_ms"`
	P90LagMS      float64  `json:"p90_lag_ms"`
	P50FillLagMS  float64  `json:"p50_fill_lag_ms"`
	P90FillLagMS  float64  `json:"p90_fill_lag_ms"`
	MeanFillLagMS float64  `json:"mean_fill_lag_ms"`
	NFillLag      int      `json:"n_fill_lag"`
	NClockSkip    int      `json:"n_clock_skip"`
	NSL           int      `json:"n_sl"`
	Forecast      Forecast `json:"forecast"`
}

func (s *Store) ComputeStats(ctx context.Context) (Stats, error) {
	var st Stats
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM windows`).Scan(&st.NWindows)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM windows
WHERE quote_source != '' AND quote_ts IS NOT NULL
  AND quote_ts >= window_ts AND quote_ts <= window_ts + 1.0`).Scan(&st.NQuoted)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM windows WHERE side IN ('long','short') AND lighter_entry_px IS NOT NULL`).Scan(&st.NTrades)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM windows WHERE side='skip'`).Scan(&st.NSkip)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM windows WHERE skip_reason = 'clock' OR skip_reason LIKE 'clock,%' OR skip_reason LIKE '%,clock'`).Scan(&st.NClockSkip)
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM windows WHERE exit_reason = 'sl'`).Scan(&st.NSL)
	if st.NWindows > 0 {
		st.Coverage = float64(st.NQuoted) / float64(st.NWindows)
	}
	var wins, settled int
	_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(1) FROM windows
WHERE side IN ('long','short') AND lighter_entry_px IS NOT NULL AND pnl_usd IS NOT NULL AND pnl_usd > 0`).Scan(&wins)
	_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(1) FROM windows
WHERE side IN ('long','short') AND lighter_entry_px IS NOT NULL AND pnl_usd IS NOT NULL`).Scan(&settled)
	var meanBps, meanPnL sql.NullFloat64
	_ = s.DB.QueryRowContext(ctx, `
SELECT AVG(ret_bps), AVG(pnl_usd) FROM windows
WHERE side IN ('long','short') AND lighter_entry_px IS NOT NULL
  AND ret_bps IS NOT NULL AND pnl_usd IS NOT NULL`).Scan(&meanBps, &meanPnL)
	if settled > 0 {
		st.WR = float64(wins) / float64(settled)
	}
	if meanBps.Valid {
		st.MeanBps = meanBps.Float64
	}
	if meanPnL.Valid {
		st.MeanPnLUSD = meanPnL.Float64
	}
	var livePaired, shadowPaired sql.NullFloat64
	_ = s.DB.QueryRowContext(ctx, `
SELECT COUNT(1), AVG(ret_bps), AVG(shadow_ret_bps)
FROM windows
WHERE lighter_entry_px IS NOT NULL AND ret_bps IS NOT NULL AND shadow_ret_bps IS NOT NULL`).Scan(&st.NPaired, &livePaired, &shadowPaired)
	// Gap/match use paired means; headline mean_bps stays all closed trades.
	var pairedLiveMean float64
	if livePaired.Valid {
		pairedLiveMean = livePaired.Float64
	}
	if shadowPaired.Valid {
		st.ShadowMeanBps = shadowPaired.Float64
	}
	if livePaired.Valid && shadowPaired.Valid {
		st.GapBps, st.MatchCoef = LiveShadowGap(pairedLiveMean, shadowPaired.Float64)
	}
	// fill_lag_ms <= 0 is a whole-second created_at collapsed onto :00, not a measured lag.
	const tradeLag = `side IN ('long','short') AND lighter_entry_px IS NOT NULL AND fill_lag_ms IS NOT NULL AND fill_lag_ms > 0`
	st.P50LagMS = percentileWhere(s, ctx, "fill_lag_ms", tradeLag, 0.50)
	st.P90LagMS = percentileWhere(s, ctx, "fill_lag_ms", tradeLag, 0.90)
	var meanLag sql.NullFloat64
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1), AVG(fill_lag_ms) FROM windows WHERE `+tradeLag).Scan(&st.NFillLag, &meanLag)
	if meanLag.Valid {
		st.MeanFillLagMS = meanLag.Float64
	}
	st.P50FillLagMS = percentileWhere(s, ctx, "fill_lag_ms", tradeLag, 0.50)
	st.P90FillLagMS = percentileWhere(s, ctx, "fill_lag_ms", tradeLag, 0.90)

	var sampleFrom, sampleTo sql.NullInt64
	_ = s.DB.QueryRowContext(ctx, `SELECT MIN(window_ts), MAX(window_ts) FROM windows`).Scan(&sampleFrom, &sampleTo)
	var equity float64
	_ = s.DB.QueryRowContext(ctx, `
SELECT equity FROM equity_snapshots WHERE source='live' ORDER BY ts DESC LIMIT 1`).Scan(&equity)
	if equity <= 0 {
		_ = s.DB.QueryRowContext(ctx, `
SELECT equity_after FROM windows WHERE equity_after IS NOT NULL ORDER BY window_ts DESC LIMIT 1`).Scan(&equity)
	}
	var fromTS, toTS int64
	if sampleFrom.Valid {
		fromTS = sampleFrom.Int64
	}
	if sampleTo.Valid {
		toTS = sampleTo.Int64
	}
	st.Forecast = BuildForecast(equity, st.NTrades, st.NWindows, wins, st.MeanBps, st.MeanPnLUSD, fromTS, toTS)
	st.Forecast.WR = st.WR
	return st, nil
}

func percentile(s *Store, ctx context.Context, col string, p float64) float64 {
	return percentileWhere(s, ctx, col, col+` IS NOT NULL`, p)
}

func percentileWhere(s *Store, ctx context.Context, col, where string, p float64) float64 {
	var n int
	_ = s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM windows WHERE `+where).Scan(&n)
	if n == 0 {
		return 0
	}
	off := int(float64(n-1) * p)
	var v float64
	q := fmt.Sprintf(`SELECT %s FROM windows WHERE %s ORDER BY %s LIMIT 1 OFFSET %d`, col, where, col, off)
	_ = s.DB.QueryRowContext(ctx, q).Scan(&v)
	return v
}

// LiveShadowGap is live_mean − shadow_mean in bps.
// MatchCoef is live/shadow when |shadow| is not ~0 (otherwise nil).
func LiveShadowGap(liveMean, shadowMean float64) (gap float64, coef *float64) {
	gap = liveMean - shadowMean
	if math.Abs(shadowMean) < 0.05 {
		return gap, nil
	}
	v := liveMean / shadowMean
	return gap, &v
}
