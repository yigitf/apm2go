import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { api } from "../api/client";
import type {
  ClassHistogram,
  Deadlock,
  DiagnosticEntry,
  DiagnosticKind,
  DumpThread,
  HeapInfo,
  JVMEntry,
  ThreadDump,
} from "../api/types";
import { JvmRuntimeCharts } from "../components/JvmRuntimeCharts";
import { PlacementBadge } from "../components/PlacementBadge";
import {
  Card,
  EmptyState,
  ErrorState,
  Loading,
  StateBadge,
  Table,
  Td,
  Th,
} from "../components/primitives";
import { formatBytes, formatDateTime, formatRelative, truncate } from "../format";

/** How each diagnostic is labelled, and what it costs the target. */
const DIAGNOSTICS: {
  kind: DiagnosticKind;
  label: string;
  cost: string;
}[] = [
  {
    kind: "thread_dump",
    label: "Thread dump",
    cost: "Pauses the JVM briefly to walk every thread stack.",
  },
  {
    kind: "class_histogram",
    label: "Heap histogram",
    cost: "Pauses the JVM to count live objects; longer on a large heap.",
  },
  { kind: "heap_info", label: "Heap summary", cost: "Cheap; reads the collector's own counters." },
  { kind: "vm_flags", label: "JVM flags", cost: "Cheap; reads the flags the JVM started with." },
];

/**
 * One JVM in depth: what it is doing right now, and what it was doing at the
 * moments someone asked.
 *
 * The charts above come from the metric stream and are always there; the dumps
 * below only exist because an operator ran them. That split is the page: a
 * continuous view of the runtime, and a deliberate, costly look inside it.
 */
export function JvmDetail() {
  const { pid: pidParam = "" } = useParams();
  const pid = Number(pidParam);

  const jvms = useQuery({
    queryKey: ["jvms"],
    queryFn: api.jvms,
    refetchInterval: 10_000,
  });

  if (jvms.isError) return <ErrorState error={jvms.error} />;
  if (jvms.isLoading) return <Loading rows={4} />;

  const entry = (jvms.data ?? []).find((e) => e.jvm.pid === pid);
  if (!entry) {
    return (
      <Card>
        <EmptyState
          title={`No JVM with pid ${pidParam} is being tracked`}
          hint="The process may have exited, or apm2go may never have seen it. The JVM list shows what it currently knows about."
        />
      </Card>
    );
  }

  return (
    <div className="space-y-3">
      <Header entry={entry} />
      <JvmRuntimeCharts service={entry.jvm.service_name} columns={3} />
      <Diagnostics pid={pid} live={entry.state !== "exited"} />
    </div>
  );
}

function Header({ entry }: { entry: JVMEntry }) {
  const { jvm } = entry;
  return (
    <Card>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3">
        <Link to="/jvms" className="text-[13px]" style={{ color: "var(--text-muted)" }}>
          ← JVMs
        </Link>
        <h2 className="text-[15px] font-semibold">{jvm.service_name}</h2>
        <StateBadge state={entry.state} title={entry.reason} />
        <PlacementBadge jvm={jvm} />
        <div
          className="flex flex-wrap items-center gap-x-4 text-[12px]"
          style={{ color: "var(--text-muted)" }}
        >
          <span className="tabular">pid {jvm.pid}</span>
          <span>Java {jvm.java_version || "unknown"}</span>
          <span>{jvm.user || `uid ${jvm.uid}`}</span>
          <span>started {formatRelative(jvm.start_time)}</span>
        </div>
        <Link
          to={`/services/${encodeURIComponent(jvm.service_name)}`}
          className="ml-auto text-[13px]"
          style={{ color: "var(--series-1)" }}
        >
          Traces for this service →
        </Link>
      </div>
    </Card>
  );
}

