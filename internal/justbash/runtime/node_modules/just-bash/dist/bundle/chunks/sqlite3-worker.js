var __require = /* @__PURE__ */ ((x) => typeof require !== "undefined" ? require : typeof Proxy !== "undefined" ? new Proxy(x, {
  get: (a, b) => (typeof require !== "undefined" ? require : a)[b]
}) : x)(function(x) {
  if (typeof require !== "undefined") return require.apply(this, arguments);
  throw Error('Dynamic require of "' + x + '" is not supported');
});

// src/commands/sqlite3/worker.ts
import { parentPort, workerData } from "node:worker_threads";
import initSqlJs from "sql.js";

// src/fs/sanitize-error.ts
function sanitizeWithUnixPrefixes(message, includeHostRuntimePrefixes, includeFileUrls) {
  if (!message) return message;
  let sanitized = message.replace(/\n\s+at\s.*/g, "");
  if (includeFileUrls) {
    sanitized = sanitized.replace(/\bfile:\/\/\/?[^\s'",)}\]:]+/g, "<path>");
  }
  sanitized = sanitized.replace(
    includeHostRuntimePrefixes ? /(?:\/(?:Users|home|private|var|opt|Library|System|usr|etc|tmp|nix|snap|workspace|root|srv|mnt|app))\b[^\s'",)}\]:]*/g : /(?:\/(?:Users|home|private|var|opt|Library|System|usr|etc|tmp|nix|snap))\b[^\s'",)}\]:]*/g,
    "<path>"
  );
  sanitized = sanitized.replace(/node:internal\/[^\s'",)}\]:]+/g, "<internal>");
  sanitized = sanitized.replace(/[A-Z]:\\[^\s'",)}\]:]+/g, "<path>");
  if (includeFileUrls) {
    sanitized = sanitized.replace(/\\\\[^\s\\]+\\[^\s'",)}\]:]+/g, "<path>");
  }
  return sanitized;
}
function sanitizeErrorMessage(message) {
  return sanitizeWithUnixPrefixes(message, false, false);
}
function sanitizeHostErrorMessage(message) {
  return sanitizeWithUnixPrefixes(message, true, true);
}

// src/security/defense-in-depth-box.ts
import * as nodeAsyncHooks from "node:async_hooks";
import * as nodeModule from "node:module";

// src/security/blocked-globals.ts
var blockedGlobalViolationTypes;
function getBlockedGlobalViolationTypes() {
  if (!blockedGlobalViolationTypes) getBlockedGlobals();
  return blockedGlobalViolationTypes;
}
function getBlockedGlobals() {
  const globals = [
    // Direct code execution vectors
    {
      prop: "Function",
      target: globalThis,
      violationType: "function_constructor",
      strategy: "throw",
      reason: "Function constructor allows arbitrary code execution"
    },
    {
      prop: "eval",
      target: globalThis,
      violationType: "eval",
      strategy: "throw",
      reason: "eval() allows arbitrary code execution"
    },
    // Timer functions with string argument allow code execution
    {
      prop: "setTimeout",
      target: globalThis,
      violationType: "setTimeout",
      strategy: "throw",
      reason: "setTimeout with string argument allows code execution"
    },
    {
      prop: "setInterval",
      target: globalThis,
      violationType: "setInterval",
      strategy: "throw",
      reason: "setInterval with string argument allows code execution"
    },
    {
      prop: "setImmediate",
      target: globalThis,
      violationType: "setImmediate",
      strategy: "throw",
      reason: "setImmediate could be used to escape sandbox context"
    },
    // Note: We intentionally do NOT block `process` entirely because:
    // 1. Node.js internals (Promise resolution, etc.) use process.nextTick
    // 2. Blocking process entirely breaks normal async operation
    // 3. The primary code execution vectors (Function, eval) are already blocked
    // However, we DO block specific dangerous process properties.
    {
      prop: "env",
      target: process,
      violationType: "process_env",
      strategy: "throw",
      reason: "process.env could leak sensitive environment variables",
      // Node.js internals and bundled dependencies read these env vars
      // during module loading, file watching, and I/O within the
      // AsyncLocalStorage context. None are user secrets.
      allowedKeys: /* @__PURE__ */ new Set([
        // Node.js core
        "NODE_V8_COVERAGE",
        "NODE_DEBUG",
        "NODE_DEBUG_NATIVE",
        "NODE_COMPILE_CACHE",
        "NODE_ENV",
        "WATCH_REPORT_DEPENDENCIES",
        // Dependencies
        "FORCE_COLOR",
        // chalk/supports-color
        "DEBUG",
        // debug package
        "UNDICI_NO_FG",
        // undici (Node.js fetch)
        "JEST_WORKER_ID",
        // jest/vitest worker detection
        "__MINIMATCH_TESTING_PLATFORM__",
        // minimatch
        "LOG_TOKENS",
        // query engine debug logging
        "LOG_STREAM"
        // query engine debug logging
      ])
    },
    {
      prop: "binding",
      target: process,
      violationType: "process_binding",
      strategy: "throw",
      reason: "process.binding provides access to native Node.js modules"
    },
    {
      prop: "_linkedBinding",
      target: process,
      violationType: "process_binding",
      strategy: "throw",
      reason: "process._linkedBinding provides access to native Node.js modules"
    },
    {
      prop: "dlopen",
      target: process,
      violationType: "process_dlopen",
      strategy: "throw",
      reason: "process.dlopen allows loading native addons"
    },
    {
      prop: "getBuiltinModule",
      target: process,
      violationType: "process_get_builtin_module",
      strategy: "throw",
      reason: "process.getBuiltinModule allows loading native Node.js modules (fs, child_process, vm)"
    },
    // Note: process.mainModule is handled specially in defense-in-depth-box.ts
    // and worker-defense-in-depth.ts because it may be undefined in ESM contexts
    // but we still want to block both reading and setting it.
    // Process control vectors
    {
      prop: "exit",
      target: process,
      violationType: "process_exit",
      strategy: "throw",
      reason: "process.exit could terminate the interpreter"
    },
    {
      prop: "abort",
      target: process,
      violationType: "process_exit",
      strategy: "throw",
      reason: "process.abort could crash the interpreter"
    },
    {
      prop: "kill",
      target: process,
      violationType: "process_kill",
      strategy: "throw",
      reason: "process.kill could signal other processes"
    },
    // Privilege escalation vectors
    {
      prop: "setuid",
      target: process,
      violationType: "process_setuid",
      strategy: "throw",
      reason: "process.setuid could escalate privileges"
    },
    {
      prop: "setgid",
      target: process,
      violationType: "process_setuid",
      strategy: "throw",
      reason: "process.setgid could escalate privileges"
    },
    {
      prop: "seteuid",
      target: process,
      violationType: "process_setuid",
      strategy: "throw",
      reason: "process.seteuid could escalate effective user privileges"
    },
    {
      prop: "setegid",
      target: process,
      violationType: "process_setuid",
      strategy: "throw",
      reason: "process.setegid could escalate effective group privileges"
    },
    {
      prop: "initgroups",
      target: process,
      violationType: "process_setuid",
      strategy: "throw",
      reason: "process.initgroups could modify supplementary group IDs"
    },
    {
      prop: "setgroups",
      target: process,
      violationType: "process_setuid",
      strategy: "throw",
      reason: "process.setgroups could modify supplementary group IDs"
    },
    // File permission manipulation
    {
      prop: "umask",
      target: process,
      violationType: "process_umask",
      strategy: "throw",
      reason: "process.umask could modify file creation permissions"
    },
    // Information disclosure vectors
    // Note: process.argv is an array (object) so gets an object proxy
    {
      prop: "argv",
      target: process,
      violationType: "process_argv",
      strategy: "throw",
      reason: "process.argv may contain secrets in CLI arguments"
    },
    // Note: process.execPath is a string primitive, handled specially
    // in defense-in-depth-box.ts and worker-defense-in-depth.ts
    // Note: process.connected is a boolean primitive, handled specially
    // in defense-in-depth-box.ts and worker-defense-in-depth.ts
    // Working directory access/manipulation
    {
      prop: "cwd",
      target: process,
      violationType: "process_chdir",
      strategy: "throw",
      reason: "process.cwd could disclose real host working directory path"
    },
    {
      prop: "chdir",
      target: process,
      violationType: "process_chdir",
      strategy: "throw",
      reason: "process.chdir could confuse the interpreter's CWD tracking"
    },
    // Diagnostic report (leaks full environment, host paths, system info)
    {
      prop: "report",
      target: process,
      violationType: "process_report",
      strategy: "throw",
      reason: "process.report could disclose full environment, host paths, and system info"
    },
    // Environment file loading (Node 21.7+)
    {
      prop: "loadEnvFile",
      target: process,
      violationType: "process_env",
      strategy: "throw",
      reason: "process.loadEnvFile could load env files bypassing env proxy"
    },
    // Exception handler manipulation
    {
      prop: "setUncaughtExceptionCaptureCallback",
      target: process,
      violationType: "process_exception_handler",
      strategy: "throw",
      reason: "setUncaughtExceptionCaptureCallback could intercept security errors"
    },
    ...[
      "on",
      "once",
      "addListener",
      "prependListener",
      "prependOnceListener"
    ].map(
      (prop) => ({
        prop,
        target: process,
        violationType: "process_exception_handler",
        strategy: "throw",
        reason: "process-global listeners can outlive the sandbox and lose its security context"
      })
    ),
    // IPC communication vectors (may be undefined in non-IPC contexts)
    {
      prop: "send",
      target: process,
      violationType: "process_send",
      strategy: "throw",
      reason: "process.send could communicate with parent process in IPC contexts"
    },
    {
      prop: "channel",
      target: process,
      violationType: "process_channel",
      strategy: "throw",
      reason: "process.channel could access IPC channel to parent process"
    },
    // Timing side-channel vectors
    {
      prop: "cpuUsage",
      target: process,
      violationType: "process_timing",
      strategy: "throw",
      reason: "process.cpuUsage could enable timing side-channel attacks"
    },
    {
      prop: "memoryUsage",
      target: process,
      violationType: "process_timing",
      strategy: "throw",
      reason: "process.memoryUsage could enable timing side-channel attacks"
    },
    {
      prop: "hrtime",
      target: process,
      violationType: "process_timing",
      strategy: "throw",
      reason: "process.hrtime could enable timing side-channel attacks"
    },
    // We also don't block `require` because:
    // 1. It may not exist in all environments (ESM)
    // 2. import() is the modern escape vector and can't be blocked this way
    // Reference leak vectors
    {
      prop: "WeakRef",
      target: globalThis,
      violationType: "weak_ref",
      strategy: "throw",
      reason: "WeakRef could be used to leak references outside sandbox"
    },
    {
      prop: "FinalizationRegistry",
      target: globalThis,
      violationType: "finalization_registry",
      strategy: "throw",
      reason: "FinalizationRegistry could be used to leak references outside sandbox"
    },
    // Introspection/interception vectors (freeze instead of throw)
    // SECURITY RATIONALE: Reflect is frozen (not blocked) because:
    // 1. Defense infrastructure uses Reflect.apply/get/set/construct internally
    // 2. Frozen Reflect cannot be mutated but remains fully functional
    // 3. Reflect.construct(Function, ['code']) IS safe because globalThis.Function
    //    is replaced with a blocking proxy — Reflect.construct receives the proxy
    // 4. Security depends on NEVER leaking original Function/eval references.
    //    If an unpatched Function ref leaked, Reflect.construct would bypass defense.
    {
      prop: "Reflect",
      target: globalThis,
      violationType: "reflect",
      strategy: "freeze",
      reason: "Reflect provides introspection capabilities"
    },
    {
      prop: "Proxy",
      target: globalThis,
      violationType: "proxy",
      strategy: "throw",
      reason: "Proxy allows intercepting and modifying object behavior"
    },
    // WebAssembly allows arbitrary code execution
    {
      prop: "WebAssembly",
      target: globalThis,
      violationType: "webassembly",
      strategy: "throw",
      reason: "WebAssembly allows executing arbitrary compiled code"
    },
    // SharedArrayBuffer and Atomics can enable side-channel attacks
    {
      prop: "SharedArrayBuffer",
      target: globalThis,
      violationType: "shared_array_buffer",
      strategy: "throw",
      reason: "SharedArrayBuffer could enable side-channel communication or timing attacks"
    },
    {
      prop: "Atomics",
      target: globalThis,
      violationType: "atomics",
      strategy: "throw",
      reason: "Atomics could enable side-channel communication or timing attacks"
    },
    // Note: Error.prepareStackTrace is handled specially in defense-in-depth-box.ts
    // because we only want to block SETTING it, not reading (V8 reads it internally)
    // Timing side-channel: performance.now() provides sub-millisecond resolution
    // Note: Date.now() is intentionally NOT blocked — it's used for $SECONDS,
    // date command, and has only ~1ms resolution (vs process.hrtime at ns).
    {
      prop: "performance",
      target: globalThis,
      violationType: "performance_timing",
      strategy: "throw",
      reason: "performance.now() provides sub-millisecond timing for side-channel attacks"
    },
    // Block direct access to process.stdout and process.stderr to prevent
    // writing to the host's actual stdout/stderr, bypassing the interpreter's
    // output accumulation.
    {
      prop: "stdout",
      target: process,
      violationType: "process_stdout",
      strategy: "throw",
      reason: "process.stdout could bypass interpreter output to write to host stdout"
    },
    {
      prop: "stderr",
      target: process,
      violationType: "process_stderr",
      strategy: "throw",
      reason: "process.stderr could bypass interpreter output to write to host stderr"
    },
    // Prototype pollution vectors
    {
      prop: "__defineGetter__",
      target: Object.prototype,
      violationType: "prototype_mutation",
      strategy: "throw",
      reason: "__defineGetter__ allows prototype pollution via getter injection"
    },
    {
      prop: "__defineSetter__",
      target: Object.prototype,
      violationType: "prototype_mutation",
      strategy: "throw",
      reason: "__defineSetter__ allows prototype pollution via setter injection"
    },
    {
      prop: "__lookupGetter__",
      target: Object.prototype,
      violationType: "prototype_mutation",
      strategy: "throw",
      reason: "__lookupGetter__ enables introspection for prototype pollution attacks"
    },
    {
      prop: "__lookupSetter__",
      target: Object.prototype,
      violationType: "prototype_mutation",
      strategy: "throw",
      reason: "__lookupSetter__ enables introspection for prototype pollution attacks"
    },
    // Freeze JSON and Math to prevent mutation of built-in utility objects
    {
      prop: "JSON",
      target: globalThis,
      violationType: "json_mutation",
      strategy: "freeze",
      reason: "Freeze JSON to prevent mutation of parsing/serialization"
    },
    {
      prop: "Math",
      target: globalThis,
      violationType: "math_mutation",
      strategy: "freeze",
      reason: "Freeze Math to prevent mutation of math utilities"
    }
  ];
  try {
    const AsyncFunction = Object.getPrototypeOf(async () => {
    }).constructor;
    if (AsyncFunction && AsyncFunction !== Function) {
      globals.push({
        prop: "constructor",
        target: Object.getPrototypeOf(async () => {
        }),
        violationType: "async_function_constructor",
        strategy: "throw",
        reason: "AsyncFunction constructor allows arbitrary async code execution"
      });
    }
  } catch {
  }
  try {
    const GeneratorFunction = Object.getPrototypeOf(
      function* () {
      }
    ).constructor;
    if (GeneratorFunction && GeneratorFunction !== Function) {
      globals.push({
        prop: "constructor",
        target: Object.getPrototypeOf(function* () {
        }),
        violationType: "generator_function_constructor",
        strategy: "throw",
        reason: "GeneratorFunction constructor allows arbitrary generator code execution"
      });
    }
  } catch {
  }
  try {
    const AsyncGeneratorFunction = Object.getPrototypeOf(
      async function* () {
      }
    ).constructor;
    if (AsyncGeneratorFunction && AsyncGeneratorFunction !== Function && AsyncGeneratorFunction !== Object.getPrototypeOf(async () => {
    }).constructor) {
      globals.push({
        prop: "constructor",
        target: Object.getPrototypeOf(async function* () {
        }),
        violationType: "async_generator_function_constructor",
        strategy: "throw",
        reason: "AsyncGeneratorFunction constructor allows arbitrary async generator code execution"
      });
    }
  } catch {
  }
  const availableGlobals = globals.filter((g) => {
    try {
      return g.target[g.prop] !== void 0;
    } catch {
      return false;
    }
  });
  blockedGlobalViolationTypes ??= new Set(
    availableGlobals.map(({ violationType }) => violationType)
  );
  return availableGlobals;
}

// src/security/safe-timestamp.ts
var performanceTimeOrigin = performance.timeOrigin;
var performanceNow = performance.now.bind(performance);
function getSafeTimestamp() {
  return Math.floor(performanceTimeOrigin + performanceNow());
}

// src/security/violation-error-message.ts
var NON_EXCLUDABLE_VIOLATION_TYPES = /* @__PURE__ */ new Set([
  "function_constructor",
  "async_function_constructor",
  "generator_function_constructor",
  "async_generator_function_constructor"
]);
function assertExcludableViolationTypes(violationTypes) {
  const nonExcludable = violationTypes?.find(
    (type) => NON_EXCLUDABLE_VIOLATION_TYPES.has(type)
  );
  if (nonExcludable) {
    throw new RangeError(
      `defenseInDepth.excludeViolationTypes cannot disable the non-excludable "${nonExcludable}" protection`
    );
  }
}
var DEFENSE_IN_DEPTH_NOTICE = "\n\nThis is a defense-in-depth measure and indicates a bug in just-bash. Please report this at security@vercel.com";
function formatViolationErrorMessage(message, violationType, canExclude) {
  const exclusionHint = canExclude && !NON_EXCLUDABLE_VIOLATION_TYPES.has(violationType) ? `

If this access is required by trusted host-runtime code, add "${violationType}" to defenseInDepth.excludeViolationTypes.` : "";
  return message + DEFENSE_IN_DEPTH_NOTICE + exclusionHint;
}

// src/security/defense-in-depth-box.ts
var IS_BROWSER = typeof __BROWSER__ !== "undefined" && __BROWSER__;
var AsyncLocalStorageClass = null;
var nodeModuleApi = null;
var nodeModuleClass = null;
if (!IS_BROWSER) {
  try {
    AsyncLocalStorageClass = nodeAsyncHooks.AsyncLocalStorage;
    nodeModuleApi = nodeModule;
    nodeModuleClass = nodeModuleApi.Module ?? nodeModuleApi.default ?? null;
  } catch {
  }
}
var executionContext = !IS_BROWSER && AsyncLocalStorageClass ? new AsyncLocalStorageClass() : null;

// src/security/worker-defense-in-depth.ts
var WORKER_SPECIAL_EXCLUDABLE_VIOLATION_TYPES = /* @__PURE__ */ new Set([
  "error_prepare_stack_trace",
  "module_load",
  "module_resolve_filename",
  "process_main_module",
  "process_exec_path",
  "process_connected"
]);
var WorkerSecurityViolationError = class extends Error {
  constructor(message, violation) {
    super(
      formatViolationErrorMessage(
        message,
        violation.type,
        WORKER_SPECIAL_EXCLUDABLE_VIOLATION_TYPES.has(violation.type) || getBlockedGlobalViolationTypes().has(violation.type)
      )
    );
    this.violation = violation;
    this.name = "WorkerSecurityViolationError";
  }
  violation;
};
var MAX_STORED_VIOLATIONS = 1e3;
var nodeModuleClass2 = null;
try {
  const getBuiltinModule = process.getBuiltinModule;
  const moduleApi = typeof getBuiltinModule === "function" ? getBuiltinModule("module") : typeof __require === "function" ? (
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    __require("node:module")
  ) : null;
  nodeModuleClass2 = moduleApi?.Module ?? moduleApi?.default ?? null;
  if (typeof getBuiltinModule === "function") {
    const workerThreads = getBuiltinModule("worker_threads");
    const isWorkerThread = workerThreads?.isMainThread === false;
    if (isWorkerThread) {
      const cryptoApi = getBuiltinModule("crypto");
      cryptoApi?.randomUUID?.();
    }
  }
} catch {
  nodeModuleClass2 = null;
}
function generateExecutionId() {
  if (typeof crypto !== "undefined" && crypto.randomUUID) {
    return crypto.randomUUID();
  }
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = Math.random() * 16 | 0;
    const v = c === "x" ? r : r & 3 | 8;
    return v.toString(16);
  });
}
var WorkerDefenseInDepth = class {
  config;
  isActivated = false;
  originalDescriptors = [];
  /**
   * Restores the native accessor backing slot for module methods patched via
   * their setter (see protectModuleMethod). Restoring the descriptor alone
   * does not clear that slot, since the runtime's setter, not the descriptor
   * shape, is what tracks the active override.
   */
  moduleAccessorResets = [];
  patchFailures = [];
  violations = [];
  executionId;
  /**
   * Original Proxy constructor, captured before patching.
   * This is captured at instance creation time to ensure we get the unpatched version.
   */
  originalProxy;
  /**
   * Recursion guard to prevent infinite loops when proxy traps trigger
   * code that accesses the same proxied object (e.g., process.env).
   */
  inTrap = false;
  /**
   * Create and activate the worker defense layer.
   *
   * @param config - Configuration for the defense layer
   */
  constructor(config) {
    assertExcludableViolationTypes(config.excludeViolationTypes);
    this.originalProxy = Proxy;
    this.config = config;
    this.executionId = generateExecutionId();
    if (config.enabled !== false) {
      this.activate();
    }
  }
  /**
   * Get statistics about the defense layer.
   */
  getStats() {
    return {
      violationsBlocked: this.violations.length,
      violations: [...this.violations],
      isActive: this.isActivated
    };
  }
  /**
   * Clear stored violations. Useful for testing.
   */
  clearViolations() {
    this.violations = [];
  }
  /**
   * Get the execution ID for this worker.
   */
  getExecutionId() {
    return this.executionId;
  }
  /**
   * Deactivate the defense layer and restore original globals.
   * Typically only needed for testing.
   */
  deactivate() {
    if (!this.isActivated) {
      return;
    }
    this.restorePatches();
    this.isActivated = false;
  }
  /**
   * Activate the defense layer by applying patches.
   */
  activate() {
    if (this.isActivated) {
      return;
    }
    try {
      this.applyPatches();
      this.isActivated = true;
    } catch (error) {
      this.restorePatches();
      this.isActivated = false;
      throw error;
    }
  }
  /**
   * Get a human-readable path for a target object and property.
   */
  getPathForTarget(target, prop) {
    if (target === globalThis) {
      return `globalThis.${prop}`;
    }
    if (typeof process !== "undefined" && target === process) {
      return `process.${prop}`;
    }
    if (target === Error) {
      return `Error.${prop}`;
    }
    if (target === Function.prototype) {
      return `Function.prototype.${prop}`;
    }
    if (target === Object.prototype) {
      return `Object.prototype.${prop}`;
    }
    return `<object>.${prop}`;
  }
  /**
   * Record a violation and invoke the callback.
   * In worker context, blocking always happens (no audit mode context check).
   */
  recordViolation(type, path, message) {
    const violation = {
      timestamp: getSafeTimestamp(),
      type,
      message,
      path,
      stack: new Error().stack,
      executionId: this.executionId
    };
    if (this.violations.length < MAX_STORED_VIOLATIONS) {
      this.violations.push(violation);
    }
    if (this.config.onViolation) {
      try {
        this.config.onViolation(violation);
      } catch (e) {
        console.debug(
          "[WorkerDefenseInDepth] onViolation callback threw:",
          e instanceof Error ? e.message : e
        );
      }
    }
    return violation;
  }
  /**
   * Create a blocking proxy for a function.
   * In worker context, always blocks (no context check needed).
   */
  createBlockingProxy(original, path, violationType) {
    const self = this;
    const auditMode = this.config.auditMode;
    return new this.originalProxy(original, {
      apply(target, thisArg, args) {
        const message = `${path} is blocked in worker context`;
        const violation = self.recordViolation(violationType, path, message);
        if (!auditMode) {
          throw new WorkerSecurityViolationError(message, violation);
        }
        return Reflect.apply(target, thisArg, args);
      },
      construct(target, args, newTarget) {
        const message = `${path} constructor is blocked in worker context`;
        const violation = self.recordViolation(violationType, path, message);
        if (!auditMode) {
          throw new WorkerSecurityViolationError(message, violation);
        }
        return Reflect.construct(target, args, newTarget);
      }
    });
  }
  /**
   * Create a blocking proxy for an object (blocks all property access).
   */
  createBlockingObjectProxy(original, path, violationType, allowedKeys) {
    const self = this;
    const auditMode = this.config.auditMode;
    return new this.originalProxy(original, {
      get(target, prop, receiver) {
        if (self.inTrap) {
          return Reflect.get(target, prop, receiver);
        }
        if (allowedKeys && typeof prop === "string" && allowedKeys.has(prop)) {
          return Reflect.get(target, prop, receiver);
        }
        self.inTrap = true;
        try {
          const fullPath = `${path}.${String(prop)}`;
          const message = `${fullPath} is blocked in worker context`;
          const violation = self.recordViolation(
            violationType,
            fullPath,
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return Reflect.get(target, prop, receiver);
        } finally {
          self.inTrap = false;
        }
      },
      set(target, prop, value, receiver) {
        const setValue = () => path === "process.env" && prop === "DEBUG" ? Reflect.set(target, prop, value) : Reflect.set(target, prop, value, receiver);
        if (self.inTrap) {
          return setValue();
        }
        self.inTrap = true;
        try {
          const fullPath = `${path}.${String(prop)}`;
          const message = `${fullPath} modification is blocked in worker context`;
          const violation = self.recordViolation(
            violationType,
            fullPath,
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return setValue();
        } finally {
          self.inTrap = false;
        }
      },
      ownKeys(target) {
        if (self.inTrap) {
          return Reflect.ownKeys(target);
        }
        self.inTrap = true;
        try {
          const message = `${path} enumeration is blocked in worker context`;
          const violation = self.recordViolation(violationType, path, message);
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return Reflect.ownKeys(target);
        } finally {
          self.inTrap = false;
        }
      },
      getOwnPropertyDescriptor(target, prop) {
        if (self.inTrap) {
          return Reflect.getOwnPropertyDescriptor(target, prop);
        }
        self.inTrap = true;
        try {
          const fullPath = `${path}.${String(prop)}`;
          const message = `${fullPath} descriptor access is blocked in worker context`;
          const violation = self.recordViolation(
            violationType,
            fullPath,
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return Reflect.getOwnPropertyDescriptor(target, prop);
        } finally {
          self.inTrap = false;
        }
      },
      has(target, prop) {
        if (self.inTrap) {
          return Reflect.has(target, prop);
        }
        self.inTrap = true;
        try {
          const fullPath = `${path}.${String(prop)}`;
          const message = `${fullPath} existence check is blocked in worker context`;
          const violation = self.recordViolation(
            violationType,
            fullPath,
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return Reflect.has(target, prop);
        } finally {
          self.inTrap = false;
        }
      },
      deleteProperty(target, prop) {
        if (self.inTrap) {
          return Reflect.deleteProperty(target, prop);
        }
        self.inTrap = true;
        try {
          const fullPath = `${path}.${String(prop)}`;
          const message = `${fullPath} deletion is blocked in worker context`;
          const violation = self.recordViolation(
            violationType,
            fullPath,
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return Reflect.deleteProperty(target, prop);
        } finally {
          self.inTrap = false;
        }
      },
      setPrototypeOf(target, proto) {
        if (self.inTrap) {
          return Reflect.setPrototypeOf(target, proto);
        }
        self.inTrap = true;
        try {
          const message = `${path} setPrototypeOf is blocked in worker context`;
          const violation = self.recordViolation(violationType, path, message);
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return Reflect.setPrototypeOf(target, proto);
        } finally {
          self.inTrap = false;
        }
      },
      defineProperty(target, prop, descriptor) {
        if (self.inTrap) {
          return Reflect.defineProperty(target, prop, descriptor);
        }
        self.inTrap = true;
        try {
          const fullPath = `${path}.${String(prop)}`;
          const message = `${fullPath} defineProperty is blocked in worker context`;
          const violation = self.recordViolation(
            violationType,
            fullPath,
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return Reflect.defineProperty(target, prop, descriptor);
        } finally {
          self.inTrap = false;
        }
      }
    });
  }
  createReadonlyObjectProxy(original, path, violationType) {
    const auditMode = this.config.auditMode;
    const allowOrThrow = (operation) => {
      const message = `${path} ${operation} is blocked in worker context`;
      const violation = this.recordViolation(violationType, path, message);
      if (!auditMode) {
        throw new WorkerSecurityViolationError(message, violation);
      }
    };
    return new this.originalProxy(original, {
      set(target, prop, value, receiver) {
        allowOrThrow("modification");
        return Reflect.set(target, prop, value, receiver);
      },
      defineProperty(target, prop, descriptor) {
        allowOrThrow("defineProperty");
        return Reflect.defineProperty(target, prop, descriptor);
      },
      deleteProperty(target, prop) {
        allowOrThrow("deletion");
        return Reflect.deleteProperty(target, prop);
      },
      setPrototypeOf(target, prototype) {
        allowOrThrow("setPrototypeOf");
        return Reflect.setPrototypeOf(target, prototype);
      },
      preventExtensions(target) {
        allowOrThrow("preventExtensions");
        return Reflect.preventExtensions(target);
      }
    });
  }
  /**
   * Apply security patches to dangerous globals.
   */
  applyPatches() {
    this.patchFailures = [];
    const blockedGlobals = getBlockedGlobals();
    const excludeTypes = new Set(this.config.excludeViolationTypes ?? []);
    const workerInfrastructureListenerMethods = /* @__PURE__ */ new Set([
      "on",
      "once",
      "addListener",
      "prependListener",
      "prependOnceListener"
    ]);
    const permanentIntrinsicPatches = [];
    for (const blocked of blockedGlobals) {
      if (excludeTypes.has(blocked.violationType)) {
        continue;
      }
      if (blocked.target === process && workerInfrastructureListenerMethods.has(blocked.prop)) {
        continue;
      }
      if (blocked.strategy === "freeze") {
        permanentIntrinsicPatches.push(blocked);
        continue;
      }
      this.applyPatch(blocked);
    }
    if (!excludeTypes.has("function_constructor")) {
      this.protectConstructorChain(excludeTypes);
    }
    if (!excludeTypes.has("error_prepare_stack_trace")) {
      this.protectErrorPrepareStackTrace();
    }
    if (!excludeTypes.has("module_load")) {
      this.protectModuleLoad();
    }
    if (!excludeTypes.has("module_resolve_filename")) {
      this.protectModuleResolveFilename();
    }
    if (!excludeTypes.has("process_main_module")) {
      this.protectProcessMainModule();
    }
    if (!excludeTypes.has("process_exec_path")) {
      this.protectProcessExecPath();
    }
    if (!excludeTypes.has("process_connected")) {
      this.protectProcessConnected();
    }
    if (!excludeTypes.has("proxy")) {
      this.protectProxyRevocable();
    }
    const criticalPaths = [
      "Function.prototype.constructor",
      "Module._load",
      "Module._resolveFilename"
    ];
    const criticalFailures = this.patchFailures.filter(
      (path) => criticalPaths.includes(path)
    );
    if (criticalFailures.length > 0) {
      this.restorePatches();
      throw new Error(
        `WorkerDefenseInDepth: critical patches failed: ${criticalFailures.join(", ")}`
      );
    }
    for (const blocked of permanentIntrinsicPatches) {
      this.applyPatch(blocked);
    }
    this.lockWellKnownSymbols();
  }
  /**
   * Lock well-known Symbol properties on built-in constructors/prototypes.
   */
  lockWellKnownSymbols() {
    const lock = (obj, sym) => {
      try {
        const desc = Object.getOwnPropertyDescriptor(obj, sym);
        if (desc?.configurable) {
          if ("value" in desc) {
            Object.defineProperty(obj, sym, {
              ...desc,
              configurable: false,
              writable: false
            });
            return;
          }
          Object.defineProperty(obj, sym, { ...desc, configurable: false });
        }
      } catch {
      }
    };
    for (const ctor of [Array, Map, Set, RegExp, Promise]) {
      lock(ctor, Symbol.species);
    }
    for (const proto of [
      Array.prototype,
      String.prototype,
      Map.prototype,
      Set.prototype
    ]) {
      lock(proto, Symbol.iterator);
    }
    lock(Symbol.prototype, Symbol.toPrimitive);
    lock(Date.prototype, Symbol.toPrimitive);
    for (const sym of [
      Symbol.match,
      Symbol.matchAll,
      Symbol.replace,
      Symbol.search,
      Symbol.split
    ]) {
      lock(RegExp.prototype, sym);
    }
    lock(Function.prototype, Symbol.hasInstance);
    lock(Array.prototype, Symbol.unscopables);
    for (const proto of [
      Map.prototype,
      Set.prototype,
      Promise.prototype,
      ArrayBuffer.prototype
    ]) {
      lock(proto, Symbol.toStringTag);
    }
  }
  /**
   * Block Proxy.revocable to prevent bypassing Proxy constructor blocking.
   *
   * Proxy.revocable internally uses the real Proxy constructor, so it bypasses
   * our blocking proxy on globalThis.Proxy. We replace it with a wrapper that
   * always blocks in worker context.
   */
  protectProxyRevocable() {
    const self = this;
    const auditMode = this.config.auditMode;
    try {
      const originalRevocable = this.originalProxy.revocable;
      if (typeof originalRevocable !== "function") return;
      const descriptor = Object.getOwnPropertyDescriptor(
        this.originalProxy,
        "revocable"
      );
      this.originalDescriptors.push({
        target: this.originalProxy,
        prop: "revocable",
        descriptor
      });
      Object.defineProperty(this.originalProxy, "revocable", {
        value: function revocable(_target, _handler) {
          const message = "Proxy.revocable is blocked in worker context";
          const violation = self.recordViolation(
            "proxy",
            "Proxy.revocable",
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return originalRevocable(_target, _handler);
        },
        writable: false,
        configurable: true
        // Must be configurable for restoration
      });
    } catch {
    }
  }
  /**
   * Protect against .constructor.constructor escape vector.
   * @param excludeTypes - Set of violation types to skip
   */
  protectConstructorChain(excludeTypes) {
    let AsyncFunction = null;
    let GeneratorFunction = null;
    let AsyncGeneratorFunction = null;
    try {
      AsyncFunction = Object.getPrototypeOf(async () => {
      }).constructor;
    } catch {
    }
    try {
      GeneratorFunction = Object.getPrototypeOf(function* () {
      }).constructor;
    } catch {
    }
    try {
      AsyncGeneratorFunction = Object.getPrototypeOf(
        async function* () {
        }
      ).constructor;
    } catch {
    }
    this.patchPrototypeConstructor(
      Function.prototype,
      "Function.prototype.constructor",
      "function_constructor"
    );
    if (!excludeTypes.has("async_function_constructor") && AsyncFunction && AsyncFunction !== Function) {
      this.patchPrototypeConstructor(
        AsyncFunction.prototype,
        "AsyncFunction.prototype.constructor",
        "async_function_constructor"
      );
    }
    if (!excludeTypes.has("generator_function_constructor") && GeneratorFunction && GeneratorFunction !== Function) {
      this.patchPrototypeConstructor(
        GeneratorFunction.prototype,
        "GeneratorFunction.prototype.constructor",
        "generator_function_constructor"
      );
    }
    if (!excludeTypes.has("async_generator_function_constructor") && AsyncGeneratorFunction && AsyncGeneratorFunction !== Function && AsyncGeneratorFunction !== AsyncFunction) {
      this.patchPrototypeConstructor(
        AsyncGeneratorFunction.prototype,
        "AsyncGeneratorFunction.prototype.constructor",
        "async_generator_function_constructor"
      );
    }
  }
  /**
   * Protect Error.prepareStackTrace from being set.
   */
  protectErrorPrepareStackTrace() {
    const self = this;
    const auditMode = this.config.auditMode;
    try {
      const originalDescriptor = Object.getOwnPropertyDescriptor(
        Error,
        "prepareStackTrace"
      );
      this.originalDescriptors.push({
        target: Error,
        prop: "prepareStackTrace",
        descriptor: originalDescriptor
      });
      let currentValue = originalDescriptor?.value;
      Object.defineProperty(Error, "prepareStackTrace", {
        get() {
          return currentValue;
        },
        set(value) {
          const message = "Error.prepareStackTrace modification is blocked in worker context";
          const violation = self.recordViolation(
            "error_prepare_stack_trace",
            "Error.prepareStackTrace",
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          currentValue = value;
        },
        configurable: true
      });
    } catch {
    }
  }
  /**
   * Patch a prototype's constructor property.
   *
   * Returns a proxy that allows reading properties (like .name) but blocks
   * calling the constructor as a function (which would allow code execution).
   */
  patchPrototypeConstructor(prototype, path, violationType) {
    const self = this;
    const auditMode = this.config.auditMode;
    try {
      const originalDescriptor = Object.getOwnPropertyDescriptor(
        prototype,
        "constructor"
      );
      this.originalDescriptors.push({
        target: prototype,
        prop: "constructor",
        descriptor: originalDescriptor
      });
      const originalValue = originalDescriptor?.value;
      const constructorProxy = originalValue && typeof originalValue === "function" ? new this.originalProxy(originalValue, {
        apply(_target, _thisArg, _args) {
          const message = `${path} invocation is blocked in worker context`;
          const violation = self.recordViolation(
            violationType,
            path,
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return void 0;
        },
        construct(_target, _args, _newTarget) {
          const message = `${path} construction is blocked in worker context`;
          const violation = self.recordViolation(
            violationType,
            path,
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return {};
        },
        // Allow all property access (like .name, .prototype, etc.)
        get(target, prop, receiver) {
          return Reflect.get(target, prop, receiver);
        },
        getPrototypeOf(target) {
          return Reflect.getPrototypeOf(target);
        },
        has(target, prop) {
          return Reflect.has(target, prop);
        },
        ownKeys(target) {
          return Reflect.ownKeys(target);
        },
        getOwnPropertyDescriptor(target, prop) {
          return Reflect.getOwnPropertyDescriptor(target, prop);
        }
      }) : originalValue;
      Object.defineProperty(prototype, "constructor", {
        get() {
          return constructorProxy;
        },
        set(value) {
          const message = `${path} modification is blocked in worker context`;
          const violation = self.recordViolation(violationType, path, message);
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          Object.defineProperty(this, "constructor", {
            value,
            writable: true,
            configurable: true
          });
        },
        configurable: true
      });
    } catch {
      this.patchFailures.push(path);
    }
  }
  /**
   * Protect process.mainModule from being accessed or set.
   *
   * The attack vector is:
   * ```
   * process.mainModule.require('child_process').execSync('whoami')
   * process.mainModule.constructor._load('vm')
   * ```
   *
   * process.mainModule may be undefined in ESM contexts but could exist in
   * CommonJS workers. We block both reading and setting.
   */
  protectProcessMainModule() {
    if (typeof process === "undefined") return;
    const self = this;
    const auditMode = this.config.auditMode;
    try {
      const originalDescriptor = Object.getOwnPropertyDescriptor(
        process,
        "mainModule"
      );
      this.originalDescriptors.push({
        target: process,
        prop: "mainModule",
        descriptor: originalDescriptor
      });
      const currentValue = originalDescriptor?.value;
      if (currentValue !== void 0) {
        Object.defineProperty(process, "mainModule", {
          get() {
            const message = "process.mainModule access is blocked in worker context";
            const violation = self.recordViolation(
              "process_main_module",
              "process.mainModule",
              message
            );
            if (!auditMode) {
              throw new WorkerSecurityViolationError(message, violation);
            }
            return currentValue;
          },
          set(value) {
            const message = "process.mainModule modification is blocked in worker context";
            const violation = self.recordViolation(
              "process_main_module",
              "process.mainModule",
              message
            );
            if (!auditMode) {
              throw new WorkerSecurityViolationError(message, violation);
            }
            Object.defineProperty(process, "mainModule", {
              value,
              writable: true,
              configurable: true
            });
          },
          configurable: true
        });
      }
    } catch {
    }
  }
  /**
   * Protect process.execPath from being read or set in worker context.
   *
   * process.execPath is a string primitive (not an object), so it cannot be
   * proxied via the normal blocked globals mechanism. We use Object.defineProperty
   * with getter/setter (same pattern as protectProcessMainModule).
   */
  protectProcessExecPath() {
    if (typeof process === "undefined") return;
    const self = this;
    const auditMode = this.config.auditMode;
    try {
      const originalDescriptor = Object.getOwnPropertyDescriptor(
        process,
        "execPath"
      );
      this.originalDescriptors.push({
        target: process,
        prop: "execPath",
        descriptor: originalDescriptor
      });
      const currentValue = originalDescriptor?.value ?? process.execPath;
      Object.defineProperty(process, "execPath", {
        get() {
          const message = "process.execPath access is blocked in worker context";
          const violation = self.recordViolation(
            "process_exec_path",
            "process.execPath",
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return currentValue;
        },
        set(value) {
          const message = "process.execPath modification is blocked in worker context";
          const violation = self.recordViolation(
            "process_exec_path",
            "process.execPath",
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          Object.defineProperty(process, "execPath", {
            value,
            writable: true,
            configurable: true
          });
        },
        configurable: true
      });
    } catch {
    }
  }
  /**
   * Protect process.connected from being read or set in worker context.
   *
   * process.connected is a boolean primitive (not an object), so it cannot be
   * proxied via the normal blocked globals mechanism. We use Object.defineProperty
   * with getter/setter (same pattern as protectProcessExecPath).
   *
   * Only protects if process.connected exists (IPC contexts).
   */
  protectProcessConnected() {
    if (typeof process === "undefined") return;
    if (process.connected === void 0) return;
    const self = this;
    const auditMode = this.config.auditMode;
    try {
      const originalDescriptor = Object.getOwnPropertyDescriptor(
        process,
        "connected"
      );
      this.originalDescriptors.push({
        target: process,
        prop: "connected",
        descriptor: originalDescriptor
      });
      const currentValue = originalDescriptor?.value ?? process.connected;
      Object.defineProperty(process, "connected", {
        get() {
          const message = "process.connected access is blocked in worker context";
          const violation = self.recordViolation(
            "process_connected",
            "process.connected",
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          return currentValue;
        },
        set(value) {
          const message = "process.connected modification is blocked in worker context";
          const violation = self.recordViolation(
            "process_connected",
            "process.connected",
            message
          );
          if (!auditMode) {
            throw new WorkerSecurityViolationError(message, violation);
          }
          Object.defineProperty(process, "connected", {
            value,
            writable: true,
            configurable: true
          });
        },
        configurable: true
      });
    } catch {
    }
  }
  /**
   * Protect Module._load from being called.
   *
   * The attack vector is:
   * ```
   * module.constructor._load('child_process')
   * require.main.constructor._load('vm')
   * ```
   *
   * We access the Module class and replace _load with a blocking proxy.
   */
  protectModuleLoad() {
    this.protectModuleMethod("_load", "module_load");
  }
  /**
   * Protect Module._resolveFilename from being called in worker context.
   *
   * Module._resolveFilename is called for both require() and import() resolution.
   * Blocking it catches file-based import() specifiers.
   *
   * data: and blob: URLs are handled by ESM loader hooks registered
   * in the main thread (DefenseInDepthBox.protectDynamicImport).
   */
  protectModuleResolveFilename() {
    this.protectModuleMethod("_resolveFilename", "module_resolve_filename");
  }
  protectModuleMethod(prop, violationType) {
    const path = `Module.${prop}`;
    const self = this;
    const auditMode = this.config.auditMode;
    try {
      const ModuleClass = nodeModuleClass2;
      const original = ModuleClass?.[prop];
      if (!ModuleClass || typeof original !== "function") {
        throw new Error("node:module Module class or method is unavailable");
      }
      const descriptor = Object.getOwnPropertyDescriptor(ModuleClass, prop);
      if (!descriptor) throw new Error("method descriptor is unavailable");
      if (descriptor.configurable === false && descriptor.writable === false) {
        throw new Error("method is non-configurable and non-writable");
      }
      const wrap = (fn) => (
        // @banned-pattern-ignore: intentional Proxy usage for security blocking
        new self.originalProxy(fn, {
          apply(target, thisArg, args) {
            const message = `${path} is blocked in worker context`;
            const violation = self.recordViolation(
              violationType,
              path,
              message
            );
            if (!auditMode) {
              throw new WorkerSecurityViolationError(message, violation);
            }
            return Reflect.apply(target, thisArg, args);
          }
        })
      );
      const proxy = wrap(original);
      this.originalDescriptors.push({
        target: ModuleClass,
        prop,
        descriptor
      });
      if ("value" in descriptor) {
        Object.defineProperty(ModuleClass, prop, {
          ...descriptor,
          value: proxy
        });
        const installed = Object.getOwnPropertyDescriptor(ModuleClass, prop);
        if (ModuleClass[prop] !== proxy || installed?.value !== proxy) {
          throw new Error("installed patch failed verification");
        }
      } else {
        const originalGet = descriptor.get;
        const originalSet = descriptor.set;
        if (typeof originalSet !== "function") {
          throw new Error(
            "accessor has no setter to install protection through"
          );
        }
        this.moduleAccessorResets.push(() => {
          originalSet.call(ModuleClass, original);
        });
        const guardedDescriptor = {
          configurable: descriptor.configurable,
          enumerable: descriptor.enumerable,
          get: originalGet,
          set: (next) => {
            const message = `${path} modification is blocked in worker context`;
            const violation = self.recordViolation(
              violationType,
              path,
              message
            );
            if (!auditMode) {
              throw new WorkerSecurityViolationError(message, violation);
            }
            const wrapped = typeof next === "function" ? wrap(next) : next;
            const previous = originalGet?.call(ModuleClass);
            try {
              install(wrapped);
            } catch (error) {
              install(previous);
              throw error;
            }
          }
        };
        const install = (value) => {
          try {
            originalSet.call(ModuleClass, value);
          } finally {
            Object.defineProperty(ModuleClass, prop, guardedDescriptor);
          }
          const installed = Object.getOwnPropertyDescriptor(ModuleClass, prop);
          if (installed?.get !== guardedDescriptor.get || installed?.set !== guardedDescriptor.set || originalGet?.call(ModuleClass) !== value || ModuleClass[prop] !== value) {
            throw new Error("installed patch failed accessor verification");
          }
        };
        install(proxy);
      }
    } catch {
      this.patchFailures.push(path);
    }
  }
  /**
   * Apply a single patch to a blocked global.
   */
  applyPatch(blocked) {
    const { target, prop, violationType, strategy } = blocked;
    try {
      const original = target[prop];
      if (original === void 0) {
        return;
      }
      const descriptor = Object.getOwnPropertyDescriptor(target, prop);
      this.originalDescriptors.push({ target, prop, descriptor });
      if (strategy === "freeze") {
        if (typeof original === "object" && original !== null) {
          Object.freeze(original);
          const path = this.getPathForTarget(target, prop);
          const proxy = this.createReadonlyObjectProxy(
            original,
            path,
            violationType
          );
          Object.defineProperty(target, prop, {
            ...descriptor,
            value: proxy
          });
        }
      } else {
        const path = this.getPathForTarget(target, prop);
        const proxy = typeof original === "function" ? this.createBlockingProxy(
          original,
          path,
          violationType
        ) : this.createBlockingObjectProxy(
          original,
          path,
          violationType,
          blocked.allowedKeys
        );
        Object.defineProperty(target, prop, {
          value: proxy,
          writable: true,
          configurable: true
        });
      }
    } catch {
    }
  }
  /**
   * Restore all original values.
   */
  restorePatches() {
    for (let i = this.moduleAccessorResets.length - 1; i >= 0; i--) {
      try {
        this.moduleAccessorResets[i]();
      } catch {
      }
    }
    this.moduleAccessorResets = [];
    for (let i = this.originalDescriptors.length - 1; i >= 0; i--) {
      const { target, prop, descriptor } = this.originalDescriptors[i];
      try {
        if (descriptor) {
          Object.defineProperty(target, prop, descriptor);
        } else {
          delete target[prop];
        }
      } catch {
      }
    }
    this.originalDescriptors = [];
  }
};

// src/security/wasm-callback.ts
function sanitizeUnknownError(error) {
  const message = error instanceof Error ? error.message : String(error);
  return sanitizeErrorMessage(message);
}

// src/commands/sqlite3/worker.ts
var MIN_SQLITE_HEAP_LIMIT = 32 * 1024 * 1024;
var MAX_SQLITE_HEAP_LIMIT = 128 * 1024 * 1024;
var cachedSQL = null;
var defense = null;
function wrapWorkerMessage(protocolToken, message) {
  const wrapped = /* @__PURE__ */ Object.create(null);
  if (!message || typeof message !== "object") {
    wrapped.success = false;
    wrapped.error = "Worker attempted to post non-object message";
    wrapped.protocolToken = protocolToken;
    return wrapped;
  }
  for (const [key, value] of Object.entries(message))
    wrapped[key] = value;
  wrapped.protocolToken = protocolToken;
  return wrapped;
}
function postWorkerMessage(protocolToken, message) {
  try {
    parentPort?.postMessage(wrapWorkerMessage(protocolToken, message));
  } catch (error) {
    console.debug(
      "[sqlite3-worker] failed to post worker message:",
      sanitizeUnknownError(error)
    );
  }
}
async function initializeSqlJs() {
  if (cachedSQL) {
    return cachedSQL;
  }
  cachedSQL = await initSqlJs();
  return cachedSQL;
}
function activateDefense() {
  if (defense) return;
  defense = new WorkerDefenseInDepth({});
}
function stripLeadingNoise(sql) {
  let s = sql;
  for (; ; ) {
    const before = s;
    s = s.replace(/^\s+/, "");
    if (s.startsWith("--")) {
      const nl = s.indexOf("\n");
      s = nl === -1 ? "" : s.slice(nl + 1);
    } else if (s.startsWith("/*")) {
      const end = s.indexOf("*/");
      s = end === -1 ? "" : s.slice(end + 2);
    }
    if (s === before) return s;
  }
}
function isReadOnlyStatement(sql) {
  const s = stripLeadingNoise(sql).toUpperCase();
  if (s.startsWith("SELECT")) return true;
  if (s.startsWith("EXPLAIN")) return true;
  if (s.startsWith("VALUES")) return true;
  return false;
}
function tailAfterFailedPrepare(db, sql) {
  let quote = null;
  let lineComment = false;
  let blockComment = false;
  for (let i = 0; i < sql.length; i++) {
    const char = sql[i];
    const next = sql[i + 1];
    if (lineComment) {
      if (char === "\n") lineComment = false;
      continue;
    }
    if (blockComment) {
      if (char === "*" && next === "/") {
        blockComment = false;
        i++;
      }
      continue;
    }
    if (quote !== null) {
      const closes = quote === "]" ? char === "]" : char === quote;
      if (closes) {
        if (quote !== "]" && next === quote) i++;
        else quote = null;
      }
      continue;
    }
    if (char === "-" && next === "-") {
      lineComment = true;
      i++;
      continue;
    }
    if (char === "/" && next === "*") {
      blockComment = true;
      i++;
      continue;
    }
    if (char === "'" || char === '"' || char === "`") {
      quote = char;
      continue;
    }
    if (char === "[") {
      quote = "]";
      continue;
    }
    if (char !== ";") continue;
    const prefix = sql.slice(0, i + 1);
    try {
      const statement = db.prepare(prefix);
      statement.free();
      return sql.slice(i + 1);
    } catch (error) {
      if (!/incomplete input/i.test(error.message))
        return sql.slice(i + 1);
    }
  }
  return null;
}
function valueByteLength(value) {
  if (value === null || value === void 0) return 4;
  if (value instanceof Uint8Array) return value.byteLength;
  if (typeof value === "string") return Buffer.byteLength(value, "utf8");
  return Buffer.byteLength(String(value), "utf8");
}
function databaseByteLength(db) {
  const pageCount = db.exec("PRAGMA page_count")[0]?.values[0]?.[0];
  const pageSize = db.exec("PRAGMA page_size")[0]?.values[0]?.[0];
  if (typeof pageCount !== "number" || typeof pageSize !== "number") return 0;
  return pageCount * pageSize;
}
async function executeQuery(data) {
  let db;
  try {
    if (data.dbBuffer && data.dbBuffer.byteLength > data.limits.maxDatabaseBytes) {
      throw new Error(
        `database exceeds ${data.limits.maxDatabaseBytes} byte limit`
      );
    }
    const SQL = await initializeSqlJs();
    if (data.dbBuffer) {
      db = new SQL.Database(data.dbBuffer);
    } else {
      db = new SQL.Database();
    }
    const sqliteHeapLimit = Math.min(
      MAX_SQLITE_HEAP_LIMIT,
      Math.max(
        MIN_SQLITE_HEAP_LIMIT,
        (data.dbBuffer?.byteLength ?? 0) + data.limits.maxResultBytes * 2
      )
    );
    db.run(`PRAGMA hard_heap_limit = ${sqliteHeapLimit}`);
    const bootstrapIterator = db.iterateStatements("SELECT 1");
    const bootstrapStatement = bootstrapIterator.next();
    if (!bootstrapStatement.done) {
      bootstrapStatement.value.step();
      bootstrapStatement.value.free();
    }
    activateDefense();
  } catch (e) {
    const message = sanitizeHostErrorMessage(e.message);
    return {
      success: false,
      error: message,
      defenseStats: defense?.getStats()
    };
  }
  const results = [];
  let hasModifications = false;
  let resultRows = 0;
  let resultBytes = 0;
  try {
    let remainingSql = data.sql;
    while (remainingSql.trim().length > 0) {
      let prepared;
      const iterator = db.iterateStatements(remainingSql);
      try {
        const next = iterator.next();
        if (next.done) break;
        prepared = next.value;
        remainingSql = iterator.getRemainingSQL();
      } catch (e) {
        const message = sanitizeHostErrorMessage(e.message);
        results.push({
          type: "error",
          error: message
        });
        if (data.options.bail) break;
        let tail = iterator.getRemainingSQL();
        if (tail === remainingSql)
          tail = tailAfterFailedPrepare(db, remainingSql) ?? remainingSql;
        if (tail === remainingSql) break;
        remainingSql = tail;
        continue;
      }
      const stmt = prepared.getSQL();
      try {
        const columns = prepared.getColumnNames();
        for (const column of columns) {
          resultBytes += Buffer.byteLength(column, "utf8");
        }
        const rows = [];
        while (prepared.step()) {
          if (resultRows >= data.limits.maxResultRows) {
            throw new Error(
              `query result exceeds ${data.limits.maxResultRows} row limit`
            );
          }
          const row = prepared.get();
          let rowBytes = row.length * 8;
          for (const value of row) rowBytes += valueByteLength(value);
          if (resultBytes + rowBytes > data.limits.maxResultBytes) {
            throw new Error(
              `query result exceeds ${data.limits.maxResultBytes} byte limit`
            );
          }
          resultBytes += rowBytes;
          resultRows++;
          rows.push(row);
        }
        results.push({ type: "data", columns, rows });
        if (!isReadOnlyStatement(stmt)) hasModifications = true;
      } catch (e) {
        const message = sanitizeHostErrorMessage(e.message);
        results.push({ type: "error", error: message });
        if (data.options.bail) {
          break;
        }
      } finally {
        prepared.free();
      }
    }
    let resultBuffer = null;
    if (hasModifications) {
      const databaseBytes = databaseByteLength(db);
      if (databaseBytes > data.limits.maxDatabaseBytes) {
        throw new Error(
          `database exceeds ${data.limits.maxDatabaseBytes} byte limit`
        );
      }
      resultBuffer = db.export();
    }
    db.close();
    return {
      success: true,
      results,
      hasModifications,
      dbBuffer: resultBuffer,
      defenseStats: defense?.getStats()
    };
  } catch (e) {
    db.close();
    const message = sanitizeHostErrorMessage(e.message);
    return {
      success: false,
      error: message,
      defenseStats: defense?.getStats()
    };
  }
}
if (parentPort && workerData) {
  const input = workerData;
  executeQuery(input).then((result) => {
    postWorkerMessage(input.protocolToken, result);
  }).catch((error) => {
    postWorkerMessage(input.protocolToken, {
      success: false,
      error: sanitizeUnknownError(error),
      defenseStats: defense?.getStats()
    });
  });
}
