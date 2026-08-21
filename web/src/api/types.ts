/** Types mirroring apm2go's JSON API. */

export type JVMState =
  | "discovered"
  | "pending"
  | "attaching"
  | "attached"
  | "failed"
  | "skipped"
  | "exited";

export interface JVM {
  pid: number;
  ns_pid: number;
  uid: number;
  user: string;
  service_name: string;
  service_name_source: string;
  cmdline: string[];
  exe_path: string;
  java_home: string;
  main_class?: string;
  jar_path?: string;
  java_version: string;
  java_major: number;
  vm_name?: string;
  system_props?: Record<string, string>;
  start_time: string;
  in_container: boolean;
  /** True when the process reaches apm2go over loopback. */
  shares_our_network: boolean;
  /** The address that reaches apm2go from inside this process's network. */
  gateway?: string;
  container_id?: string;
  pod_uid?: string;
  container?: ContainerInfo;
  systemd_unit?: string;
  already_instrumented: boolean;
  instrumented_by_us: boolean;
}

/** What the container runtime knows about a containerized process. */
export interface ContainerInfo {
  id?: string;
  name?: string;
  image?: string;
  compose_project?: string;
  compose_service?: string;
  pod_name?: string;
  pod_namespace?: string;
  pod_uid?: string;
  app_label?: string;
  source?: string;
}

export interface JVMEntry {
  jvm: JVM;
  state: JVMState;
  reason?: string;
  warnings?: string[];
  first_seen: string;
  last_seen: string;
  attempts: number;
  next_attempt?: string;
  attached_at?: string;
  exited_at?: string;
  endpoint?: string;
  manual_only: boolean;
}

export interface ServiceStats {
  service: string;
  span_count: number;
  error_count: number;
  error_rate: number;
  requests_per_second: number;
  avg_latency_ns: number;
  p50_latency_ns: number;
  p95_latency_ns: number;
  p99_latency_ns: number;
  max_latency_ns: number;
  last_seen: string;
  /**
   * The language the service is written in, as its own telemetry reported it.
   * Absent when nothing that reached apm2go said, which is "not known" rather
   * than a language.
   */
  runtime?: string;
}

export interface OperationStats extends ServiceStats {
  operation: string;
  kind: string;
}

export interface TraceSummary {
  trace_id: string;
  root_service: string;
  root_operation: string;
  start_time: string;
  duration_ns: number;
  span_count: number;
  error_count: number;
  service_count: number;
  http_method?: string;
  http_route?: string;
  http_status?: number;
  exception_type?: string;
}

export interface SpanEvent {
  timestamp: string;
  name: string;
  attributes?: Record<string, string>;
}

export interface Span {
  timestamp: string;
  duration: number;
  trace_id: string;
  span_id: string;
  parent_span_id: string;
  service: string;
  operation: string;
  kind: number;
  status: number;
  status_message?: string;
  http_method?: string;
  http_route?: string;
  http_status?: number;
  db_system?: string;
  db_statement?: string;
  db_name?: string;
  peer_service?: string;
  host_name?: string;
  pid?: number;
  attributes?: Record<string, string>;
  events?: SpanEvent[];
}

export interface Trace {
  trace_id: string;
  spans: Span[];
  start_time: string;
  duration_ns: number;
  services: string[];
}

export interface Dependency {
  caller: string;
  callee: string;
  call_count: number;
  error_count: number;
  error_rate: number;
  avg_latency_ns: number;
}

export interface TimeSeriesPoint {
  timestamp: string;
  count: number;
  error_count: number;
  rate: number;
  error_rate: number;
  avg_latency_ns: number;
  p95_latency_ns: number;
}

export interface SelfStats {
  version: string;
  mode: string;
  started_at: string;
  uptime_sec: number;
  otel_agent: string;
  config_hint: {
    auto_attach: boolean;
    sample_ratio: number;
    span_retention: string;
    rollup_retention: string;
    max_spans_per_s: number;
    otlp_grpc: string;
    otlp_http: string;
  };
  receiver?: {
    requests_grpc: number;
    requests_http: number;
    spans_accepted: number;
    spans_malformed: number;
    spans_rejected: number;
    last_received_at: number;
    unauthenticated: number;
    listen_addresses?: string[];
    metrics_accepted?: number;
    metrics_malformed?: number;
  };
  container_sources?: string[];
  pipeline?: {
    queued: number;
    metrics_written?: number;
    metrics_dropped?: number;
    queue_capacity: number;
    queue_depth: number;
    written: number;
    dropped_queue_full: number;
    dropped_rate_limit: number;
    write_errors: number;
    batches: number;
    last_flush_ms: number;
  };
  cardinality?: { services: number; operations: number };
  storage?: {
    span_count: number;
    rollup_count: number;
    oldest_span?: string;
    newest_span?: string;
    services: number;
  };
}

/**
 * One process apm2go is watching through eBPF right now.
 *
 * Distinct from a ServiceStats: that is derived from stored spans and so only
 * exists once the service has served a request. This exists from the moment
 * apm2go decided to instrument the process, which is what lets the UI tell
 * "watched and quiet" apart from "never found".
 */
