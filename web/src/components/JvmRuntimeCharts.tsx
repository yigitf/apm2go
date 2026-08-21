import { RuntimeCharts, type RuntimeChartSpec } from "./RuntimeCharts";

/**
 * The JVM instruments worth a chart, in the order they are read.
 *
 * The OpenTelemetry Java agent reports rather more than this; these are the
 * ones that explain a latency problem. The rest stay one query away rather
 * than competing for attention here.
 */
const JVM_CHARTS: readonly RuntimeChartSpec[] = [
  { name: "jvm.memory.used", title: "Heap", subtitle: "memory used, by pool" },
  { name: "jvm.gc.duration", title: "GC", subtitle: "collection time" },
  { name: "jvm.thread.count", title: "Threads", subtitle: "live threads" },
  { name: "jvm.cpu.recent_utilization", title: "JVM CPU", subtitle: "process utilisation" },
  { name: "jvm.class.count", title: "Classes", subtitle: "loaded" },
];

/**
 * The JVM's own state, on the same time axis as everything around it.
 *
 * These are what separate "this code got slower" from "this JVM was busy
 * collecting garbage" — the two look identical in a latency chart, and the
 * only way to tell them apart is to put them side by side.
 *
 * Renders nothing when the service reports no JVM metrics — a non-Java
 * service, or a Java one attached before metrics were enabled — rather than
 * showing empty frames.
 */
export function JvmRuntimeCharts({ service, columns = 3 }: { service: string; columns?: number }) {
  return <RuntimeCharts service={service} charts={JVM_CHARTS} columns={columns} />;
}
