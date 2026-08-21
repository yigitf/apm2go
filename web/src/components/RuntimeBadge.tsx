import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { useTimeRange } from "./TimeRange";
import dotnetIcon from "../assets/runtimes/dotnet.png";
import goIcon from "../assets/runtimes/go.png";
import httpdIcon from "../assets/runtimes/httpd.png";
import javaIcon from "../assets/runtimes/java.png";
import nginxIcon from "../assets/runtimes/nginx.png";
import nodejsIcon from "../assets/runtimes/nodejs.png";
import phpIcon from "../assets/runtimes/php.png";
import pythonIcon from "../assets/runtimes/python.png";
import rubyIcon from "../assets/runtimes/ruby.png";
import rustIcon from "../assets/runtimes/rust.png";
import unknownIcon from "../assets/runtimes/unknown.png";

/** One badge: the image, and the name it stands for. */
interface Badge {
  src: string;
  label: string;
}

/**
 * The languages a badge exists for, keyed by the value apm2go stores.
 *
 * The keys are the normalised spellings the receiver produces, not the raw
 * attribute values: "node" and "nodejs" both arrive from real producers and are
 * folded into one at ingest, so this table has one entry per language rather
 * than one per synonym.
 */
const BADGES: Record<string, Badge> = {
  java: { src: javaIcon, label: "Java" },
  nodejs: { src: nodejsIcon, label: "Node.js" },
  python: { src: pythonIcon, label: "Python" },
  go: { src: goIcon, label: "Go" },
  php: { src: phpIcon, label: "PHP" },
  ruby: { src: rubyIcon, label: "Ruby" },
  rust: { src: rustIcon, label: "Rust" },
  dotnet: { src: dotnetIcon, label: ".NET" },
  // Web servers are identified by the software, not by the language they are
  // written in: "C" is true of both and distinguishes neither, and which
  // server fronts an application is the thing an operator is asking.
  nginx: { src: nginxIcon, label: "nginx" },
  httpd: { src: httpdIcon, label: "Apache httpd" },
};

/**
 * Resolves a stored runtime to the badge that stands for it.
 *
 * Exported because not every place a badge belongs can render an <img>: the
 * service map draws its nodes in SVG, and needs the URL to put in an <image>.
 */
export function runtimeBadge(runtime?: string): Badge {
  const key = (runtime ?? "").toLowerCase();
  return (
    BADGES[key] ?? {
      src: unknownIcon,
      label: key ? `Unrecognised runtime "${runtime}"` : "Language not reported",
    }
  );
}

/**
 * Looks up the language of every reporting service.
 *
 * The pages that list services already have this in hand; the ones that list
 * service *names* — the map's edges, a trace's root — do not, and this is how
 * they get it. It is the same query, under the same key, so react-query serves
 * it from the cache the other page filled rather than asking again.
 */
export function useServiceRuntimes(): Map<string, string | undefined> {
  const { range, tick } = useTimeRange();

  const { data } = useQuery({
    queryKey: ["services", range, tick],
    queryFn: () => api.services(range),
    refetchInterval: 10_000,
  });

  return new Map((data?.services ?? []).map((service) => [service.service, service.runtime]));
}

/**
 * The language a service is written in, as a small badge beside its name.
 *
 * A service with no runtime recorded still gets a badge, a neutral "?", rather
 * than nothing: a missing image in a column of images reads as a rendering
 * fault, while a question mark reads as the fact it is — apm2go received
 * telemetry that never said what language produced it. The tooltip and the alt
 * text carry the language name, so the badge is never the only carrier of it.
 */
export function RuntimeBadge({ runtime, size = 16 }: { runtime?: string; size?: number }) {
  const badge = runtimeBadge(runtime);

  return (
    <img
      src={badge.src}
      alt={badge.label}
      title={badge.label}
      width={size}
      height={size}
      className="shrink-0 rounded-[4px]"
      style={{ width: size, height: size }}
    />
  );
}