export interface WatchedProcess {
  service: string;
  runtime?: string;
  pid: number;
  ports?: number[];
  container_id?: string;
  first_seen: string;
  container?: ContainerInfo;
}

/** Envelope every list endpoint returns, carrying the range it covers. */
export interface RangeEnvelope {
  from: string;
  to: string;
}

/** One instrument that reported data, for the metric picker. */
export interface MetricName {
  name: string;
  kind: "gauge" | "sum" | "histogram";
  unit?: string;
  series_count: number;
}

/** One plotted point of a metric series. */
export interface MetricPoint {
  timestamp: string;
  value: number;
}

/**
 * One instrument's values over time, for one set of attributes. An instrument
 * with attributes yields several series — one per memory pool, per disk, per
 * direction — which is why labels are part of the identity.
 */
export interface MetricSeries {
  name: string;
  kind: "gauge" | "sum" | "histogram";
  unit?: string;
  labels?: Record<string, string>;
  /** null, not [], when nothing fell in range: a nil Go slice serialises that way. */
  points: MetricPoint[] | null;
}

// ------------------------------------------------------------- diagnostics

/** The diagnostics apm2go can collect from a running JVM. */
export type DiagnosticKind = "thread_dump" | "class_histogram" | "heap_info" | "vm_flags";

/** One thread's entry in a dump. */
export interface DumpThread {
  name: string;
  number?: number;
  daemon?: boolean;
  tid?: string;
  nid?: string;
  state: string;
  detail?: string;
  /** A thread belonging to the JVM itself — GC, JIT, the VM thread — rather
   *  than to the application. These are not Java threads and have no state. */
  vm_internal?: boolean;
  status?: string;
  cpu_ms?: number;
  elapsed_sec?: number;
  frames?: string[];
  waiting_on?: string;
  waiting_on_class?: string;
  holds?: string[];
}

/**
 * A set of threads that can never proceed. `source` is "jvm" when HotSpot
 * reported it and "inferred" when apm2go found the cycle in the dump itself.
 */
export interface Deadlock {
  threads: string[];
  source: "jvm" | "inferred";
  detail?: string;
}

/** A frame many threads are sitting on at once. */
export interface Pileup {
  frame: string;
  count: number;
  states?: string[];
}

export interface ThreadDump {
  header?: string;
  threads: DumpThread[];
  state_counts: Record<string, number>;
  deadlocks?: Deadlock[];
  pileups?: Pileup[];
}

export interface ClassCount {
  rank: number;
  name: string;
  instances: number;
  bytes: number;
}

export interface ClassHistogram {
  classes: ClassCount[];
  total_instances: number;
  total_bytes: number;
  /** How many classes the JVM reported, which exceeds classes.length when the
   *  stored histogram was trimmed to its heaviest entries. */
  class_count: number;
}

export interface MemoryPool {
  used_bytes: number;
  committed_bytes?: number;
  reserved_bytes?: number;
}

export interface HeapRegion {
  name: string;
  total_bytes?: number;
  used_percent?: number;
}

export interface HeapInfo {
  collector?: string;
  total_bytes?: number;
  used_bytes?: number;
  metaspace?: MemoryPool;
  class_space?: MemoryPool;
  regions?: HeapRegion[];
}

/** The parsed part of a dump; exactly one field is set, according to kind. */
export interface DiagnosticSummary {
  threads?: ThreadDump;
  histogram?: ClassHistogram;
  heap?: HeapInfo;
}

/** A stored dump as the history list reports it, without its body. */
export interface DiagnosticEntry {
  id: string;
  ts: string;
  kind: DiagnosticKind;
  pid: number;
  start_time?: string;
  service?: string;
  duration_ms: number;
  size_bytes: number;
  /** A few counts computed at collection time, so the list costs nothing. */
  headline?: Record<string, unknown>;
  summary?: DiagnosticSummary;
}

/** What a collection returns. */
export interface DiagnosticResult {
  id: string;
  kind: DiagnosticKind;
  pid: number;
  service?: string;
  captured_at: string;
  duration_ms: number;
  size_bytes: number;
  summary: DiagnosticSummary;
  stored: boolean;
  note?: string;
  /** VM.flags has no parser; its whole value is the text. */
  text?: string;
}

export interface DiagnosticList {
  diagnostics: DiagnosticEntry[];
  available: DiagnosticKind[];
  /** The one diagnostic apm2go will not run, and why. */
  heap_dump: { command: string; note: string };
}

export interface ClassDelta {
  name: string;
  instances: number;
  bytes: number;
  instances_delta: number;
  bytes_delta: number;
}

export interface HistogramComparison {
  from: string;
  to: string;
  pid: number;
  service?: string;
  elapsed: string;
  diff: {
    growth: ClassDelta[];
    shrink: ClassDelta[];
    total_bytes_delta: number;
  } | null;
  truncated: boolean;
}
