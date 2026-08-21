import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api } from "../api/client";
import type { JVMEntry } from "../api/types";
import { useTimeRange } from "../components/TimeRange";
import { LocationBadge, PlacementBadge } from "../components/PlacementBadge";
import { RuntimeBadge } from "../components/RuntimeBadge";
import { TimeSeriesChart } from "../components/TimeSeriesChart";
import { JvmRuntimeCharts } from "../components/JvmRuntimeCharts";
import { ProcessRuntimeCharts } from "../components/ProcessRuntimeCharts";
import {
  Card,
  EmptyState,
  ErrorState,
  Loading,
  StateBadge,
  StatTile,
  Table,
  Td,
  Th,
} from "../components/primitives";
import { formatCount, formatDuration, formatPercent, formatRate } from "../format";

/** One service in depth: its trend lines and its slowest operations. */
export function ServiceDetail() {
  const { service = "" } = useParams();
  const { range, tick } = useTimeRange();

  const operations = useQuery({
    queryKey: ["operations", service, range, tick],
    queryFn: () => api.operations(service, range),
    refetchInterval: 10_000,
  });

  const timeseries = useQuery({
    queryKey: ["timeseries", service, range, tick],
    queryFn: () => api.timeSeries(range, service),
    refetchInterval: 10_000,
  });

  const services = useQuery({
    queryKey: ["services", range, tick],
    queryFn: () => api.services(range),
  });

  const jvms = useQuery({
    queryKey: ["jvms"],
    queryFn: api.jvms,
    refetchInterval: 10_000,
  });

  if (operations.isError) return <ErrorState error={operations.error} />;

  const summary = services.data?.services.find((s) => s.service === service);
  const rows = operations.data?.operations ?? [];
  const instances = (jvms.data ?? []).filter((e) => e.jvm.service_name === service);
  // Placement is a property of the process, not of the service name, so a
  // service backed by more than one instance only gets one badge — the first
  // is as good a representative as any until the instances disagree.
  const placementJvm = instances[0]?.jvm;

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <Link
            to="/services"
            className="text-[12px] hover:underline"
            style={{ color: "var(--text-muted)" }}
          >
            ← Services
          </Link>
          <h1 className="mt-1 flex items-center gap-2 text-[20px] font-semibold">
            <RuntimeBadge runtime={summary?.runtime} size={20} />
            {placementJvm && <PlacementBadge jvm={placementJvm} size={18} />}
            {service}
            {placementJvm && <LocationBadge jvm={placementJvm} />}
          </h1>
        </div>
        <Link
          to={`/traces?service=${encodeURIComponent(service)}`}
          className="rounded-md px-3 py-1.5 text-[13px] font-medium"
          style={{ border: "1px solid var(--border-strong)", color: "var(--text-secondary)" }}
        >
          View traces
        </Link>
      </div>

      {summary && (
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          <StatTile label="Throughput" value={formatRate(summary.requests_per_second)} />
          <StatTile
            label="Error rate"
            value={formatPercent(summary.error_rate)}
            detail={`${formatCount(summary.error_count)} failed`}
            tone={summary.error_rate > 0.05 ? "critical" : summary.error_rate > 0.01 ? "warning" : "good"}
          />
          <StatTile label="p95 latency" value={formatDuration(summary.p95_latency_ns)} />
          <StatTile label="p99 latency" value={formatDuration(summary.p99_latency_ns)} />
        </div>
      )}

      <div className="grid gap-3 lg:grid-cols-3">
        <Card title="Throughput" subtitle="requests per second">
          <div className="px-2 pb-2">
            <TimeSeriesChart
              points={timeseries.data?.points ?? []}
              from={timeseries.data?.from}
              to={timeseries.data?.to}
              measure="rate"
            />
          </div>
        </Card>
        <Card title="Latency" subtitle="95th percentile">
          <div className="px-2 pb-2">
            <TimeSeriesChart
              points={timeseries.data?.points ?? []}
              from={timeseries.data?.from}
              to={timeseries.data?.to}
              measure="p95"
            />
          </div>
        </Card>
        <Card title="Errors" subtitle="failed requests per bucket">
          <div className="px-2 pb-2">
            <TimeSeriesChart
              points={timeseries.data?.points ?? []}
              from={timeseries.data?.from}
              to={timeseries.data?.to}
              measure="errors"
            />
          </div>
        </Card>
      </div>

      <ServiceJvms instances={instances} />
      <JvmRuntimeCharts service={service} />
      <ProcessRuntimeCharts service={service} />

      <Card title="Operations" subtitle="sorted by request volume">
        {operations.isLoading ? (
          <Loading />
        ) : rows.length === 0 ? (
          <EmptyState title="No operations recorded in this range" />
        ) : (
          <Table
            head={
              <>
                <Th>Operation</Th>
                <Th>Kind</Th>
                <Th align="right">Rate</Th>
                <Th align="right">Requests</Th>
                <Th align="right">Errors</Th>
                <Th align="right">p50</Th>
                <Th align="right">p95</Th>
                <Th align="right">p99</Th>
              </>
            }
          >
            {rows.map((op) => (
              <tr key={`${op.operation}-${op.kind}`} className="hover:bg-[var(--hover-wash)]">
                <Td>
                  <Link
                    to={`/traces?service=${encodeURIComponent(service)}&operation=${encodeURIComponent(op.operation)}`}
                    className="hover:underline"
                    title={op.operation}
                  >
                    {op.operation}
                  </Link>
                </Td>
                <Td>
                  <span className="text-[12px]" style={{ color: "var(--text-muted)" }}>
                    {op.kind}
                  </span>
                </Td>
                <Td align="right">{formatRate(op.requests_per_second)}</Td>
                <Td align="right">{formatCount(op.span_count)}</Td>
                <Td align="right">
                  <span
                    style={{
                      color: op.error_rate > 0.01 ? "var(--status-critical)" : "var(--text-primary)",
                    }}
                  >
                    {formatPercent(op.error_rate)}
                  </span>
                </Td>
                <Td align="right">{formatDuration(op.p50_latency_ns)}</Td>
                <Td align="right">{formatDuration(op.p95_latency_ns)}</Td>
                <Td align="right">{formatDuration(op.p99_latency_ns)}</Td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
    </div>
  );
}

/**
 * A chip per JVM backing this service, linking straight into its thread dumps
 * and heap histograms.
 *
 * Diagnostics are addressed by pid, not by service, so this is the bridge: the
 * one place on a service page that says which live process to go look inside,
 * and takes you there in one click. Renders nothing when the inventory has no
 * matching process — a server-mode install with no local JVMs, for instance.
 */
function ServiceJvms({ instances }: { instances: JVMEntry[] }) {
  if (instances.length === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-[11px] font-medium tracking-wide uppercase" style={{ color: "var(--text-muted)" }}>
        JVM
      </span>
      {instances.map((entry) => (
        <Link
          key={entry.jvm.pid}
          to={`/jvms/${entry.jvm.pid}`}
          className="flex items-center gap-1.5 rounded-md px-2.5 py-1 text-[12px]"
          style={{ border: "1px solid var(--border-strong)", color: "var(--text-secondary)" }}
        >
          <StateBadge state={entry.state} title={entry.reason} />
          <PlacementBadge jvm={entry.jvm} size={14} />
          <span className="tabular">pid {entry.jvm.pid}</span>
          <span style={{ color: "var(--text-muted)" }}>· diagnostics</span>
        </Link>
      ))}
    </div>
  );
}
