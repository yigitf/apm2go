import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { useTimeRange } from "../components/TimeRange";
import { RuntimeBadge, useServiceRuntimes } from "../components/RuntimeBadge";
import { Card, EmptyState, ErrorState, Loading, Table, Td, Th } from "../components/primitives";
import { formatDuration, formatTime, truncate } from "../format";

/** Latency filters offered as presets, since typing a duration is fiddly. */
const DURATION_FILTERS = [
  { label: "Any", value: "" },
  { label: "> 100ms", value: "100ms" },
  { label: "> 500ms", value: "500ms" },
  { label: "> 1s", value: "1s" },
  { label: "> 5s", value: "5s" },
];

/** The trace list: filter down to the requests worth opening. */
export function Traces() {
  const { range, tick } = useTimeRange();
  const runtimes = useServiceRuntimes();
  const [params, setParams] = useSearchParams();

  const service = params.get("service") ?? "";
  const operation = params.get("operation") ?? "";
  const onlyErrors = params.get("status") === "error";
  const minDuration = params.get("min_duration") ?? "";
  const [search, setSearch] = useState(params.get("search") ?? "");

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  };

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["traces", range, service, operation, onlyErrors, minDuration, params.get("search"), tick],
    queryFn: () =>
      api.traces(range, {
        service: service || undefined,
        operation: operation || undefined,
        search: params.get("search") ?? undefined,
        status: onlyErrors ? "error" : undefined,
        min_duration: minDuration || undefined,
        limit: 200,
      }),
    refetchInterval: 10_000,
  });

  const traces = data?.traces ?? [];

  return (
    <div className="space-y-3">
      {/* Filters sit in one row above the results, so their effect on the list
          below is immediately visible. */}
      <div className="flex flex-wrap items-center gap-2">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            setParam("search", search);
          }}
        >
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search operations, routes, SQL…"
            className="w-72 rounded-md px-2.5 py-1.5 text-[13px]"
            style={{
              background: "var(--surface-1)",
              border: "1px solid var(--border-strong)",
              color: "var(--text-primary)",
            }}
          />
        </form>

        <select
          value={minDuration}
          onChange={(e) => setParam("min_duration", e.target.value)}
          aria-label="Minimum duration"
          className="rounded-md px-2 py-1.5 text-[13px]"
          style={{
            background: "var(--surface-1)",
            border: "1px solid var(--border-strong)",
            color: "var(--text-primary)",
          }}
        >
          {DURATION_FILTERS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>

        <button
          type="button"
          onClick={() => setParam("status", onlyErrors ? "" : "error")}
          aria-pressed={onlyErrors}
          className="rounded-md px-2.5 py-1.5 text-[13px] font-medium"
          style={{
            border: "1px solid var(--border-strong)",
            background: onlyErrors
              ? "color-mix(in srgb, var(--status-critical) 14%, transparent)"
              : "var(--surface-1)",
            color: onlyErrors ? "var(--status-critical)" : "var(--text-secondary)",
          }}
        >
          Errors only
        </button>

        {(service || operation) && (
          <button
            type="button"
            onClick={() => {
              const next = new URLSearchParams(params);
              next.delete("service");
              next.delete("operation");
              setParams(next, { replace: true });
            }}
            className="rounded-md px-2.5 py-1.5 text-[12px]"
            style={{ border: "1px solid var(--border-strong)", color: "var(--text-secondary)" }}
          >
            {service}
            {operation ? ` · ${truncate(operation, 30)}` : ""} ✕
          </button>
        )}

        <span className="ml-auto text-[12px]" style={{ color: "var(--text-muted)" }}>
          {traces.length === 200 ? "showing first 200" : `${traces.length} traces`}
        </span>
      </div>

      <Card>
        {isError ? (
          <ErrorState error={error} />
        ) : isLoading ? (
          <Loading rows={6} />
        ) : traces.length === 0 ? (
          <EmptyState
            title="No traces match these filters"
            hint="Try widening the time range, or clearing the duration and error filters."
          />
        ) : (
          <Table
            head={
              <>
                <Th>Time</Th>
                <Th>Service</Th>
                <Th>Operation</Th>
                <Th align="right">Duration</Th>
                <Th align="right">Spans</Th>
                <Th>Status</Th>
              </>
            }
          >
            {traces.map((trace) => {
              const failed = trace.error_count > 0;
              return (
                <tr key={trace.trace_id} className="hover:bg-[var(--hover-wash)]">
                  <Td>
                    <Link to={`/traces/${trace.trace_id}`} className="tabular hover:underline">
                      {formatTime(trace.start_time)}
                    </Link>
                  </Td>
                  <Td>
                    <span className="flex items-center gap-2">
                      <RuntimeBadge runtime={runtimes.get(trace.root_service)} />
                      {trace.root_service}
                      {trace.service_count > 1 && (
                        <span className="text-[11px]" style={{ color: "var(--text-muted)" }}>
                          +{trace.service_count - 1}
                        </span>
                      )}
                    </span>
                  </Td>
                  <Td>
                    <Link to={`/traces/${trace.trace_id}`} className="hover:underline">
                      {trace.http_method && (
                        <span
                          className="mr-1.5 text-[11px] font-semibold"
                          style={{ color: "var(--text-muted)" }}
                        >
                          {trace.http_method}
                        </span>
                      )}
                      {truncate(trace.http_route || trace.root_operation, 70)}
                    </Link>
                  </Td>
                  <Td align="right">{formatDuration(trace.duration_ns)}</Td>
                  <Td align="right">{trace.span_count}</Td>
                  <Td>
                    {failed ? (
                      <span
                        className="rounded px-1.5 py-0.5 text-[11px] font-semibold"
                        style={{
                          color: "var(--status-critical)",
                          background: "color-mix(in srgb, var(--status-critical) 14%, transparent)",
                        }}
                        title={trace.exception_type}
                      >
                        {trace.exception_type
                          ? truncate(trace.exception_type.split(".").pop() ?? "error", 24)
                          : `${trace.error_count} error${trace.error_count > 1 ? "s" : ""}`}
                      </span>
                    ) : (
                      <span className="text-[12px]" style={{ color: "var(--text-muted)" }}>
                        {trace.http_status || "ok"}
                      </span>
                    )}
                  </Td>
                </tr>
              );
            })}
          </Table>
        )}
      </Card>
    </div>
  );
}
