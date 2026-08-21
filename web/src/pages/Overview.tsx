import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { useTimeRange } from "../components/TimeRange";
import { RuntimeBadge } from "../components/RuntimeBadge";
import { TimeSeriesChart } from "../components/TimeSeriesChart";
import {
  Card,
  EmptyState,
  ErrorState,
  Loading,
  StatTile,
  Table,
  Td,
  Th,
} from "../components/primitives";
import { formatCount, formatDuration, formatPercent, formatRate } from "../format";

/**
 * The landing view: how much traffic the host is serving, how much of it is
 * failing, and how slow it is — then the same three numbers per service.
 */
export function Overview() {
  const { range, tick } = useTimeRange();

  const services = useQuery({
    queryKey: ["services", range, tick],
    queryFn: () => api.services(range),
    refetchInterval: 10_000,
  });

  const timeseries = useQuery({
    queryKey: ["timeseries", range, tick],
    queryFn: () => api.timeSeries(range),
    refetchInterval: 10_000,
  });

  if (services.isError) return <ErrorState error={services.error} />;

  const rows = services.data?.services ?? [];
  const totals = rows.reduce(
    (acc, s) => ({
      spans: acc.spans + s.span_count,
      errors: acc.errors + s.error_count,
      // The worst service's p95 is the honest headline: averaging percentiles
      // across services would understate the one that is actually hurting.
      p95: Math.max(acc.p95, s.p95_latency_ns),
    }),
    { spans: 0, errors: 0, p95: 0 },
  );

  const errorRate = totals.spans > 0 ? totals.errors / totals.spans : 0;

  // Throughput is read off the most recent bucket rather than averaged over the
  // whole window. A service that started two minutes into a one-hour range
  // would otherwise show a rate fifty times below what its own chart plots,
  // right beside that chart.
  const points = timeseries.data?.points ?? [];
  const currentRate = points.length > 0 ? points[points.length - 1].rate : 0;

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile
          label="Throughput"
          value={formatRate(currentRate)}
          detail={`${formatCount(totals.spans)} requests in range`}
        />
        <StatTile
          label="Error rate"
          value={formatPercent(errorRate)}
          detail={`${formatCount(totals.errors)} failed`}
          tone={errorRate > 0.05 ? "critical" : errorRate > 0.01 ? "warning" : "good"}
        />
        <StatTile
          label="Slowest p95"
          value={formatDuration(totals.p95)}
          detail="across all services"
        />
        <StatTile
          label="Services"
          value={String(rows.length)}
          detail="reporting traces"
        />
      </div>

      {/* Two stacked charts rather than one with two y-axes: throughput and
          latency share a time axis but nothing else. */}
      <div className="grid gap-3 lg:grid-cols-2">
        <Card title="Throughput" subtitle="requests per second">
          <div className="px-2 pb-2">
            {timeseries.isLoading ? (
              <Loading rows={2} />
            ) : (
              <TimeSeriesChart
                points={timeseries.data?.points ?? []}
                from={timeseries.data?.from}
                to={timeseries.data?.to}
                measure="rate"
              />
            )}
          </div>
        </Card>
        <Card title="Latency" subtitle="95th percentile">
          <div className="px-2 pb-2">
            {timeseries.isLoading ? (
              <Loading rows={2} />
            ) : (
              <TimeSeriesChart
                points={timeseries.data?.points ?? []}
                from={timeseries.data?.from}
                to={timeseries.data?.to}
                measure="p95"
              />
            )}
          </div>
        </Card>
      </div>

      <Card title="Services" subtitle="rate, errors and duration">
        {services.isLoading ? (
          <Loading />
        ) : rows.length === 0 ? (
          <EmptyState
            title="No traces in this range"
            hint="Check the JVMs page: a discovered process must be attached before it reports traces."
          />
        ) : (
          <Table
            head={
              <>
                <Th>Service</Th>
                <Th align="right">Rate</Th>
                <Th align="right">Requests</Th>
                <Th align="right">Errors</Th>
                <Th align="right">p50</Th>
                <Th align="right">p95</Th>
                <Th align="right">p99</Th>
              </>
            }
          >
            {rows.map((service) => (
              <tr key={service.service} className="hover:bg-[var(--hover-wash)]">
                <Td>
                  <Link
                    to={`/services/${encodeURIComponent(service.service)}`}
                    className="flex items-center gap-2 font-medium hover:underline"
                  >
                    <RuntimeBadge runtime={service.runtime} />
                    {service.service}
                  </Link>
                </Td>
                <Td align="right">{formatRate(service.requests_per_second)}</Td>
                <Td align="right">{formatCount(service.span_count)}</Td>
                <Td align="right">
                  <span
                    style={{
                      color:
                        service.error_rate > 0.01 ? "var(--status-critical)" : "var(--text-primary)",
                    }}
                  >
                    {formatPercent(service.error_rate)}
                  </span>
                </Td>
                <Td align="right">{formatDuration(service.p50_latency_ns)}</Td>
                <Td align="right">{formatDuration(service.p95_latency_ns)}</Td>
                <Td align="right">{formatDuration(service.p99_latency_ns)}</Td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
    </div>
  );
}
