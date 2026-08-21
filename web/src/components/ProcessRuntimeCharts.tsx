import { RuntimeCharts, type RuntimeChartSpec } from "./RuntimeCharts";

/**
 * Process-level instruments for a service apm2go watches via eBPF rather than
 * an in-process agent — Node.js, Python, Go, PHP.
 *
 * These come from apm2go reading /proc directly for the pid eBPF discovery
 * found, not from OBI: measured directly, none of OBI's own metric features
 * produced a runtime figure for a non-Java process, only HTTP request
 * counters. CPU and memory are what the kernel already accounts per process
 * regardless of language; GC and other runtime-internal detail would need
 * that language's own SDK loaded at start-up, which is a restart apm2go does
 * not ask for today.
 */
const PROCESS_CHARTS: readonly RuntimeChartSpec[] = [
  { name: "process.cpu.utilization", title: "CPU", subtitle: "process utilisation" },
  { name: "process.memory.usage", title: "Memory", subtitle: "resident set size" },
  { name: "process.disk.io", title: "Disk I/O", subtitle: "bytes read and written" },
];

/**
 * The process's own resource use, on the same time axis as everything around
 * it — the eBPF-observed counterpart to JvmRuntimeCharts.
 *
 * Renders nothing when the service reports no process metrics, which is what
 * a Java service always does: it has its own richer panel already, and
 * showing both would repeat CPU on two charts that measure it two different
 * ways.
 */
export function ProcessRuntimeCharts({ service, columns = 3 }: { service: string; columns?: number }) {
  return <RuntimeCharts service={service} charts={PROCESS_CHARTS} columns={columns} />;
}
