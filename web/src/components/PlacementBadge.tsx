import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { JVM } from "../api/types";
import containerIcon from "../assets/placement/container.png";
import hostIcon from "../assets/placement/host.png";
import { truncate } from "../format";

/**
 * Where a process runs: inside a container, or straight on the host.
 *
 * This is the first thing that has to be established about a misbehaving
 * process, and until now the page said it only in the text of a pill that also
 * carried the container's name — so scanning a list for "which of these are
 * containerized" meant reading every pill. An icon answers that at a glance,
 * and the pill beside it still says *which* container.
 *
 * The distinction is not cosmetic inside apm2go either: a containerized JVM is
 * on its own network, cannot reach apm2go over loopback, and has its agent jars
 * staged somewhere else entirely. Nearly every support question about a process
 * that "looks attached but reports nothing" turns on this one bit.
 */
export function PlacementBadge({ jvm, size = 16 }: { jvm: JVM; size?: number }) {
  const containerized = jvm.in_container;

  // Named as specifically as what apm2go actually resolved: the container
  // runtime is not always reachable, and "in a container" with no name is a
  // weaker but still true statement than pretending to know which one.
  const label = containerized
    ? jvm.container?.pod_name
      ? `Runs in pod ${jvm.container.pod_name}`
      : jvm.container?.name
        ? `Runs in container ${jvm.container.name}`
        : "Runs in a container"
    : jvm.systemd_unit
      ? `Runs on the host, under ${jvm.systemd_unit}`
      : "Runs on the host, outside any container";

  return (
    <img
      src={containerized ? containerIcon : hostIcon}
      alt={label}
      title={label}
      width={size}
      height={size}
      className="shrink-0 rounded-[4px]"
      style={{ width: size, height: size }}
    />
  );
}

/**
 * Says where a process actually lives, as visible text rather than a hover
 * tooltip.
 *
 * A containerized service that reports under a plain name is indistinguishable
 * from a host process until you know where it runs, and "which container is
 * this?" is the first question when one of them misbehaves — worth a pill an
 * operator can read at a glance, not just an icon they have to hover.
 */
export function LocationBadge({ jvm }: { jvm: JVM }) {
  const label = jvm.container?.pod_name
    ? `pod ${jvm.container.pod_name}`
    : jvm.container?.name
      ? `container ${jvm.container.name}`
      : jvm.in_container
        ? "container"
        : jvm.systemd_unit
          ? `unit ${jvm.systemd_unit}`
          : "host";

  const containerized = jvm.in_container;

  return (
    <span
      className="inline-flex items-center rounded-full px-2 py-0.5 text-[11px]"
      style={{
        color: containerized ? "var(--series-7)" : "var(--text-muted)",
        background: containerized
          ? "color-mix(in srgb, var(--series-7) 12%, transparent)"
          : "var(--hover-wash)",
      }}
      title={jvm.container?.image}
    >
      {truncate(label, 46)}
    </span>
  );
}

/**
 * The JVM behind each reporting service, keyed by service name.
 *
 * Placement — container or host, and which one — lives on the JVM record, not
 * on a ServiceStats: that is derived purely from stored spans and knows
 * nothing about where the process producing them runs. This is the join, so
 * any page that only has a service name can still show where it lives. Reads
 * from the same "jvms" query the JVMs page fills, so no extra request is
 * made just because a second page wants the same answer.
 */
export function useServicePlacements(): Map<string, JVM> {
  const { data } = useQuery({
    queryKey: ["jvms"],
    queryFn: api.jvms,
    refetchInterval: 10_000,
  });
  return new Map((data ?? []).map((entry) => [entry.jvm.service_name, entry.jvm]));
}