/** The diagnostics panel: run one, read the last, compare two. */
function Diagnostics({ pid, live }: { pid: number; live: boolean }) {
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<string | null>(null);

  const history = useQuery({
    queryKey: ["diagnostics", pid],
    queryFn: () => api.diagnostics(pid),
  });

  const collect = useMutation({
    mutationFn: (kind: DiagnosticKind) => api.collectDiagnostic(pid, kind),
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: ["diagnostics", pid] });
      if (result.id) setSelected(result.id);
    },
  });

  const entries = history.data?.diagnostics ?? [];
  const shown = selected ?? entries[0]?.id ?? null;

  return (
    <div className="space-y-3">
      <Card title="Diagnostics" subtitle="collected on request, never on a timer">
        <div className="space-y-3 px-4 py-3">
          <div className="flex flex-wrap gap-2">
            {DIAGNOSTICS.map((d) => (
              <button
                key={d.kind}
                type="button"
                title={d.cost}
                disabled={collect.isPending || !live}
                onClick={() => collect.mutate(d.kind)}
                className="rounded-md px-3 py-1.5 text-[13px] font-medium"
                style={{
                  border: "1px solid var(--border-strong)",
                  color: "var(--text-secondary)",
                  opacity: collect.isPending || !live ? 0.5 : 1,
                }}
              >
                {collect.isPending && collect.variables === d.kind ? "Running…" : d.label}
              </button>
            ))}
          </div>

          {!live && (
            <p className="text-[12px]" style={{ color: "var(--text-muted)" }}>
              This process has exited, so there is nothing left to diagnose. The dumps already
              collected are still below.
            </p>
          )}

          <p className="text-[12px]" style={{ color: "var(--text-muted)" }}>
            Thread dumps and heap histograms stop the JVM at a safepoint while they run. That is
            why nothing here happens automatically.
          </p>

          {collect.isError && <ErrorState error={collect.error} />}

          {history.data?.heap_dump && <HeapDumpNote heapDump={history.data.heap_dump} pid={pid} />}
        </div>
      </Card>

      {history.isLoading ? (
        <Loading rows={2} />
      ) : entries.length === 0 ? (
        <Card>
          <EmptyState
            title="No diagnostics collected yet"
            hint="Run one above. Each is stored, so a later dump can be compared against it."
          />
        </Card>
      ) : (
        <>
          <History entries={entries} selected={shown} onSelect={setSelected} />
          <Comparison entries={entries} />
          {shown && <StoredDump id={shown} />}
        </>
      )}
    </div>
  );
}

/**
 * The command apm2go refuses to run.
 *
 * Showing it rather than hiding it is the honest position: a heap dump is
 * sometimes exactly what is needed, and an operator who wants one should not
 * have to reconstruct the command from memory. What apm2go will not do is issue
 * it against a live service on a button press.
 */
function HeapDumpNote({
  heapDump,
  pid,
}: {
  heapDump: { command: string; note: string };
  pid: number;
}) {
  return (
    <details>
      <summary className="cursor-pointer text-[12px]" style={{ color: "var(--text-muted)" }}>
        Why is there no heap dump button?
      </summary>
      <div className="mt-2 space-y-1.5">
        <p className="text-[12px]" style={{ color: "var(--text-secondary)" }}>
          {heapDump.note}
        </p>
        <pre
          className="overflow-x-auto rounded px-2 py-1.5 text-[11px]"
          style={{ background: "var(--hover-wash)" }}
        >
          jcmd {pid} {heapDump.command}
        </pre>
      </div>
    </details>
  );
}

