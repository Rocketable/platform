export function encodeSessionId(id: string) {
  return btoa(id).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

export function decodeSessionId(value: string) {
  return atob(value.replaceAll("-", "+").replaceAll("_", "/"));
}
