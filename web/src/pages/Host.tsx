import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { useTimeRange } from "../components/TimeRange";
import { MetricChart, formatValue } from "../components/MetricChart";
import { Card, EmptyState, ErrorState, Loading, StatTile } from "../components/primitives";

/**
 * The charts shown on the host page, in the order an operator reads them.
 *
 * A fixed list rather than everything the host reported: the point of this page
 * is answering "is the machine the problem", and four saturating resources
 * answer it. Everything else is available through the metric picker.
 */
const HOST_CHARTS = [
  { name: "system.cpu.utilization", title: "CPU", subtitle: "utilisation" },
  { name: "system.memory.utilization", title: "Memory", subtitle: "in use" },
  { name: "system.filesystem.utilization", title: "Disks", subtitle: "used, per filesystem" },
  { name: "system.network.io", title: "Network", subtitle: "bytes per interval" },
] as const;

/** How the machine itself is doing, on the same time axis as the traces. */
export function Host() {
  const { range, tick } = useTimeRange();

  const load = useQuery({
    queryKey: ["metric", "load", range, tick],
    queryFn: () => api.metric(range, "system.cpu.load_average.1m"),
    refetchInterval: 15_000,
  });

  const cpu = useQuery({
    queryKey: ["metric", "cpu", range, tick],
    queryFn: () => api.metric(range, "system.cpu.utilization"),
    refetchInterval: 15_000,
  });

  const memory = useQuery({
    queryKey: ["metric", "mem", range, tick],
    queryFn: () => api.metric(range, "system.memory.utilization"),
    refetchInterval: 15_000,
  });

  if (cpu.isError) return <ErrorState error={cpu.error} />;

  const latest = (series?: { points: { value: number }[] | null }[]) => {
    const points = series?.[0]?.points;
    return points && points.length > 0 ? points[points.length - 1].value : undefined;
  };

  const cpuNow = latest(cpu.data?.series);
  const memNow = latest(memory.data?.series);
  const loadNow = latest(load.data?.series);

  const nothingYet =
    !cpu.isLoading && (cpu.data?.series.length ?? 0) === 0 && (memory.data?.series.length ?? 0) === 0;

  if (nothingYet) {
    return (
      <Card>
        <EmptyState
          title="No host measurements in this range"
          hint="Host metrics are collected by apm2go itself. If this stays empty, check that host_metrics.enabled is on."
        />
      </Card>
    );
  }

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-3">
        <StatTile
          label="CPU"
          value={cpuNow !== undefined ? formatValue(cpuNow, "1") : "—"}
          detail="current utilisation"
          tone={cpuNow !== undefined && cpuNow > 0.9 ? "critical" : cpuNow !== undefined && cpuNow > 0.7 ? "warning" : "neutral"}
        />
        <StatTile
          label="Memory"
          value={memNow !== undefined ? formatValue(memNow, "1") : "—"}
          detail="in use"
          tone={memNow !== undefined && memNow > 0.9 ? "critical" : memNow !== undefined && memNow > 0.8 ? "warning" : "neutral"}
        />
        <StatTile
          label="Load average"
          value={loadNow !== undefined ? loadNow.toFixed(2) : "—"}
          detail="1 minute"
        />
      </div>

      <div className="grid gap-3 lg:grid-cols-2">
        {HOST_CHARTS.map((chart) => (
          <HostChart key={chart.name} {...chart} />
        ))}
      </div>
    </div>
  );
}

/** One host chart, fetched on its own so a slow query cannot block the others. */
function HostChart({
  name,
  title,
  subtitle,
}: {
  name: string;
  title: string;
  subtitle: string;
}) {
  const { range, tick } = useTimeRange();

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["metric", name, range, tick],
    queryFn: () => api.metric(range, name),
    refetchInterval: 15_000,
  });

  return (
    <Card title={title} subtitle={subtitle}>
      <div className="px-2 pb-1">
        {isError ? (
          <ErrorState error={error} />
        ) : isLoading ? (
          <Loading rows={2} />
        ) : (
          <MetricChart series={data?.series ?? []} from={data?.from} to={data?.to} />
        )}
      </div>
    </Card>
  );
}
