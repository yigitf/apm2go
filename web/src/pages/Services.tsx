import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { useTimeRange } from "../components/TimeRange";
import { LocationBadge, PlacementBadge, useServicePlacements } from "../components/PlacementBadge";
import { RuntimeBadge } from "../components/RuntimeBadge";
import { Card, EmptyState, ErrorState, Loading } from "../components/primitives";
import { formatCount, formatDuration, formatPercent, formatRate } from "../format";

/** A card per service, ordered by traffic. */
export function Services() {
  const { range, tick } = useTimeRange();

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["services", range, tick],
    queryFn: () => api.services(range),
    refetchInterval: 10_000,
  });

  // Read before either early return: a hook can't follow one conditionally.
  const placements = useServicePlacements();

  if (isError) return <ErrorState error={error} />;
  if (isLoading) return <Loading rows={5} />;

  const services = data?.services ?? [];

  return (
    <div className="space-y-4">
      {services.length === 0 ? (
        <Card>
          <EmptyState
            title="No services are reporting"
            hint="Attach a JVM from the JVMs page, or widen the time range."
          />
        </Card>
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {services.map((service) => {
            const jvm = placements.get(service.service);
            return (
        <Link key={service.service} to={`/services/${encodeURIComponent(service.service)}`}>
          <div className="card h-full px-4 py-3 transition-colors hover:bg-[var(--hover-wash)]">
            <div className="flex items-center gap-2">
              <RuntimeBadge runtime={service.runtime} size={18} />
              {jvm && <PlacementBadge jvm={jvm} size={16} />}
              <h3 className="min-w-0 truncate text-[14px] font-semibold">{service.service}</h3>
            </div>
            {jvm && (
              <div className="mt-1.5">
                <LocationBadge jvm={jvm} />
              </div>
            )}

            <div className="mt-3 grid grid-cols-3 gap-2">
              <Metric label="Rate" value={formatRate(service.requests_per_second)} />
              <Metric
                label="Errors"
                value={formatPercent(service.error_rate)}
                tone={service.error_rate > 0.01 ? "critical" : undefined}
              />
              <Metric label="p95" value={formatDuration(service.p95_latency_ns)} />
            </div>

            <p className="mt-3 text-[11px]" style={{ color: "var(--text-muted)" }}>
              {formatCount(service.span_count)} requests in range
            </p>
          </div>
        </Link>
            );
          })}
        </div>
      )}

      <Quiet reporting={services.map((s) => s.service)} />
    </div>
  );
}

/**
 * The processes apm2go is watching that have not reported a request.
 *
 * Without this the page is a lie by omission. Everything above it is derived
 * from stored spans, so a service that is being instrumented and has simply
 * been idle appears nowhere — and "nowhere" is exactly where a service apm2go
 * never found also appears. On a real host that is not a corner case: an nginx
 * in front of an application nobody is using is watched, is reporting CPU and
 * memory, and looked, until this existed, like a discovery failure.
 */
function Quiet({ reporting }: { reporting: string[] }) {
  const { data } = useQuery({
    queryKey: ["processes"],
    queryFn: api.processes,
    refetchInterval: 15_000,
  });

  const loud = new Set(reporting);
  const quiet = (data ?? []).filter((process) => !loud.has(process.service));
  if (quiet.length === 0) return null;

  return (
    <Card
      title="Watched, no requests yet"
      subtitle="instrumented and reporting resource use; nothing has called them in this range"
    >
      <div className="grid gap-x-4 gap-y-2 px-4 py-3 md:grid-cols-2 xl:grid-cols-3">
        {quiet.map((process) => (
          <div key={`${process.service}-${process.pid}`} className="flex items-center gap-2 text-[13px]">
            <RuntimeBadge runtime={process.runtime} />
            <span className="truncate font-medium">{process.service}</span>
            <span className="tabular shrink-0 text-[11px]" style={{ color: "var(--text-muted)" }}>
              pid {process.pid}
              {process.ports && process.ports.length > 0 && ` · :${process.ports.join(", :")}`}
            </span>
            {process.container?.name && (
              <span className="truncate text-[11px]" style={{ color: "var(--text-muted)" }}>
                {process.container.name}
              </span>
            )}
          </div>
        ))}
      </div>
    </Card>
  );
}

function Metric({
  label,
  value,
  tone,
}: {
  label: string;
  value: string;
  tone?: "critical";
}) {
  return (
    <div>
      <div className="text-[11px]" style={{ color: "var(--text-muted)" }}>
        {label}
      </div>
      <div
        className="tabular mt-0.5 text-[15px] font-semibold"
        style={{ color: tone === "critical" ? "var(--status-critical)" : "var(--text-primary)" }}
      >
        {value}
      </div>
    </div>
  );
}
