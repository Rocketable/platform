export function SlackWarning({ text }: { text: string }) {
  return text === "" ? null : <p role="alert">{text}</p>;
}
