import { expect, test, mock } from "bun:test";
import { Effect } from "effect";
import { copyText } from "./transcript-text";

test("clipboard effects are lazy, copy exact text, and expose failures as Errors", async () => {
  const clipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
  try {
    const pending = Promise.withResolvers<void>();
    const writeText = mock(() => pending.promise);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    const effect = copyText("  exact\ntext\0", {} as Element);
    expect(writeText).not.toHaveBeenCalled();
    const result = Effect.runPromise(effect);
    expect(writeText).toHaveBeenCalledWith("  exact\ntext\0");
    pending.resolve();
    await expect(result).resolves.toBeUndefined();
    for (const cause of [new Error("Permission denied"), "Permission denied"]) {
      writeText.mockImplementation(() => Promise.reject(cause));
      await expect(Effect.runPromise(effect)).rejects.toMatchObject({ message: "Permission denied" });
    }
    const late = Promise.withResolvers<void>();
    writeText.mockImplementation(() => late.promise);
    const copied = mock();
    const failed = mock();
    const done = Promise.withResolvers<void>();
    const stop = Effect.runCallback(effect.pipe(Effect.match({ onSuccess: copied, onFailure: failed })), { onExit: () => done.resolve() });
    stop();
    await done.promise;
    late.resolve();
    await late.promise;
    expect(copied).not.toHaveBeenCalled();
    expect(failed).not.toHaveBeenCalled();
  } finally {
    if (clipboard) Object.defineProperty(navigator, "clipboard", clipboard);
    else Reflect.deleteProperty(navigator, "clipboard");
  }
});

test("plain HTTP copying uses its container and restores focus on success and failure", () => {
  const clipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
  const document = Object.getOwnPropertyDescriptor(globalThis, "document");
  try {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    for (const outcome of ["success", "false", "throw"]) {
      const calls: string[] = [];
      const input = { value: "", style: { position: "", opacity: "" }, select: () => { calls.push("select"); }, remove: () => { calls.push("remove"); } };
      const focus = mock(() => { calls.push("focus"); });
      const createElement = mock(() => input);
      const execCommand = mock(() => {
        calls.push("copy");
        if (outcome === "throw") throw new Error("Copy failed");
        return outcome === "success";
      });
      Object.defineProperty(globalThis, "document", { configurable: true, value: { createElement, activeElement: { focus }, execCommand } });
      const append = mock(() => { calls.push("append"); });
      const effect = copyText("  exact\ntext\0", { append } as unknown as Element);
      expect(calls).toEqual([]);
      if (outcome === "success") expect(Effect.runSync(effect)).toBeUndefined();
      else expect(() => Effect.runSync(effect)).toThrow("Copy failed");
      expect(createElement).toHaveBeenCalledWith("textarea");
      expect(append).toHaveBeenCalledWith(input);
      expect(input.value).toBe("  exact\ntext\0");
      expect(execCommand).toHaveBeenCalledWith("copy");
      expect(calls).toEqual(["append", "select", "copy", "remove", "focus"]);
      expect(focus).toHaveBeenCalledWith({ preventScroll: true });
    }
  } finally {
    if (clipboard) Object.defineProperty(navigator, "clipboard", clipboard);
    else Reflect.deleteProperty(navigator, "clipboard");
    if (document) Object.defineProperty(globalThis, "document", document);
    else Reflect.deleteProperty(globalThis, "document");
  }
});
