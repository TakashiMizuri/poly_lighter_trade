import { Fragment, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import type { WindowTimings } from "./api";
import { buildGantt, buildTimeline, type GanttModel, type TimelineGap, type TimelineNode } from "./timeline";

function Track({ children, live }: { children: ReactNode; live?: boolean }) {
  return (
    <div className="flex items-start" data-timeline-track={live ? "1" : undefined}>
      {children}
    </div>
  );
}

function Column({
  title,
  top,
  mid,
  bot,
  marker,
}: {
  title: string;
  top?: ReactNode;
  mid: ReactNode;
  bot?: ReactNode;
  marker?: string;
}) {
  return (
    <div title={title} data-timeline={marker} className="flex w-max min-w-10 max-w-36 shrink-0 flex-col items-center">
      <div className="flex h-8 w-full items-end justify-center px-1 text-center text-[10px] leading-tight">{top}</div>
      <div className="flex h-6 w-full items-center justify-center">{mid}</div>
      <div className="flex h-8 w-full items-start justify-center px-1 pt-0.5 text-center text-[10px] leading-tight text-muted-foreground tabular-nums">
        {bot}
      </div>
    </div>
  );
}

function Line({ dashed }: { dashed?: boolean }) {
  if (dashed) {
    return (
      <div className="h-px w-full bg-[repeating-linear-gradient(90deg,var(--color-foreground)_0_4px,transparent_4px_8px)] opacity-45" />
    );
  }
  return <div className="h-px w-full bg-foreground/40" />;
}

function GapCol({ gap }: { gap: TimelineGap }) {
  return (
    <Column
      title={gap.title}
      marker={gap.dashed ? "wait" : `gap:${gap.label}`}
      top={<span className="whitespace-nowrap font-medium text-muted-foreground tabular-nums">{gap.label}</span>}
      mid={<Line dashed={gap.dashed} />}
    />
  );
}

function Dot({ kind }: { kind: "pre" | "post" | "open" }) {
  if (kind === "open") {
    return <span className="relative z-10 h-2.5 w-2.5 rotate-45 bg-foreground ring-2 ring-background" />;
  }
  return (
    <span
      className={
        kind === "pre"
          ? "relative z-10 h-1.5 w-1.5 rounded-full border border-foreground/70 bg-background"
          : "relative z-10 h-1.5 w-1.5 rounded-full bg-foreground"
      }
    />
  );
}

function Marker({ kind }: { kind: "pre" | "post" | "open" }) {
  return (
    <span className="relative flex w-full items-center justify-center">
      <span className="absolute inset-x-0 top-1/2 h-px -translate-y-1/2 bg-foreground/40" />
      <Dot kind={kind} />
    </span>
  );
}

function NodeCol({ node, compact }: { node: TimelineNode; compact?: boolean }) {
  const detail = node.detail && (!compact || node.detail.length <= 16) ? node.detail : null;
  return (
    <Column
      title={node.title}
      marker={`node:${node.id}`}
      top={<span className="line-clamp-2">{node.label}</span>}
      mid={<Marker kind={node.kind} />}
      bot={detail ? <span className="line-clamp-2 font-sans">{detail}</span> : null}
    />
  );
}

function OpenCol() {
  return <Column title="Открытие окна, :00 UTC" marker="open" mid={<Marker kind="open" />} />;
}

function Side({
  align,
  visible,
  balance,
}: {
  align: "start" | "end";
  visible: ReactNode;
  balance: ReactNode;
}) {
  const place = align === "end" ? "justify-items-end" : "justify-items-start";
  return (
    <div className={`grid w-full ${place}`}>
      <div className="col-start-1 row-start-1">{visible}</div>
      <div className="invisible col-start-1 row-start-1" aria-hidden>
        {balance}
      </div>
    </div>
  );
}

function TrackNodes({
  nodes,
  gaps,
  tail,
  live,
  compact,
}: {
  nodes: TimelineNode[];
  gaps: TimelineGap[];
  tail?: TimelineGap | null;
  live?: boolean;
  compact?: boolean;
}) {
  return (
    <Track live={live}>
      {nodes.map((node, i) => (
        <Fragment key={node.id}>
          <GapCol gap={gaps[i]} />
          <NodeCol node={node} compact={compact} />
        </Fragment>
      ))}
      {tail ? <GapCol gap={tail} /> : null}
    </Track>
  );
}

function useCenterOnOpen(key: string) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    let centered = false;
    const center = () => {
      if (centered) return;
      const open = el.querySelector('[data-timeline="open"]');
      if (!open || el.clientWidth < 8) return;
      const mark = open.getBoundingClientRect();
      const view = el.getBoundingClientRect();
      el.scrollLeft += mark.left + mark.width / 2 - (view.left + view.width / 2);
      centered = true;
    };
    center();
    const obs = new ResizeObserver(center);
    obs.observe(el);
    return () => obs.disconnect();
  }, [key]);
  return ref;
}

