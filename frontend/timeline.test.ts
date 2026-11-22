import assert from "node:assert/strict";
import { buildGantt, buildTimeline } from "./src/timeline.ts";

const skip = buildTimeline({
  ms_arm: 1812,
  ms_ready: 330,
  ms_warm_book: 1200,
  ms_nonce: 242,
  ms_quote: 3,
  ms_open_to_quote: 1215,
  ms_open_to_decide: 1215,
});
assert.ok(skip);
assert.deepEqual(
  skip.before.map((n) => n.label),
  ["рынок PM", "equity"],
);
assert.deepEqual(
  skip.beforeGaps.map((g) => g.label),
  ["1812", "330"],
);
assert.equal(skip.toOpen?.dashed, true);
assert.deepEqual(
  skip.after.map((n) => n.label),
  ["котировка · решение"],
);
assert.deepEqual(
  skip.afterGaps.map((g) => g.label),
  ["1215"],
);
assert.match(skip.after[0].detail ?? "", /запрос 3/);
assert.match(skip.before[0].detail ?? "", /прогрев 1200/);
assert.match(skip.before[0].detail ?? "", /nonce 242/);

const trade = buildTimeline({
  ms_late_start: 18,
  ms_arm: 900,
  ms_ready: 280,
  ms_warm_book: 700,
  ms_nonce: 90,
  ms_quote: 6,
  ms_open_to_quote: 40,
  ms_open_to_decide: 44,
  ms_open_to_send: 52,
  ms_send: 38,
  ms_open_to_send_ack: 91,
  ms_open_to_exchange: 105,
  ms_fill_wait: 70,
  ms_stop: 180,
  ms_order_get: 25,
  ms_sqlite: 4,
});
assert.ok(trade);
assert.deepEqual(
  trade.before.map((n) => n.label),
  ["рынок PM", "equity"],
);
assert.deepEqual(
  trade.after.map((n) => n.label),
  ["старт", "котировка", "решение", "IOC", "ответ", "биржа", "филл", "стоп", "метка", "запись"],
);
assert.equal(trade.afterGaps[0].label, "18");
assert.equal(trade.afterGaps[1].label, "22");
assert.equal(trade.afterGaps[4].label, "39");
assert.equal(trade.afterGaps[5].label, "14");
assert.equal(trade.after.find((n) => n.id === "exchange")?.title.includes("лаг входа"), true);
assert.equal(trade.afterGaps[6].approx, true);

const lateArm = buildTimeline({
  ms_late_start: 30,
  ms_arm: 700,
  ms_warm_book: 500,
  ms_nonce: 40,
  ms_quote: 5,
  ms_open_to_quote: 760,
  ms_open_to_decide: 762,
});
assert.ok(lateArm);
assert.equal(lateArm.before.length, 0);
assert.equal(lateArm.toOpen, null);
assert.deepEqual(
  lateArm.after.map((n) => n.label),
  ["старт", "рынок PM", "котировка · решение"],
);
assert.deepEqual(
  lateArm.afterGaps.map((g) => g.label),
  ["30", "700", "30"],
);
assert.equal(lateArm.afterGaps[1].approx, undefined);

const shortPrewarm = buildTimeline({
  ms_arm: 200,
  ms_quote: 3,
  ms_open_to_quote: 400,
  ms_open_to_decide: 402,
});
assert.ok(shortPrewarm);
assert.deepEqual(
  shortPrewarm.before.map((n) => n.label),
  ["рынок PM"],
);
assert.equal(shortPrewarm.after[0].label, "котировка · решение");
assert.equal(shortPrewarm.afterGaps[0].label, "400");

assert.equal(buildTimeline(null), null);
assert.equal(buildTimeline({}), null);
assert.equal(buildTimeline({ ms_quote: 0, ms_arm: 0 }), null);

const gantt = buildGantt({
  ms_arm: 1812,
  ms_ready: 330,
  ms_warm_book: 1200,
  ms_nonce: 242,
  ms_quote: 3,
  ms_open_to_quote: 1215,
  ms_open_to_decide: 1215,
});
assert.ok(gantt);
const byId = Object.fromEntries(gantt.bars.map((b) => [b.id, b]));
assert.equal(byId.ready.start, -330);
assert.equal(byId.ready.end, 0);
assert.equal(byId.arm.start, -2142);
assert.equal(byId.arm.end, -330);
assert.equal(byId.warm.start, -2142);
assert.equal(byId.warm.end, -2142 + 1200);
assert.equal(byId["wait-quote"].start, 0);
assert.equal(byId["wait-quote"].end, 1212);
assert.equal(byId.quote.start, 1212);
assert.equal(byId.quote.end, 1215);
assert.equal(gantt.from, -2142);
assert.equal(gantt.to, 1215);
const armShare = (byId.arm.end - byId.arm.start) / (gantt.to - gantt.from);
const quoteShare = (byId.quote.end - byId.quote.start) / (gantt.to - gantt.from);
assert.ok(armShare > 0.5);
assert.ok(quoteShare < 0.01);

console.log("timeline ok");
