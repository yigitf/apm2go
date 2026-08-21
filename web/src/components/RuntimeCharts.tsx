import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { useTimeRange } from "./TimeRange";
import { MetricChart } from "./MetricChart";
import { Card, Loading } from "./primitives";

export interface RuntimeChartSpec {
  name: string;
  title: string;
  subtitle: string;
}

/**
 * A row of runtime metric charts for one service, showing only the ones it
 * actually reports.
 *
 * The instruments that exist for a service depend entirely on how apm2go is
 * watching it: the OpenTelemetry Java agent reports heap and GC, apm2go's own
 * /proc-based sampling reports CPU and memory for everything eBPF discovers,
 * and neither knows about the other's instruments. This component does not
 * care which produced what — it renders whatever from `charts` is present and
 * nothing when none of it is, so a service watched a different way, or not
 * yet reporting, simply shows no panel rather than an empty frame.
 */
export function RuntimeCharts({
  service,
  charts,
  columns = 3,
}: {
  service: string;
  charts: readonly RuntimeChartSpec[];
  columns?: number;
}) {
  const { range, tick } = useTimeRange();

  const { data } = useQuery({
    queryKey: ["metric-names", service, range, tick],
    queryFn: () => api.metricNames(range, service),
    refetchInterval: 30_000,
  });

  const available = new Set((data?.metrics ?? []).map((m) => m.name));
  const present = charts.filter((chart) => available.has(chart.name));
  if (present.length === 0) return null;

  return (
    <div className={`grid gap-3 ${columns === 2 ? "lg:grid-cols-2" : "lg:grid-cols-3"}`}>
      {present.map((chart) => (
        <ServiceMetricChart key={chart.name} service={service} {...chart} />
      ))}
    </div>
  );
}

/** One metric chart scoped to a service. */
function ServiceMetricChart({ service, name, title, subtitle }: { service: string } & RuntimeChartSpec) {
  const { range, tick } = useTimeRange();

  const { data, isLoading } = useQuery({
    queryKey: ["metric", service, name, range, tick],
    queryFn: () => api.metric(range, name, service),
    refetchInterval: 15_000,
  });

  return (
    <Card title={title} subtitle={subtitle}>
      <div className="px-2 pb-1">
        {isLoading ? <Loading rows={2} /> : <MetricChart series={data?.series ?? []} from={data?.from} to={data?.to} />}
      </div>
    </Card>
  );
}
