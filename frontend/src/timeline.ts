// Turns a window's duration spans and ":00" offsets into an ordered timeline.
// Offsets (ms_open_to_*) are real moments. Spans that finish before the bell
// stay on the left as step durations: their distance to :00 is not measured.

export type TimelineInput = {
  ms_late_start?: number;
  ms_arm?: number;
  ms_ready?: number;
  ms_warm_book?: number;
  ms_nonce?: number;
  ms_has_window?: number;
  ms_quote?: number;
  ms_equity?: number;
  ms_send?: number;
  ms_fill_wait?: number;
  ms_pos_check?: number;
  ms_order_get?: number;
  ms_stop?: number;
  ms_sqlite?: number;
  ms_open_to_quote?: number;
  ms_open_to_decide?: number;
  ms_open_to_send?: number;
  ms_open_to_send_ack?: number;
  ms_open_to_exchange?: number;
};

export type TimelineGap = {
  label: string;
  title: string;
  dashed?: boolean;
  approx?: boolean;
};

export type TimelineNode = {
  id: string;
  label: string;
  title: string;
  detail?: string;
  kind: "pre" | "post";
};

export type TimelineModel = {
  before: TimelineNode[];
  beforeGaps: TimelineGap[];
  toOpen: TimelineGap | null;
  after: TimelineNode[];
  afterGaps: TimelineGap[];
  summary: string;
};

type Raw = {
  id: string;
  label: string;
  title: string;
  detail?: string;
  at: number;
  absolute: boolean;
  span?: number;
  rank: number;
};

const MERGE_MS = 3;

function pos(n: number | undefined): number | null {
  return typeof n === "number" && Number.isFinite(n) && n > 0 ? n : null;
}

function roundMs(n: number): number {
  return Math.round(n);
}

function joinDetail(a?: string, b?: string): string | undefined {
  const parts = [a, b].filter((p): p is string => !!p);
  return parts.length ? parts.join(" · ") : undefined;
}

function armRunsAfterOpen(
  arm: number | null,
  ready: number | null,
  late: number | null,
  firstPost: number | null,
  quoteDur: number | null,
): boolean {
  // refreshReady is recorded only while the clock is still before :00, and it
  // runs after a successful prewarm arm. A ready span means the arm we stored
  // finished before the bell.
  if (arm == null || ready != null) return false;
  if (firstPost == null) return late != null && late >= 15;
  const room = firstPost - (late ?? 0);
  if (arm > room + 40) return false;
  const accounted = (late ?? 0) + arm + (quoteDur ?? 0);
  return accounted + 100 >= firstPost;
}

function mergeClose(points: Raw[]): Raw[] {
  const sorted = [...points].sort((a, b) => a.at - b.at || a.rank - b.rank);
  const out: Raw[] = [];
  for (const p of sorted) {
    const prev = out[out.length - 1];
    if (prev && p.at - prev.at <= MERGE_MS) {
      prev.label = `${prev.label} · ${p.label}`;
      prev.id = `${prev.id}+${p.id}`;
      prev.absolute = prev.absolute || p.absolute;
      prev.title = `${prev.title} ${p.title}`;
      prev.detail = joinDetail(prev.detail, p.detail);
      prev.span = undefined;
      continue;
    }
    out.push({ ...p });
  }
  return out;
}

function gapBetween(prevLabel: string, prevAt: number, node: Raw): TimelineGap {
  const delta = node.at - prevAt;
  const n = roundMs(delta);
  const span = node.span;
  const spanIsGap = span != null && Math.abs(delta - span) <= 5;
  if (node.absolute || spanIsGap) {
    return {
      label: String(n),
      title: spanIsGap && !node.absolute
        ? `${roundMs(span)} мс — длительность «${node.label}»`
        : `${n} мс: ${prevLabel} → ${node.label}`,
    };
  }
  return {
    label: `~${n}`,
    approx: true,
    title: `около ${n} мс: ${prevLabel} → ${node.label}. Конец шага сложен из длительностей, отдельной метки нет.`,
  };
}

function summarize(model: Omit<TimelineModel, "summary">): string {
  const left = model.before
    .map((node, i) => `${node.label} ${model.beforeGaps[i]?.label ?? "?"} мс`)
    .join(", ");
  const right = model.after
    .map((node, i) => `${node.label} (${model.afterGaps[i]?.label ?? "?"} мс)`)
    .join(", ");
  const parts = ["Колокол :00."];
  if (left) parts.unshift(`До :00: ${left}.`);
  if (right) parts.push(`После: ${right}.`);
  else parts.push("После :00 замеров нет.");
  return parts.join(" ");
}

