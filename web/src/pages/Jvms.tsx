import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { JVMEntry } from "../api/types";
import { LocationBadge, PlacementBadge } from "../components/PlacementBadge";
import { Card, EmptyState, ErrorState, Loading, StateBadge } from "../components/primitives";
import { formatRelative, isUnset, truncate } from "../format";

/**
 * The JVM inventory: what apm2go found on this host and what it did about it.
 *
 * This is the page an operator opens when a service is missing from the trace
 * views, so every failure reason and warning is shown in full rather than
 * summarised.
 */
export function Jvms() {
  const queryClient = useQueryClient();
  const [expanded, setExpanded] = useState<number | null>(null);

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["jvms"],
    queryFn: api.jvms,
    // Discovery runs every few seconds; matching it keeps the page live without
    // the complexity of wiring the event stream into every row.
    refetchInterval: 5_000,
  });

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["jvms"] });

  const attach = useMutation({ mutationFn: api.attachJVM, onSettled: invalidate });
  const disable = useMutation({ mutationFn: api.disableJVM, onSettled: invalidate });
  const enable = useMutation({ mutationFn: api.enableJVM, onSettled: invalidate });

  if (isError) return <ErrorState error={error} />;
  if (isLoading) return <Loading rows={4} />;

  const entries = data ?? [];
  if (entries.length === 0) {
    return (
      <Card>
        <EmptyState
          title="No JVMs found on this host"
          hint="apm2go scans /proc for Java processes. If one is running, check that apm2go has permission to read /proc and that discovery filters are not excluding it."
        />
      </Card>
    );
  }

  return (
    <div className="space-y-3">
      {entries.map((entry) => (
        <JVMRow
          key={`${entry.jvm.pid}-${entry.first_seen}`}
          entry={entry}
          expanded={expanded === entry.jvm.pid}
          onToggle={() => setExpanded(expanded === entry.jvm.pid ? null : entry.jvm.pid)}
          onAttach={() => attach.mutate(entry.jvm.pid)}
          onDisable={() => disable.mutate(entry.jvm.pid)}
          onEnable={() => enable.mutate(entry.jvm.pid)}
          busy={attach.isPending && attach.variables === entry.jvm.pid}
        />
      ))}
    </div>
  );
}

function JVMRow({
  entry,
  expanded,
  onToggle,
  onAttach,
  onDisable,
  onEnable,
  busy,
}: {
  entry: JVMEntry;
  expanded: boolean;
  onToggle: () => void;
  onAttach: () => void;
  onDisable: () => void;
  onEnable: () => void;
  busy: boolean;
}) {
  const { jvm } = entry;
  const canAttach = entry.state !== "attached" && entry.state !== "exited" && entry.state !== "attaching";

  return (
    <Card>
      <div className="flex flex-wrap items-start gap-4 px-4 py-3">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <Link to={`/jvms/${jvm.pid}`} className="text-[14px] font-semibold hover:underline">
              {jvm.service_name}
            </Link>
            <StateBadge state={entry.state} title={entry.reason} />
            <PlacementBadge jvm={jvm} />
            <LocationBadge jvm={jvm} />
          </div>

          <div
            className="mt-1 flex flex-wrap items-center gap-x-4 gap-y-1 text-[12px]"
            style={{ color: "var(--text-muted)" }}
          >
            <span className="tabular">pid {jvm.pid}</span>
            <span>Java {jvm.java_version || "unknown"}</span>
            <span>{jvm.user || `uid ${jvm.uid}`}</span>
            <span>started {formatRelative(jvm.start_time)}</span>
            {!isUnset(entry.attached_at) && (
              <span>attached {formatRelative(entry.attached_at)}</span>
            )}
            {jvm.container?.image && (
              <span title={jvm.container.image}>{truncate(jvm.container.image, 40)}</span>
            )}
            {!jvm.shares_our_network && jvm.gateway && (
              <span title="This process is on its own network and exports to apm2go at this address">
                exports via {jvm.gateway}
              </span>
            )}
          </div>

          {entry.reason && entry.state === "failed" && (
            <p className="mt-2 text-[12px]" style={{ color: "var(--status-critical)" }}>
              {entry.reason}
              {!isUnset(entry.next_attempt) && (
                <span style={{ color: "var(--text-muted)" }}> · retrying {formatRelative(entry.next_attempt)}</span>
              )}
            </p>
          )}

          {entry.reason && entry.state === "skipped" && (
            <p className="mt-2 text-[12px]" style={{ color: "var(--text-secondary)" }}>
              {entry.reason}
            </p>
          )}

          {entry.warnings?.map((warning, i) => (
            <p key={i} className="mt-2 text-[12px]" style={{ color: "var(--status-serious)" }}>
              ⚠ {warning}
            </p>
          ))}
        </div>

        <div className="flex shrink-0 items-center gap-2">
          {/* Named rather than implied: the detail page is where thread dumps
              and heap histograms live, and nothing else on this row hints that
              it exists. */}
          <Link
            to={`/jvms/${jvm.pid}`}
            className="rounded-md px-3 py-1.5 text-[13px]"
            style={{ border: "1px solid var(--border-strong)", color: "var(--text-secondary)" }}
          >
            Diagnostics
          </Link>
          {canAttach && (
            <button
              type="button"
              onClick={onAttach}
              disabled={busy}
              className="rounded-md px-3 py-1.5 text-[13px] font-medium"
              style={{ background: "var(--series-1)", color: "#ffffff", opacity: busy ? 0.6 : 1 }}
            >
              {busy ? "Attaching…" : "Attach"}
            </button>
          )}
          {entry.manual_only ? (
            <button
              type="button"
              onClick={onEnable}
              className="rounded-md px-3 py-1.5 text-[13px]"
              style={{ border: "1px solid var(--border-strong)", color: "var(--text-secondary)" }}
            >
              Enable auto-attach
            </button>
          ) : (
            entry.state !== "exited" && (
              <button
                type="button"
                onClick={onDisable}
                className="rounded-md px-3 py-1.5 text-[13px]"
                style={{ border: "1px solid var(--border-strong)", color: "var(--text-secondary)" }}
              >
                Disable
              </button>
            )
          )}
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={expanded}
            className="rounded-md px-2 py-1.5 text-[13px]"
            style={{ border: "1px solid var(--border-strong)", color: "var(--text-secondary)" }}
          >
            {expanded ? "Less" : "More"}
          </button>
        </div>
      </div>

      {expanded && <JVMDetail entry={entry} />}
    </Card>
  );
}

