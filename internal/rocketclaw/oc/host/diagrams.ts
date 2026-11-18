import DOMPurify from "dompurify"

export function sanitizeDiagram(svg: string) {
  return DOMPurify.sanitize(svg, {
    USE_PROFILES: { svg: true },
    ADD_ATTR: ["target", "rel", "role"],
    FORBID_TAGS: ["style", "image"],
    FORBID_CONTENTS: ["style", "script"],
  })
}