export function buildTimeline(t: TimelineInput | null | undefined): TimelineModel | null {
  if (!t) return null;

  const late = pos(t.ms_late_start);
  const arm = pos(t.ms_arm);
  const ready = pos(t.ms_ready);
  const warm = pos(t.ms_warm_book);
  const nonce = pos(t.ms_nonce);
  const hasWindow = pos(t.ms_has_window);
  const quoteDur = pos(t.ms_quote);
  const equity = pos(t.ms_equity);
  const sendDur = pos(t.ms_send);
  const fillDur = pos(t.ms_fill_wait);
  const posCheck = pos(t.ms_pos_check);
  const orderGet = pos(t.ms_order_get);
  const stopDur = pos(t.ms_stop);
  const sqlite = pos(t.ms_sqlite);

  const quoteAt = pos(t.ms_open_to_quote);
  const decideAt = pos(t.ms_open_to_decide);
  const sendAt = pos(t.ms_open_to_send);
  const ackAt = pos(t.ms_open_to_send_ack);
  const exchAt = pos(t.ms_open_to_exchange);

  const anchors = [quoteAt, decideAt, sendAt, ackAt, exchAt].filter((n): n is number => n != null);
  const firstPost = anchors.length ? Math.min(...anchors) : null;
  const armAfter = armRunsAfterOpen(arm, ready, late, firstPost, quoteDur);

  const before: TimelineNode[] = [];
  const beforeGaps: TimelineGap[] = [];

  const pushBefore = (node: TimelineNode, dur: number, gapTitle: string) => {
    before.push(node);
    beforeGaps.push({ label: String(roundMs(dur)), title: gapTitle });
  };

  if (arm != null && !armAfter) {
    const nested = [
      warm != null ? `прогрев ${roundMs(warm)}` : "",
      nonce != null ? `nonce ${roundMs(nonce)}` : "",
    ].filter(Boolean);
    pushBefore(
      {
        id: "arm",
        kind: "pre",
        label: "рынок PM",
        title: `Resolve рынка Polymarket занял ${roundMs(arm)} мс до колокола. Прогрев стакана и nonce идут внутри этого шага и частично параллельно.`,
        detail: nested.length ? nested.join(" · ") : undefined,
      },
      arm,
      `${roundMs(arm)} мс — resolve рынка Polymarket до :00`,
    );
  }
  if (ready != null) {
    pushBefore(
      {
        id: "ready",
        kind: "pre",
        label: "equity",
        title: `Снимок equity и цены Lighter занял ${roundMs(ready)} мс до колокола.`,
      },
      ready,
      `${roundMs(ready)} мс — equity и цена Lighter до :00`,
    );
  }

  const afterRaw: Raw[] = [];

  if (late != null && late >= 5) {
    afterRaw.push({
      id: "wake",
      label: "старт",
      title: `Процесс дошёл до решения через ${roundMs(late)} мс после :00.`,
      at: late,
      absolute: true,
      rank: 0,
    });
  }
  if (hasWindow != null && hasWindow >= 25) {
    const at = (late ?? 0) + hasWindow;
    afterRaw.push({
      id: "check",
      label: "проверка",
      title: `Проверка, что окно ещё не записано, заняла ${roundMs(hasWindow)} мс.`,
      at,
      absolute: false,
      span: hasWindow,
      rank: 1,
    });
  }
  if (arm != null && armAfter) {
    const nested = [
      warm != null ? `прогрев ${roundMs(warm)}` : "",
      nonce != null ? `nonce ${roundMs(nonce)}` : "",
    ].filter(Boolean);
    const prelude = (late != null && late >= 5 ? late : 0) + (hasWindow != null && hasWindow >= 25 ? hasWindow : 0);
    afterRaw.push({
      id: "arm",
      label: "рынок PM",
      title: `Resolve рынка Polymarket занял ${roundMs(arm)} мс уже после :00.`,
      detail: nested.length ? nested.join(" · ") : undefined,
      at: prelude + arm,
      absolute: false,
      span: arm,
      rank: 2,
    });
  }
  if (quoteAt != null) {
    afterRaw.push({
      id: "quote",
      label: "котировка",
      title: `Котировка Polymarket, ${roundMs(quoteAt)} мс после :00.`,
      at: quoteAt,
      absolute: true,
      rank: 3,
    });
  }
  if (decideAt != null) {
    afterRaw.push({
      id: "decide",
      label: "решение",
      title: `Решение, ${roundMs(decideAt)} мс после :00.`,
      at: decideAt,
      absolute: true,
      rank: 4,
    });
  }
  if (sendAt != null) {
    afterRaw.push({
      id: "send",
      label: "IOC",
      title: `Отправка IOC, ${roundMs(sendAt)} мс после :00.`,
      detail: equity != null ? `equity ${roundMs(equity)}` : undefined,
      at: sendAt,
      absolute: true,
      rank: 5,
    });
  } else if (equity != null && decideAt != null) {
    afterRaw.push({
      id: "equity",
      label: "equity",
      title: `Повторный снимок equity занял ${roundMs(equity)} мс.`,
      at: decideAt + equity,
      absolute: false,
      span: equity,
      rank: 5,
    });
  }
  if (ackAt != null) {
    afterRaw.push({
      id: "ack",
      label: "ответ",
      title: `Ответ sendTx, ${roundMs(ackAt)} мс после :00.`,
      at: ackAt,
      absolute: true,
      rank: 6,
    });
  }
  if (exchAt != null) {
    afterRaw.push({
      id: "exchange",
      label: "биржа",
      title: `Lighter created_at ордера, ${roundMs(exchAt)} мс после :00. Это лаг входа.`,
      at: exchAt,
      absolute: true,
      rank: 7,
    });
  }

  let cursor = ackAt ?? (sendAt != null && sendDur != null ? sendAt + sendDur : null);
  const chain: { id: string; label: string; dur: number; title: string; rank: number }[] = [];
  if (cursor != null) {
    if (fillDur != null) {
      chain.push({
        id: "fill",
        label: "филл",
        dur: fillDur,
        title: `Подтверждение филла заняло ${roundMs(fillDur)} мс.`,
        rank: 8,
      });
    }
    if (posCheck != null) {
      chain.push({
        id: "pos",
        label: "позиция",
        dur: posCheck,
        title: `Проверка позиции заняла ${roundMs(posCheck)} мс.`,
        rank: 9,
      });
    }
    if (stopDur != null) {
      chain.push({
        id: "stop",
        label: "стоп",
        dur: stopDur,
        title: `Постановка стопа заняла ${roundMs(stopDur)} мс.`,
        rank: 10,
      });
    }
    if (orderGet != null) {
      chain.push({
        id: "stamp",
        label: "метка",
        dur: orderGet,
        title: `Запрос времени ордера занял ${roundMs(orderGet)} мс.`,
        rank: 11,
      });
    }
  }
  if (sqlite != null) {
    chain.push({
      id: "sql",
      label: "запись",
      dur: sqlite,
      title: `Запись окна в sqlite заняла ${roundMs(sqlite)} мс.`,
      rank: 12,
    });
  }
  if (chain.length) {
    if (cursor == null) {
      const placed = afterRaw.map((p) => p.at);
      cursor = placed.length ? Math.max(...placed) : 0;
    }
    for (const step of chain) {
      cursor += step.dur;
      afterRaw.push({
        id: step.id,
        label: step.label,
        title: step.title,
        at: cursor,
        absolute: false,
        span: step.dur,
        rank: step.rank,
      });
    }
  }

  const afterPoints = mergeClose(afterRaw);
  if (quoteDur != null) {
    let prevAt = 0;
    for (const p of afterPoints) {
      if (p.id.split("+").includes("quote") && p.at - prevAt > quoteDur + 15) {
        p.detail = joinDetail(p.detail, `запрос ${roundMs(quoteDur)}`);
        if (!p.title.includes("Сам запрос")) {
          p.title = `${p.title} Сам запрос котировки ${roundMs(quoteDur)} мс.`;
        }
      }
      prevAt = p.at;
    }
  }

  const after: TimelineNode[] = [];
  const afterGaps: TimelineGap[] = [];
  let prevAt = 0;
  let prevLabel = ":00";
  for (const p of afterPoints) {
    afterGaps.push(gapBetween(prevLabel, prevAt, p));
    after.push({
      id: p.id,
      label: p.label,
      title: p.title,
      detail: p.detail,
      kind: "post",
    });
    prevAt = p.at;
    prevLabel = p.label;
  }

  if (before.length === 0 && after.length === 0) return null;

  const toOpen: TimelineGap | null = before.length
    ? {
        label: "до :00",
        dashed: true,
        title: "Сколько процесс ждал колокол после подготовки — не измеряется.",
      }
    : null;

  const model = { before, beforeGaps, toOpen, after, afterGaps };
  return { ...model, summary: summarize(model) };
}