/** The full process description apm2go discovered. */
function JVMDetail({ entry }: { entry: JVMEntry }) {
  const { jvm } = entry;

  return (
    <div className="space-y-4 px-4 pt-1 pb-4" style={{ borderTop: "1px solid var(--border)" }}>
      <dl className="grid gap-x-6 gap-y-1 text-[12px] sm:grid-cols-2">
        <Detail label="Command" value={jvm.cmdline.join(" ")} mono />
        <Detail label="Executable" value={jvm.exe_path} mono />
        <Detail label="Java home" value={jvm.java_home} mono />
        <Detail label="Name source" value={jvm.service_name_source} />
        {jvm.main_class && <Detail label="Main class" value={jvm.main_class} mono />}
        {jvm.jar_path && <Detail label="Jar" value={jvm.jar_path} mono />}
        {jvm.vm_name && <Detail label="VM" value={jvm.vm_name} />}
        {jvm.systemd_unit && <Detail label="Systemd unit" value={jvm.systemd_unit} />}
        {jvm.container?.name && <Detail label="Container" value={jvm.container.name} />}
        {jvm.container?.image && <Detail label="Image" value={jvm.container.image} mono />}
        {jvm.container?.compose_project && (
          <Detail label="Compose project" value={jvm.container.compose_project} />
        )}
        {jvm.container?.pod_name && <Detail label="Pod" value={jvm.container.pod_name} />}
        {jvm.container?.pod_namespace && (
          <Detail label="Namespace" value={jvm.container.pod_namespace} />
        )}
        {!jvm.container?.name && jvm.container_id && (
          <Detail label="Container ID" value={truncate(jvm.container_id, 20)} mono />
        )}
        {entry.endpoint && <Detail label="Exporting to" value={entry.endpoint} mono />}
        <Detail
          label="Network"
          value={
            jvm.shares_our_network
              ? "shares apm2go's namespace (loopback)"
              : jvm.gateway
                ? `own namespace, reaches apm2go at ${jvm.gateway}`
                : "own namespace, no route to apm2go found"
          }
        />
      </dl>
    </div>
  );
}

function Detail({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex gap-2">
      <dt className="w-28 shrink-0" style={{ color: "var(--text-muted)" }}>
        {label}
      </dt>
      <dd className={`min-w-0 break-all ${mono ? "tabular" : ""}`}>{value}</dd>
    </div>
  );
}