function History({
  entries,
  selected,
  onSelect,
}: {
  entries: DiagnosticEntry[];
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  return (
    <Card title="History" subtitle="stored dumps, newest first">
      <Table
        head={
          <>
            <Th>Taken</Th>
            <Th>Kind</Th>
            <Th>Summary</Th>
            <Th align="right">Paused</Th>
            <Th align="right">Size</Th>
            <Th align="right">Raw</Th>
          </>
        }
      >
        {entries.map((entry) => (
          <tr
            key={entry.id}
            onClick={() => onSelect(entry.id)}
            className="cursor-pointer hover:bg-[var(--hover-wash)]"
            style={{ background: entry.id === selected ? "var(--hover-wash)" : undefined }}
          >
            <Td>
              <span title={formatDateTime(entry.ts)}>{formatRelative(entry.ts)}</span>
            </Td>
            <Td>{kindLabel(entry.kind)}</Td>
            <Td>
              <Headline entry={entry} />
            </Td>
            <Td align="right">{entry.duration_ms} ms</Td>
            <Td align="right">{formatBytes(entry.size_bytes)}</Td>
            <Td align="right">
              <a
                href={api.diagnosticRawUrl(entry.id)}
                onClick={(e) => e.stopPropagation()}
                className="text-[12px]"
                style={{ color: "var(--series-1)" }}
              >
                raw
              </a>
            </Td>
          </tr>
        ))}
      </Table>
    </Card>
  );
}

/** The counts stored alongside a dump, so the list costs nothing to render. */
function Headline({ entry }: { entry: DiagnosticEntry }) {
  const h = entry.headline ?? {};
  const deadlocks = Number(h.deadlocks ?? 0);

  if (entry.kind === "thread_dump") {
    return (
      <span>
        {String(h.threads ?? "?")} threads
        {deadlocks > 0 && (
          <strong style={{ color: "var(--status-critical)" }}>
            {" "}
            · {deadlocks} deadlock{deadlocks > 1 ? "s" : ""}
          </strong>
        )}
      </span>
    );
  }
  if (entry.kind === "class_histogram") {
    return (
      <span>
        {String(h.classes ?? "?")} classes · {formatBytes(Number(h.total_bytes ?? 0))}
      </span>
    );
  }
  if (entry.kind === "heap_info") {
    return (
      <span>
        {formatBytes(Number(h.used_bytes ?? 0))} of {formatBytes(Number(h.total_bytes ?? 0))}
      </span>
    );
  }
  return <span style={{ color: "var(--text-muted)" }}>{formatBytes(Number(h.bytes ?? 0))}</span>;
}

function kindLabel(kind: DiagnosticKind): string {
  return DIAGNOSTICS.find((d) => d.kind === kind)?.label ?? kind;
}

/** One stored dump, rendered according to what it is. */
function StoredDump({ id }: { id: string }) {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["diagnostic", id],
    queryFn: () => api.diagnostic(id),
  });

  if (isError) return <ErrorState error={error} />;
  if (isLoading || !data) return <Loading rows={3} />;

  const summary = data.summary ?? {};
  return (
    <div className="space-y-3">
      {summary.threads && <ThreadDumpView dump={summary.threads} />}
      {summary.histogram && <HistogramView histogram={summary.histogram} />}
      {summary.heap && <HeapView heap={summary.heap} />}
      {!summary.threads && !summary.histogram && !summary.heap && (
        <Card title={kindLabel(data.kind)} subtitle={formatDateTime(data.ts)}>
          <div className="px-4 py-3">
            <p className="text-[12px]" style={{ color: "var(--text-muted)" }}>
              This diagnostic has no parsed form.{" "}
              <a href={api.diagnosticRawUrl(id)} style={{ color: "var(--series-1)" }}>
                Read the raw output
              </a>
              .
            </p>
          </div>
        </Card>
      )}
    </div>
  );
}

const vmInternalHint =
  "Threads belonging to the JVM itself — garbage collection, JIT compilation, the VM thread. They are not Java threads and have no Thread.State.";

/** Renders a state the way it reads in prose rather than in the JVM's enum. */
function stateLabel(state: string): string {
  return state.toLowerCase().replace(/_/g, " ");
}

