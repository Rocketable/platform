export function encodeSessionId(id: string) {
  return btoa(id).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

export function decodeSessionId(value: string) {
  return /^(?:[\w-]{4})*(?:[\w-]{2,3})?$/.test(value) ? atob(value.replaceAll("-", "+").replaceAll("_", "/")) : "\u00ff";
}
