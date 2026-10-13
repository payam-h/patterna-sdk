/**
 * A rotating session id for `non_profiled` mode: random, not derived from anything about the
 * user or device. Rotate it at least per app launch. On React Native install
 * `react-native-get-random-values` (or use expo-crypto) so crypto.getRandomValues exists.
 */
export function newSessionId(getRandomValues?: (a: Uint8Array) => Uint8Array): string {
  const fill =
    getRandomValues ??
    ((globalThis.crypto as Crypto | undefined)?.getRandomValues?.bind(globalThis.crypto) as
      | ((a: Uint8Array) => Uint8Array)
      | undefined);
  if (!fill) throw new Error("newSessionId: crypto.getRandomValues is not available; pass one");
  const bytes = fill(new Uint8Array(16));
  let hex = "";
  for (const b of bytes) hex += b.toString(16).padStart(2, "0");
  return `s_${hex}`;
}
