import markUrl from "../assets/brand/mark-192.png";
import lockupUrl from "../assets/brand/lockup.png";

/**
 * The cup-and-gauge mark, on its own.
 *
 * The artwork is a bitmap with a transparent page, not a tinted glyph: it is
 * full-colour by design — the gauge printed on the cup runs green to red, and
 * flattening it to one colour would throw away the half of the mark that says
 * what the product measures. It therefore needs no theme variant, and is the
 * one element on the page that looks identical in both.
 */
export function BrandMark({ size = 26 }: { size?: number }) {
  return (
    <img
      src={markUrl}
      alt=""
      width={size}
      height={size}
      style={{ width: size, height: size }}
      className="shrink-0 select-none"
      draggable={false}
    />
  );
}

/**
 * The full lockup, exactly as supplied: the cup mark over the "apm2go"
 * wordmark, cropped just below the type so it drops into a header without
 * carrying the tagline's extra height. Shipped as the source artwork rather
 * than rebuilt in live text, so the lettering, the arrows and the colour
 * split are pixel-identical to the approved logo rather than an
 * approximation of it.
 */
export function BrandLockup({ height = 34 }: { height?: number }) {
  const width = Math.round((155 / 96) * height);
  return (
    <img
      src={lockupUrl}
      alt="apm2go"
      width={width}
      height={height}
      style={{ height, width }}
      className="shrink-0 select-none"
      draggable={false}
    />
  );
}
