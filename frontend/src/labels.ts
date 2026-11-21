/** Maps engine skip_reason / joined reasons to Russian UI labels. */
export function skipLabel(reason: string, threshold = 0.65): string {
  if (!reason) return "—";
  if (reason.includes(",")) {
    return reason
      .split(",")
      .map((p) => skipLabel(p.trim(), threshold))
      .join(" · ");
  }
  const th = threshold.toFixed(2).replace(/0+$/, "").replace(/\.$/, "");
  const map: Record<string, string> = {
    below_th: `ниже порога ${th}`,
    both_ge_th: `обе стороны ≥ ${th}`,
    late: "позже T+2 с",
    clock: "часы хоста",
    no_market: "нет рынка PM",
    bad_quote: "нет живой котировки",
    stale_quote: "котировка устарела",
    quote_incoherent: "котировка Up/Down не сходится",
    halt: "HALT",
    ready: "не готов (equity/цена/flat)",
    venue: "Lighter недоступен (DNS/сеть)",
    equity_too_small: "мало equity / лот",
    order_failed: "ордер не ушёл",
    entry_unfilled_after_send: "ордер без fill",
    unexpected_entry_size: "unexpected size",
    stop_missing: "стоп не встал",
    exit_late: "выход после границы",
  };
  return map[reason] || reason;
}

export function exitLabel(reason: string): string {
  if (!reason) return "—";
  if (reason.includes(",")) {
    return reason
      .split(",")
      .map((p) => exitLabel(p.trim()))
      .join(" · ");
  }
  const map: Record<string, string> = {
    sl: "стоп",
    window_close: "close окна",
    closed_away: "закрыто не стопом",
    halt: "HALT",
    halt_age: "позиция > 6 мин",
    skip: "skip",
    stop_missing: "стоп не встал",
    exit_late: "выход после границы",
  };
  return map[reason] || reason;
}
