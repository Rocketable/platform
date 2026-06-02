const BOM_CODE = 0xfeff
const BOM = String.fromCharCode(BOM_CODE)

export function split(text) {
  if (text.charCodeAt(0) !== BOM_CODE) return { bom: false, text }
  return { bom: true, text: text.slice(1) }
}

export function join(text, bom) {
  const stripped = split(text).text
  if (!bom) return stripped
  return BOM + stripped
}
