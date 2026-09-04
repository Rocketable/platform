export function encodeSessionId(id: string) {
  return btoa(id).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

export function decodeSessionId(value: string) {
  let decoded = value;
  try {
    decoded = decodeURIComponent(value);
  } catch {
    decoded = value;
  }
  const padded = decoded.replaceAll("-", "+").replaceAll("_", "/");
  const pad = padded.length % 4 === 0 ? "" : "=".repeat(4 - (padded.length % 4));
  try {
    return atob(padded + pad);
  } catch {
    return decoded;
  }
}