const GANTT_LABEL = "8.75rem";

export function GanttChart({ model }: { model: GanttModel }) {
  const span = model.to - model.from;
  const pct = (ms: number) => ((ms - model.from) / span) * 100;
  const openX = pct(0);
  return (
    <div className="mt-2">
      <div className="relative">
        <div
          className="pointer-events-none absolute bottom-0 top-0 z-10 w-px bg-foreground"
          style={{ left: `calc(${GANTT_LABEL} + (100% - ${GANTT_LABEL}) * ${openX / 100})` }}
        />
        {model.bars.map((bar) => {
          const left = pct(bar.start);
          const width = pct(bar.end) - left;
          const mark = bar.end <= bar.start;
          const ms = mark ? `+${Math.round(bar.start)}` : String(Math.round(bar.end - bar.start));
          const tone =
            bar.kind === "pre" ? "bg-foreground/25" : bar.kind === "wait" ? "bg-foreground/10" : "bg-foreground/75";
          return (
            <div
              key={bar.id}
              className="group grid items-center hover:bg-foreground/[0.06]"
              style={{ gridTemplateColumns: `${GANTT_LABEL} minmax(0,1fr)` }}
            >
              <div className="flex items-baseline justify-end gap-1.5 pr-2 text-[10px]" title={bar.title}>
                <span className="truncate text-muted-foreground group-hover:text-foreground">{bar.label}</span>
                <span className="shrink-0 tabular-nums text-foreground">{ms}</span>
              </div>
              <div className="relative h-4">
                <div
                  title={`${bar.title} ${ms} мс`}
                  className={`absolute top-1 h-2 rounded-[1px] ${tone} group-hover:bg-foreground`}
                  style={
                    mark
                      ? { left: `${left}%`, width: 2, marginLeft: -1 }
                      : { left: `${left}%`, width: `max(${width}%, 2px)` }
                  }
                />
              </div>
            </div>
          );
        })}
      </div>
      <p className="mt-1.5 text-[10px] leading-snug text-muted-foreground">
        Число — миллисекунды этапа, «+» — момент после колокола. Линия — :00. Подготовка прижата к ней.
      </p>
    </div>
  );
}

export function TimingsView({ t, compact }: { t?: WindowTimings | null; compact?: boolean }) {
  const [showGantt, setShowGantt] = useState(false);
  const gantt = buildGantt(t);
  const model = buildTimeline(t);
  const scrollerRef = useCenterOnOpen(model?.summary ?? "");
  if (!model) return <span className="text-muted-foreground">—</span>;

  const before = <TrackNodes nodes={model.before} gaps={model.beforeGaps} tail={model.toOpen} live compact={compact} />;
  const after = <TrackNodes nodes={model.after} gaps={model.afterGaps} live compact={compact} />;
  const beforeBalance = <TrackNodes nodes={model.before} gaps={model.beforeGaps} tail={model.toOpen} compact={compact} />;
  const afterBalance = <TrackNodes nodes={model.after} gaps={model.afterGaps} compact={compact} />;

  return (
    <div className="min-w-0">
      <div ref={scrollerRef} className="overflow-x-auto pb-3" role="group" aria-label={model.summary}>
        <div className="grid w-max min-w-full grid-cols-[1fr_auto_1fr] items-start">
          <Side align="end" visible={before} balance={afterBalance} />
          <OpenCol />
          <Side align="start" visible={after} balance={beforeBalance} />
        </div>
      </div>
      {gantt ? (
        <button
          type="button"
          className="mt-1 text-[10px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
          onClick={() => setShowGantt((v) => !v)}
          aria-expanded={showGantt}
        >
          {showGantt ? "Скрыть Гант" : "Гант"}
        </button>
      ) : null}
      {showGantt && gantt ? <GanttChart model={gantt} /> : null}
    </div>
  );
}
