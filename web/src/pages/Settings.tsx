import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { Card, ErrorState, Loading, StatTile } from "../components/primitives";
import { ThemeToggle } from "../components/ThemeToggle";
import { formatCount, formatRelative, formatUptime } from "../format";

/**
 * The one thing an operator can actually set from inside the UI, plus a
 * read-only account of how the process is coping.
 *
 * The counters below answer the question an operator has when traces stop
 * arriving: is nothing being sent, or is apm2go dropping what it receives?
 * They read from the config file, which is the only place the rest of this
 * page's namesake — configuration — is actually changed.
 */
export function Settings() {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["self"],
    queryFn: api.self,
    refetchInterval: 5_000,
  });

  if (isError) return <ErrorState error={error} />;
  if (isLoading || !data) return <Loading rows={5} />;

  const pipeline = data.pipeline;
  const dropped = (pipeline?.dropped_queue_full ?? 0) + (pipeline?.dropped_rate_limit ?? 0);

  return (
    <div className="space-y-4">
      <Card title="Appearance">
        <div className="flex items-center justify-between gap-4 px-4 pt-1 pb-4">
          <p className="text-[12px]" style={{ color: "var(--text-muted)" }}>
            Follows your system by default; light and dark are also available on
            their own.
          </p>
          <ThemeToggle skin="page" showLabels />
        </div>
      </Card>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile label="Uptime" value={formatUptime(data.uptime_sec)} detail={data.mode} />
        <StatTile
          label="Spans stored"
          value={formatCount(data.storage?.span_count ?? 0)}
          detail={`${data.storage?.services ?? 0} services`}
        />
        <StatTile
          label="Spans dropped"
          value={formatCount(dropped)}
          detail={dropped > 0 ? "raise the queue size or lower sampling" : "none"}
          tone={dropped > 0 ? "warning" : "good"}
        />
        <StatTile
          label="Write errors"
          value={formatCount(pipeline?.write_errors ?? 0)}
          tone={(pipeline?.write_errors ?? 0) > 0 ? "critical" : "good"}
        />
      </div>

      <div className="grid gap-3 lg:grid-cols-2">
        <Card title="Ingest" subtitle="what the receiver has seen">
          <dl className="space-y-1.5 px-4 pt-1 pb-4 text-[13px]">
            <Row
              label="Listening on"
              value={(data.receiver?.listen_addresses ?? [data.config_hint.otlp_grpc]).join(", ")}
            />
            <Row label="OTLP HTTP" value={data.config_hint.otlp_http} />
            <Row
              label="Rejected (no token)"
              value={formatCount(data.receiver?.unauthenticated ?? 0)}
            />
            <Row label="Spans accepted" value={formatCount(data.receiver?.spans_accepted ?? 0)} />
            <Row label="Metrics accepted" value={formatCount(data.receiver?.metrics_accepted ?? 0)} />
            <Row label="Malformed spans" value={formatCount(data.receiver?.spans_malformed ?? 0)} />
            <Row
              label="Last export received"
              value={
                data.receiver?.last_received_at
                  ? formatRelative(new Date(data.receiver.last_received_at * 1000).toISOString())
                  : "never"
              }
            />
            <Row
              label="Queue"
              value={`${pipeline?.queue_depth ?? 0} / ${formatCount(pipeline?.queue_capacity ?? 0)}`}
            />
            <Row label="Last flush" value={`${pipeline?.last_flush_ms ?? 0}ms`} />
          </dl>
        </Card>

        <Card title="Configuration" subtitle="read from the config file at start-up">
          <dl className="space-y-1.5 px-4 pt-1 pb-4 text-[13px]">
            <Row label="Auto-attach" value={data.config_hint.auto_attach ? "enabled" : "disabled"} />
            <Row label="Sample ratio" value={String(data.config_hint.sample_ratio)} />
            <Row label="Max spans/s" value={formatCount(data.config_hint.max_spans_per_s)} />
            <Row label="Span retention" value={data.config_hint.span_retention} />
            <Row label="Rollup retention" value={data.config_hint.rollup_retention} />
            <Row label="OpenTelemetry agent" value={data.otel_agent} />
            <Row label="Services tracked" value={String(data.cardinality?.services ?? 0)} />
            <Row label="Operations tracked" value={String(data.cardinality?.operations ?? 0)} />
          </dl>
        </Card>
      </div>

      <Card title="Version">
        <p className="tabular px-4 pt-1 pb-4 text-[12px]" style={{ color: "var(--text-secondary)" }}>
          {data.version}
        </p>
      </Card>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-4">
      <dt style={{ color: "var(--text-muted)" }}>{label}</dt>
      <dd className="tabular">{value}</dd>
    </div>
  );
}
