import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api } from "../api/client";
import type { Span } from "../api/types";
import { Waterfall, spanKindLabel } from "../components/Waterfall";
import { Card, ErrorState, Loading } from "../components/primitives";
import { formatDateTime, formatDuration, seriesColor } from "../format";

/** One trace: the waterfall, plus the full detail of whichever span is selected. */
export function TraceDetail() {
  const { traceId = "" } = useParams();
  const [selected, setSelected] = useState<Span | null>(null);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["trace", traceId],
    queryFn: () => api.trace(traceId),
  });

  if (isError) return <ErrorState error={error} />;
  if (isLoading || !data) return <Loading rows={8} />;

  const span = selected ?? data.spans[0];
  const errorCount = data.spans.filter((s) => s.status === 2 || (s.http_status ?? 0) >= 500).length;

  return (
    <div className="space-y-4">
      <div>
        <Link to="/traces" className="text-[12px] hover:underline" style={{ color: "var(--text-muted)" }}>
          ← Traces
        </Link>
        <div className="mt-1 flex flex-wrap items-baseline gap-x-4 gap-y-1">
          <h1 className="text-[18px] font-semibold">{data.spans[0]?.operation}</h1>
          <span className="tabular text-[12px]" style={{ color: "var(--text-muted)" }}>
            {traceId}
          </span>
        </div>
        <div className="mt-2 flex flex-wrap items-center gap-4 text-[12px]" style={{ color: "var(--text-secondary)" }}>
          <span>{formatDateTime(data.start_time)}</span>
          <span className="tabular">{formatDuration(data.duration_ns)} total</span>
          <span>{data.spans.length} spans</span>
          <span>
            {data.services.length} service{data.services.length === 1 ? "" : "s"}
          </span>
          {errorCount > 0 && (
            <span style={{ color: "var(--status-critical)" }}>
              {errorCount} failed span{errorCount === 1 ? "" : "s"}
            </span>
          )}
        </div>
      </div>

      {/* A legend is present whenever more than one service appears, so service
          identity is never carried by colour alone. */}
      {data.services.length > 1 && (
        <div className="flex flex-wrap items-center gap-3">
          {data.services.map((service) => (
            <span key={service} className="flex items-center gap-1.5 text-[12px]">
              <span
                className="inline-block h-2.5 w-2.5 rounded-sm"
                style={{ background: seriesColor(service) }}
                aria-hidden
              />
              {service}
            </span>
          ))}
        </div>
      )}

      <div className="grid gap-3 lg:grid-cols-[1fr_360px]">
        <Card title="Waterfall" subtitle="click a span for its detail">
          <Waterfall
            spans={data.spans}
            traceStart={data.start_time}
            traceDuration={data.duration_ns}
            onSelect={setSelected}
            selectedId={span?.span_id}
          />
        </Card>

        {span && <SpanDetail span={span} />}
      </div>
    </div>
  );
}

/** Everything known about one span, grouped so the important part is first. */
function SpanDetail({ span }: { span: Span }) {
  const exception = span.events?.find((event) => event.name === "exception");

  return (
    <Card title="Span detail">
      <div className="space-y-4 px-4 pb-4">
        <div>
          <h3 className="text-[13px] font-medium break-words">{span.operation}</h3>
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px]" style={{ color: "var(--text-muted)" }}>
            <span>{span.service}</span>
            <span>{spanKindLabel(span.kind)}</span>
            <span className="tabular">{formatDuration(span.duration)}</span>
          </div>
        </div>

        {exception && (
          <div
            className="rounded-md px-3 py-2"
            style={{
              background: "color-mix(in srgb, var(--status-critical) 10%, transparent)",
              border: "1px solid color-mix(in srgb, var(--status-critical) 30%, transparent)",
            }}
          >
            <div className="text-[12px] font-semibold" style={{ color: "var(--status-critical)" }}>
              {exception.attributes?.["exception.type"] ?? "Exception"}
            </div>
            {exception.attributes?.["exception.message"] && (
              <p className="mt-1 text-[12px] break-words">
                {exception.attributes["exception.message"]}
              </p>
            )}
            {exception.attributes?.["exception.stacktrace"] && (
              <details className="mt-2">
                <summary className="cursor-pointer text-[11px]" style={{ color: "var(--text-muted)" }}>
                  Stack trace
                </summary>
                <pre className="mt-1.5 max-h-64 overflow-auto text-[11px] leading-relaxed whitespace-pre-wrap">
                  {exception.attributes["exception.stacktrace"]}
                </pre>
              </details>
            )}
          </div>
        )}

        {span.db_statement && (
          <Field label={`${span.db_system ?? "database"} query`}>
            <pre
              className="overflow-x-auto rounded px-2 py-1.5 text-[11px] leading-relaxed whitespace-pre-wrap"
              style={{ background: "var(--hover-wash)" }}
            >
              {span.db_statement}
            </pre>
          </Field>
        )}

        {span.http_method && (
          <Field label="HTTP">
            <span className="tabular text-[12px]">
              {span.http_method} {span.http_route} → {span.http_status}
            </span>
          </Field>
        )}

        <Field label="Identity">
          <dl className="space-y-1 text-[11px]">
            <Row label="Span ID" value={span.span_id} />
            {span.parent_span_id && <Row label="Parent" value={span.parent_span_id} />}
            {span.host_name && <Row label="Host" value={span.host_name} />}
            {span.pid ? <Row label="PID" value={String(span.pid)} /> : null}
          </dl>
        </Field>

        {span.attributes && Object.keys(span.attributes).length > 0 && (
          <Field label="Attributes">
            <dl className="space-y-1 text-[11px]">
              {Object.entries(span.attributes)
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([key, value]) => (
                  <Row key={key} label={key} value={value} />
                ))}
            </dl>
          </Field>
        )}
      </div>
    </Card>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <div
        className="mb-1.5 text-[10px] font-medium tracking-wide uppercase"
        style={{ color: "var(--text-muted)" }}
      >
        {label}
      </div>
      {children}
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <dt className="w-32 shrink-0 truncate" style={{ color: "var(--text-muted)" }} title={label}>
        {label}
      </dt>
      <dd className="tabular min-w-0 break-all">{value}</dd>
    </div>
  );
}