function ThreadDumpView({ dump }: { dump: ThreadDump }) {
  const [expanded, setExpanded] = useState<string | null>(null);

  const states = Object.entries(dump.state_counts ?? {}).sort((a, b) => b[1] - a[1]);
  const deadlocked = new Set((dump.deadlocks ?? []).flatMap((d) => d.threads));

  // Deadlocked threads first, then blocked ones: this list is read top-down
  // when something is stuck, and what is stuck belongs at the top.
  const threads = [...(dump.threads ?? [])].sort((a, b) => {
    const rank = (t: DumpThread) =>
      deadlocked.has(t.name)
        ? 0
        : t.state === "BLOCKED"
          ? 1
          : t.state === "RUNNABLE"
            ? 2
            : // The JVM's own housekeeping threads go last: they are always
              // there and are never the answer to why an application is stuck.
              t.vm_internal
              ? 4
              : 3;
    return rank(a) - rank(b) || a.name.localeCompare(b.name);
  });

  return (
    <div className="space-y-3">
      {(dump.deadlocks ?? []).map((deadlock, i) => (
        <DeadlockBanner key={i} deadlock={deadlock} />
      ))}

      <Card title="Threads" subtitle={dump.header}>
        <div className="flex flex-wrap gap-x-4 gap-y-1 px-4 py-2 text-[12px]">
          {states.map(([state, count]) => (
            <span key={state} title={state === "VM_INTERNAL" ? vmInternalHint : undefined}>
              <span style={{ color: "var(--text-muted)" }}>{stateLabel(state)} </span>
              <span className="tabular font-medium">{count}</span>
            </span>
          ))}
        </div>

        {(dump.pileups?.length ?? 0) > 0 && (
          <div className="px-4 pb-2">
            <div
              className="mb-1 text-[10px] font-medium tracking-wide uppercase"
              style={{ color: "var(--text-muted)" }}
            >
              Where the threads are
            </div>
            {dump.pileups?.map((p) => (
              <div key={p.frame} className="flex gap-2 text-[12px]">
                <span className="tabular w-8 shrink-0 text-right font-medium">{p.count}</span>
                <span className="tabular min-w-0 break-all" style={{ color: "var(--text-secondary)" }}>
                  {p.frame}
                </span>
              </div>
            ))}
          </div>
        )}

        <Table
          head={
            <>
              <Th>Thread</Th>
              <Th>State</Th>
              <Th>Top frame</Th>
              <Th align="right">CPU</Th>
            </>
          }
        >
          {threads.map((thread) => (
            <ThreadRow
              key={`${thread.name}-${thread.tid ?? thread.number}`}
              thread={thread}
              deadlocked={deadlocked.has(thread.name)}
              expanded={expanded === thread.name}
              onToggle={() => setExpanded(expanded === thread.name ? null : thread.name)}
            />
          ))}
        </Table>
      </Card>
    </div>
  );
}

/**
 * A deadlock is the one finding on this page that cannot be a false alarm worth
 * ignoring: those threads are never going to move again.
 */
function DeadlockBanner({ deadlock }: { deadlock: Deadlock }) {
  return (
    <div
      className="rounded-lg px-4 py-3"
      style={{
        border: "1px solid var(--status-critical)",
        background: "color-mix(in srgb, var(--status-critical) 8%, transparent)",
      }}
    >
      <div className="text-[13px] font-semibold" style={{ color: "var(--status-critical)" }}>
        Deadlock — {deadlock.threads.length} threads are permanently stuck
      </div>
      <p className="mt-1 text-[12px]" style={{ color: "var(--text-secondary)" }}>
        {deadlock.threads.join(" → ")} → {deadlock.threads[0]}
      </p>
      {deadlock.detail && (
        <p className="mt-1 text-[12px]" style={{ color: "var(--text-muted)" }}>
          {deadlock.detail}
        </p>
      )}
      <p className="mt-1 text-[11px]" style={{ color: "var(--text-muted)" }}>
        {deadlock.source === "jvm"
          ? "Reported by the JVM's own deadlock detector."
          : "Found by apm2go from the lock annotations in this dump."}
      </p>
    </div>
  );
}

