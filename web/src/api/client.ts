import type {
  Dependency,
  DiagnosticEntry,
  DiagnosticKind,
  DiagnosticList,
  DiagnosticResult,
  HistogramComparison,
  MetricName,
  MetricSeries,
  JVMEntry,
  OperationStats,
  RangeEnvelope,
  SelfStats,
  ServiceStats,
  TimeSeriesPoint,
  Trace,
  TraceSummary,
  WatchedProcess,
} from "./types";

/**
 * The UI is served by apm2go itself, so requests are same-origin and need no
 * base URL. In development Vite proxies /api to a locally running apm2go.
 */
const BASE = "/api/v1";

/** Error carrying the API's own message, which is written for operators. */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${BASE}${path}`, {
    headers: { Accept: "application/json" },
    ...init,
  });

  if (!response.ok) {
    // The API reports failures as {"error": "..."}; fall back to the status
    // text when the body is not JSON, such as a proxy error page.
    let message = response.statusText;
    try {
      const body = await response.json();
      if (body?.error) message = body.error;
    } catch {
      // Keep the status text.
    }
    throw new ApiError(message, response.status);
  }
  return response.json() as Promise<T>;
}

/** A time range expressed the way the API accepts it. */
export interface Range {
  from: string;
  to: string;
}

function rangeParams(range: Range, extra?: Record<string, string | number | undefined>): string {
  const params = new URLSearchParams({ from: range.from, to: range.to });
  for (const [key, value] of Object.entries(extra ?? {})) {
    if (value !== undefined && value !== "" && value !== 0) {
      params.set(key, String(value));
    }
  }
  return `?${params.toString()}`;
}

export const api = {
  self: () => request<SelfStats>("/self"),

  jvms: () => request<JVMEntry[]>("/jvms"),

  /** Non-Java processes apm2go is watching, whether or not they have reported. */
  processes: () => request<WatchedProcess[]>("/processes"),

  attachJVM: (pid: number) => request<JVMEntry>(`/jvms/${pid}/attach`, { method: "POST" }),

  disableJVM: (pid: number) => request<JVMEntry>(`/jvms/${pid}/disable`, { method: "POST" }),

  enableJVM: (pid: number) => request<JVMEntry>(`/jvms/${pid}/enable`, { method: "POST" }),

  services: (range: Range) =>
    request<RangeEnvelope & { services: ServiceStats[] }>(`/services${rangeParams(range)}`),

  operations: (service: string, range: Range) =>
    request<RangeEnvelope & { operations: OperationStats[] }>(
      `/services/${encodeURIComponent(service)}/operations${rangeParams(range)}`,
    ),

  timeSeries: (range: Range, service?: string) =>
    request<RangeEnvelope & { points: TimeSeriesPoint[] }>(
      service
        ? `/services/${encodeURIComponent(service)}/timeseries${rangeParams(range)}`
        : `/timeseries${rangeParams(range)}`,
    ),

  traces: (
    range: Range,
    filters: {
      service?: string;
      operation?: string;
      search?: string;
      status?: string;
      min_duration?: string;
      limit?: number;
    } = {},
  ) => request<RangeEnvelope & { traces: TraceSummary[] }>(`/traces${rangeParams(range, filters)}`),

  trace: (traceId: string) => request<Trace>(`/traces/${traceId}`),

  metricNames: (range: Range, service?: string) =>
    request<RangeEnvelope & { service: string; metrics: MetricName[] }>(
      `/metrics${rangeParams(range, { service })}`,
    ),

  metric: (range: Range, name: string, service?: string) =>
    request<RangeEnvelope & { service: string; name: string; series: MetricSeries[] }>(
      `/metrics/query${rangeParams(range, { name, service })}`,
    ),

  dependencies: (range: Range) =>
    request<RangeEnvelope & { dependencies: Dependency[] }>(`/dependencies${rangeParams(range)}`),

  /** Stored dumps for one JVM, newest first. */
  diagnostics: (pid: number) => request<DiagnosticList>(`/jvms/${pid}/diagnostics`),

  /**
   * Runs a diagnostic against a live JVM.
   *
   * This pauses the target at a safepoint for as long as the command takes, so
   * it only ever happens because someone asked for it.
   */
  collectDiagnostic: (pid: number, kind: DiagnosticKind) =>
    request<DiagnosticResult>(`/jvms/${pid}/diagnostics/${kind}`, { method: "POST" }),

  diagnostic: (id: string) => request<DiagnosticEntry>(`/diagnostics/${id}`),

  /** Compares two class histograms of the same process, oldest first. */
  compareDiagnostics: (from: string, to: string) =>
    request<HistogramComparison>(
      `/diagnostics/compare?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`,
    ),

  /** The URL that serves a dump's verbatim text; used as a link, not fetched. */
  diagnosticRawUrl: (id: string) => `${BASE}/diagnostics/${id}/raw`,
};