export type GanttBar = {
  id: string;
  label: string;
  title: string;
  start: number;
  end: number;
  kind: "pre" | "post" | "wait";
  rank: number;
};

export type GanttModel = {
  bars: GanttBar[];
  from: number;
  to: number;
};

function pushBar(bars: GanttBar[], bar: GanttBar) {
  if (bar.end < bar.start) return;
  bars.push(bar);
}

// Proportional bars around :00. Preparation durations are packed against the
// line because the idle gap before the bell is not measured. After :00 the
// positions are real offsets.
export function buildGantt(t: TimelineInput | null | undefined): GanttModel | null {
  if (!t) return null;
  const late = pos(t.ms_late_start);
  const arm = pos(t.ms_arm);
  const ready = pos(t.ms_ready);
  const warm = pos(t.ms_warm_book);
  const nonce = pos(t.ms_nonce);
  const hasWindow = pos(t.ms_has_window);
  const quoteDur = pos(t.ms_quote);
  const sendDur = pos(t.ms_send);
  const fillDur = pos(t.ms_fill_wait);
  const posCheck = pos(t.ms_pos_check);
  const orderGet = pos(t.ms_order_get);
  const stopDur = pos(t.ms_stop);
  const sqlite = pos(t.ms_sqlite);
  const quoteAt = pos(t.ms_open_to_quote);
  const decideAt = pos(t.ms_open_to_decide);
  const sendAt = pos(t.ms_open_to_send);
  const ackAt = pos(t.ms_open_to_send_ack);
  const exchAt = pos(t.ms_open_to_exchange);

  const anchors = [quoteAt, decideAt, sendAt, ackAt, exchAt].filter((n): n is number => n != null);
  const firstPost = anchors.length ? Math.min(...anchors) : null;
  const armAfter = armRunsAfterOpen(arm, ready, late, firstPost, quoteDur);
  const wake = late != null && late >= 5 ? late : 0;

  const bars: GanttBar[] = [];

  let preEnd = 0;
  if (ready != null) {
    pushBar(bars, {
      id: "ready",
      label: "equity",
      title: `Equity и цена Lighter, ${roundMs(ready)} мс до колокола. Простой до :00 не измерен, полоса прижата к линии.`,
      start: -ready,
      end: 0,
      kind: "pre",
      rank: 4,
    });
    preEnd = -ready;
  }
  if (arm != null && !armAfter) {
    const start = preEnd - arm;
    pushBar(bars, {
      id: "arm",
      label: "рынок PM",
      title: `Resolve рынка Polymarket, ${roundMs(arm)} мс. Прогрев и nonce внутри этого шага.`,
      start,
      end: preEnd,
      kind: "pre",
      rank: 1,
    });
    if (warm != null) {
      pushBar(bars, {
        id: "warm",
        label: "прогрев",
        title: `Прогрев стакана, ${roundMs(warm)} мс, внутри resolve.`,
        start,
        end: start + Math.min(warm, arm),
        kind: "pre",
        rank: 2,
      });
    }
    if (nonce != null) {
      pushBar(bars, {
        id: "nonce",
        label: "nonce",
        title: `Nonce, ${roundMs(nonce)} мс, внутри resolve.`,
        start,
        end: start + Math.min(nonce, arm),
        kind: "pre",
        rank: 3,
      });
    }
  }

  const prelude = wake + (hasWindow != null && hasWindow >= 25 ? hasWindow : 0);
  if (hasWindow != null && hasWindow >= 25) {
    pushBar(bars, {
      id: "check",
      label: "проверка",
      title: `Проверка окна, ${roundMs(hasWindow)} мс.`,
      start: wake,
      end: wake + hasWindow,
      kind: "post",
      rank: 1,
    });
  }
  if (arm != null && armAfter) {
    pushBar(bars, {
      id: "arm",
      label: "рынок PM",
      title: `Resolve рынка Polymarket, ${roundMs(arm)} мс после :00.`,
      start: prelude,
      end: prelude + arm,
      kind: "post",
      rank: 2,
    });
    if (warm != null) {
      pushBar(bars, {
        id: "warm",
        label: "прогрев",
        title: `Прогрев стакана, ${roundMs(warm)} мс.`,
        start: prelude,
        end: prelude + Math.min(warm, arm),
        kind: "post",
        rank: 3,
      });
    }
    if (nonce != null) {
      pushBar(bars, {
        id: "nonce",
        label: "nonce",
        title: `Nonce, ${roundMs(nonce)} мс.`,
        start: prelude,
        end: prelude + Math.min(nonce, arm),
        kind: "post",
        rank: 4,
      });
    }
  }
  if (wake > 0 && (quoteAt != null || decideAt != null || sendAt != null)) {
    pushBar(bars, {
      id: "wake",
      label: "старт",
      title: `Процесс дошёл до решения через ${roundMs(wake)} мс после :00.`,
      start: wake,
      end: wake,
      kind: "post",
      rank: 0,
    });
  }

  const afterPrep = armAfter ? prelude + (arm ?? 0) : wake;
  if (quoteAt != null) {
    const q0 = quoteDur != null ? Math.max(afterPrep, quoteAt - quoteDur) : quoteAt;
    if (q0 - afterPrep > 15) {
      pushBar(bars, {
        id: "wait-quote",
        label: "до кадра",
        title: `От ${afterPrep === wake && wake === 0 ? "колокола" : "предыдущего шага"} до запроса котировки, ${roundMs(q0 - afterPrep)} мс.`,
        start: afterPrep,
        end: q0,
        kind: "wait",
        rank: 5,
      });
    }
    pushBar(bars, {
      id: "quote",
      label: "запрос",
      title: `Запрос котировки Polymarket, ${roundMs(quoteDur ?? quoteAt - q0)} мс, конец в ${roundMs(quoteAt)} мс после :00.`,
      start: q0,
      end: quoteAt,
      kind: "post",
      rank: 6,
    });
  }
  if (decideAt != null && (quoteAt == null || Math.abs(decideAt - quoteAt) > 3)) {
    pushBar(bars, {
      id: "decide",
      label: "решение",
      title: `Решение, ${roundMs(decideAt)} мс после :00.`,
      start: decideAt,
      end: decideAt,
      kind: "post",
      rank: 7,
    });
  }
  if (sendAt != null) {
    const sendEnd = ackAt != null ? ackAt : sendDur != null ? sendAt + sendDur : sendAt;
    pushBar(bars, {
      id: "send",
      label: "IOC",
      title: `Отправка IOC, ${roundMs(sendEnd - sendAt)} мс.`,
      start: sendAt,
      end: sendEnd,
      kind: "post",
      rank: 8,
    });
  }
  if (exchAt != null) {
    pushBar(bars, {
      id: "exchange",
      label: "биржа",
      title: `created_at ордера на Lighter, ${roundMs(exchAt)} мс после :00.`,
      start: exchAt,
      end: exchAt,
      kind: "post",
      rank: 9,
    });
  }

  let cursor = ackAt ?? (sendAt != null && sendDur != null ? sendAt + sendDur : null);
  const chain: { id: string; label: string; dur: number; title: string; rank: number }[] = [];
  if (cursor != null) {
    if (fillDur != null) chain.push({ id: "fill", label: "филл", dur: fillDur, title: `Подтверждение филла, ${roundMs(fillDur)} мс.`, rank: 10 });
    if (posCheck != null) chain.push({ id: "pos", label: "позиция", dur: posCheck, title: `Проверка позиции, ${roundMs(posCheck)} мс.`, rank: 11 });
    if (stopDur != null) chain.push({ id: "stop", label: "стоп", dur: stopDur, title: `Стоп, ${roundMs(stopDur)} мс.`, rank: 12 });
    if (orderGet != null) chain.push({ id: "stamp", label: "метка", dur: orderGet, title: `Запрос времени ордера, ${roundMs(orderGet)} мс.`, rank: 13 });
  }
  if (sqlite != null) chain.push({ id: "sql", label: "запись", dur: sqlite, title: `Запись sqlite, ${roundMs(sqlite)} мс.`, rank: 14 });
  if (chain.length) {
    if (cursor == null) {
      const placed = bars.filter((b) => b.end > 0).map((b) => b.end);
      cursor = placed.length ? Math.max(...placed) : 0;
    }
    for (const step of chain) {
      const start = cursor;
      cursor += step.dur;
      pushBar(bars, {
        id: step.id,
        label: step.label,
        title: step.title,
        start,
        end: cursor,
        kind: "post",
        rank: step.rank,
      });
    }
  }

  if (bars.length === 0) return null;
  bars.sort((a, b) => a.start - b.start || a.rank - b.rank);
  const from = Math.min(0, ...bars.map((b) => b.start));
  const to = Math.max(0, ...bars.map((b) => b.end));
  if (to - from <= 0) return null;
  return { bars, from, to };
}