function ThreadRow({
  thread,
  deadlocked,
  expanded,
  onToggle,
}: {
  thread: DumpThread;
  deadlocked: boolean;
  expanded: boolean;
  onToggle: () => void;
}) {
  const stateColor =
    deadlocked || thread.state === "BLOCKED"
      ? "var(--status-critical)"
      : thread.state === "RUNNABLE"
        ? "var(--status-good)"
        : "var(--text-muted)";

  return (
    <>
      <tr onClick={onToggle} className="cursor-pointer hover:bg-[var(--hover-wash)]">
        <Td>
          <span className="tabular">{truncate(thread.name, 44)}</span>
          {thread.daemon && (
            <span className="ml-1 text-[10px]" style={{ color: "var(--text-muted)" }}>
              daemon
            </span>
          )}
        </Td>
        <Td>
          <span style={{ color: stateColor }} title={thread.vm_internal ? vmInternalHint : undefined}>
            {stateLabel(thread.state)}
          </span>
          {thread.detail && (
            <span className="text-[11px]" style={{ color: "var(--text-muted)" }}>
              {" "}
              ({thread.detail})
            </span>
          )}
        </Td>
        <Td>
          <span className="tabular" style={{ color: "var(--text-secondary)" }}>
            {truncate(thread.frames?.[0] ?? "—", 60)}
          </span>
        </Td>
        <Td align="right">{thread.cpu_ms !== undefined ? `${Math.round(thread.cpu_ms)} ms` : "—"}</Td>
      </tr>
      {expanded && (
        <tr>
          <td colSpan={4} className="px-4 pb-3">
            {thread.waiting_on && (
              <p className="mb-1 text-[12px]" style={{ color: "var(--status-critical)" }}>
                waiting to acquire {thread.waiting_on}
                {thread.waiting_on_class && ` (a ${thread.waiting_on_class})`}
              </p>
            )}
            {(thread.holds?.length ?? 0) > 0 && (
              <p className="mb-1 text-[12px]" style={{ color: "var(--text-muted)" }}>
                holds {thread.holds?.join(", ")}
              </p>
            )}
            <pre
              className="overflow-x-auto rounded px-2 py-1.5 text-[11px]"
              style={{ background: "var(--hover-wash)" }}
            >
              {(thread.frames ?? []).map((f) => `at ${f}`).join("\n") || "no stack recorded"}
            </pre>
          </td>
        </tr>
      )}
    </>
  );
}

function HistogramView({ histogram }: { histogram: ClassHistogram }) {
  const heaviest = histogram.classes?.[0]?.bytes ?? 1;

  return (
    <Card
      title="Heap histogram"
      subtitle={`${formatBytes(histogram.total_bytes)} live across ${histogram.class_count.toLocaleString()} classes`}
    >
      <Table
        head={
          <>
            <Th>Class</Th>
            <Th align="right">Instances</Th>
            <Th align="right">Size</Th>
          </>
        }
      >
        {(histogram.classes ?? []).slice(0, 40).map((c) => (
          <tr key={c.name}>
            <Td>
              <div className="flex items-center gap-2">
                <span
                  className="h-1.5 shrink-0 rounded-full"
                  style={{
                    width: `${Math.max(2, (c.bytes / heaviest) * 60)}px`,
                    background: "var(--series-1)",
                  }}
                />
                <span className="tabular min-w-0 break-all">{c.name}</span>
              </div>
            </Td>
            <Td align="right">{c.instances.toLocaleString()}</Td>
            <Td align="right">{formatBytes(c.bytes)}</Td>
          </tr>
        ))}
      </Table>
      {histogram.class_count > (histogram.classes?.length ?? 0) && (
        <p className="px-4 pb-3 text-[11px]" style={{ color: "var(--text-muted)" }}>
          Showing the heaviest classes. The tail is thousands of classes holding a few hundred
          bytes each, and is not stored.
        </p>
      )}
    </Card>
  );
}

function HeapView({ heap }: { heap: HeapInfo }) {
  return (
    <Card title="Heap" subtitle={heap.collector}>
      <div className="grid gap-x-6 gap-y-1 px-4 py-3 text-[12px] sm:grid-cols-2">
        <Field label="Used" value={formatBytes(heap.used_bytes ?? 0)} />
        <Field label="Total" value={formatBytes(heap.total_bytes ?? 0)} />
        {heap.metaspace && (
          <Field
            label="Metaspace"
            value={`${formatBytes(heap.metaspace.used_bytes)} used, ${formatBytes(heap.metaspace.committed_bytes ?? 0)} committed`}
          />
        )}
        {heap.class_space && (
          <Field label="Class space" value={formatBytes(heap.class_space.used_bytes)} />
        )}
        {(heap.regions ?? []).map((region) => (
          <Field
            key={region.name}
            label={region.name}
            value={`${formatBytes(region.total_bytes ?? 0)}, ${region.used_percent ?? 0}% used`}
          />
        ))}
      </div>
    </Card>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <dt className="w-28 shrink-0" style={{ color: "var(--text-muted)" }}>
        {label}
      </dt>
      <dd className="tabular min-w-0 break-all">{value}</dd>
    </div>
  );
}

/**
 * Comparing two histograms is how a leak is found: one dump shows what is big,
 * two show what is growing.
 */
function Comparison({ entries }: { entries: DiagnosticEntry[] }) {
  const histograms = entries.filter((e) => e.kind === "class_histogram");
  const [from, setFrom] = useState<string>("");
  const [to, setTo] = useState<string>("");
  const autoSelected = useRef(false);

  // Defaults to the two most recent histograms, so a comparison is on screen
  // without two manual picks — the button that produced the second dump already
  // told the operator which pair they meant to compare. Only fires once: after
  // that, a newer dump arriving must not silently swap out a comparison someone
  // is mid-read on.
  useEffect(() => {
    if (autoSelected.current || histograms.length < 2) return;
    autoSelected.current = true;
    setTo(histograms[0].id);
    setFrom(histograms[1].id);
  }, [histograms]);

  const comparison = useQuery({
    queryKey: ["diagnostic-compare", from, to],
    queryFn: () => api.compareDiagnostics(from, to),
    enabled: Boolean(from && to && from !== to),
  });

  if (histograms.length < 2) return null;

  return (
    <Card title="Compare heap histograms" subtitle="what grew between two dumps">
      <div className="space-y-3 px-4 py-3">
        <div className="flex flex-wrap items-center gap-2 text-[12px]">
          <DumpPicker label="From" value={from} options={histograms} onChange={setFrom} />
          <DumpPicker label="To" value={to} options={histograms} onChange={setTo} />
        </div>

        {comparison.isError && <ErrorState error={comparison.error} />}

        {comparison.data?.diff && (
          <div>
            <p className="mb-2 text-[12px]" style={{ color: "var(--text-muted)" }}>
              Over {comparison.data.elapsed}, the live set changed by{" "}
              <strong
                style={{
                  color:
                    comparison.data.diff.total_bytes_delta > 0
                      ? "var(--status-serious)"
                      : "var(--status-good)",
                }}
              >
                {comparison.data.diff.total_bytes_delta > 0 ? "+" : ""}
                {formatBytes(Math.abs(comparison.data.diff.total_bytes_delta))}
              </strong>
              .
            </p>
            <Table
              head={
                <>
                  <Th>Class</Th>
                  <Th align="right">Instances</Th>
                  <Th align="right">Change</Th>
                </>
              }
            >
              {comparison.data.diff.growth.slice(0, 20).map((d) => (
                <tr key={d.name}>
                  <Td>
                    <span className="tabular break-all">{d.name}</span>
                  </Td>
                  <Td align="right">
                    {d.instances_delta > 0 ? "+" : ""}
                    {d.instances_delta.toLocaleString()}
                  </Td>
                  <Td align="right">
                    <span style={{ color: "var(--status-serious)" }}>
                      +{formatBytes(d.bytes_delta)}
                    </span>
                  </Td>
                </tr>
              ))}
            </Table>
          </div>
        )}
      </div>
    </Card>
  );
}

function DumpPicker({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: DiagnosticEntry[];
  onChange: (id: string) => void;
}) {
  return (
    <label className="flex items-center gap-1.5">
      <span style={{ color: "var(--text-muted)" }}>{label}</span>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="rounded-md px-2 py-1 text-[12px]"
        style={{ border: "1px solid var(--border-strong)", background: "transparent" }}
      >
        <option value="">choose a dump</option>
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {formatDateTime(o.ts)}
          </option>
        ))}
      </select>
    </label>
  );
}
