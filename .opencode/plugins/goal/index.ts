import { Plugin } from "@opencode-ai/plugin"
import {
  card,
  decodeGoal,
  editedStatus,
  parseCommand,
  publicGoal,
  remainingTokens,
  tokenTotal,
  unfinished,
  usageLimitedError,
  USAGE,
  USAGE_HINT,
  validateBudget,
  validateObjective,
  type Goal,
  type PublicGoal,
  type Status,
} from "./goal.js"
import { budgetLimit, continuation, objectiveUpdated } from "./prompts.js"
import { GoalRpc } from "./rpc.js"

export default Plugin.define({
  id: "goal",
  async setup(ctx) {
    const maxTokenBudget =
      typeof ctx.options.maxTokenBudget === "number" && ctx.options.maxTokenBudget > 0
        ? Math.floor(ctx.options.maxTokenBudget)
        : undefined
    const controller = new AbortController()
    const running = new Set<string>()
    const continuing = new Set<string>()
    const chains = new Map<string, Promise<unknown>>()

    const serialize = <T>(sessionID: string, fn: () => Promise<T>) => {
      const next = (chains.get(sessionID) ?? Promise.resolve()).then(fn, fn)
      chains.set(sessionID, next)
      return next
    }

    const load = async (sessionID: string) => decodeGoal(await ctx.storage.get(key(sessionID)))

    const save = async (goal: Goal) => {
      await ctx.storage.set(key(goal.sessionID), stored(goal))
      await rpc.events.emit("updated", { sessionID: goal.sessionID, goal: publicGoal(goal) })
      return goal
    }

    const snapshot = async (sessionID: string) => {
      const session = await ctx.session.get({ sessionID })
      return { session, tokens: tokenTotal(session.tokens), now: Date.now() }
    }

    const account = async (sessionID: string) => {
      const goal = await load(sessionID)
      if (!goal) return undefined
      const current = await snapshot(sessionID)
      const tokensUsed = goal.tokensUsed + Math.max(0, current.tokens - goal.accountedTokens)
      const timeUsedSeconds =
        goal.status === "active"
          ? goal.timeUsedSeconds + Math.max(0, Math.floor((current.now - goal.accountedAt) / 1000))
          : goal.timeUsedSeconds
      const overBudget = goal.tokenBudget !== undefined && tokensUsed >= goal.tokenBudget
      const status = goal.status === "active" && overBudget ? "budgetLimited" : goal.status
      if (
        tokensUsed === goal.tokensUsed &&
        timeUsedSeconds === goal.timeUsedSeconds &&
        status === goal.status &&
        current.tokens === goal.accountedTokens
      ) {
        return goal
      }
      return save({
        ...goal,
        status,
        tokensUsed,
        timeUsedSeconds,
        accountedTokens: current.tokens,
        accountedAt: current.now,
        updatedAt: current.now,
      })
    }

    const create = async (sessionID: string, objective: string, tokenBudget?: number) => {
      const error = validateObjective(objective) ?? validateBudget(tokenBudget, maxTokenBudget)
      if (error) throw new Error(error)
      const current = await snapshot(sessionID)
      const now = current.now
      return save({
        id: crypto.randomUUID(),
        sessionID,
        objective,
        status: "active",
        ...(tokenBudget === undefined ? {} : { tokenBudget }),
        tokensUsed: 0,
        timeUsedSeconds: 0,
        createdAt: now,
        updatedAt: now,
        accountedTokens: current.tokens,
        accountedAt: now,
        budgetReported: false,
      })
    }

    const update = async (sessionID: string, patch: Partial<Pick<Goal, "objective" | "status" | "tokenBudget">>) => {
      const goal = await account(sessionID)
      if (!goal) throw new Error("cannot update goal because this session has no goal")
      if (patch.objective !== undefined) {
        const error = validateObjective(patch.objective)
        if (error) throw new Error(error)
      }
      if (patch.tokenBudget !== undefined) {
        const error = validateBudget(patch.tokenBudget, maxTokenBudget)
        if (error) throw new Error(error)
      }
      const now = Date.now()
      return save({
        ...goal,
        ...(patch.objective === undefined ? {} : { objective: patch.objective }),
        ...(patch.status === undefined ? {} : { status: patch.status }),
        ...(patch.tokenBudget === undefined ? {} : { tokenBudget: patch.tokenBudget }),
        accountedAt: patch.status === "active" && goal.status !== "active" ? now : goal.accountedAt,
        updatedAt: now,
      })
    }

    const clear = async (sessionID: string) => {
      const goal = await load(sessionID)
      if (!goal) return false
      await ctx.storage.remove(key(sessionID))
      await rpc.events.emit("cleared", { sessionID })
      return true
    }

    const parent = async (sessionID: string) => {
      const session = await ctx.session.get({ sessionID })
      return session.parentID === undefined
    }

    const notify = async (sessionID: string, text: string) => {
      await ctx.session.synthetic({ sessionID, text, description: text, resume: false })
    }

    const steer = async (sessionID: string, text: string) => {
      continuing.add(sessionID)
      try {
        await ctx.session.synthetic({ sessionID, text })
      } catch {
        continuing.delete(sessionID)
      }
    }

    const continueIfIdle = async (sessionID: string) => {
      if (continuing.has(sessionID) || running.has(sessionID)) return
      if (!(await parent(sessionID))) return
      const goal = await account(sessionID)
      if (!goal) return
      if (goal.status === "budgetLimited" && !goal.budgetReported) {
        await save({ ...goal, budgetReported: true, updatedAt: Date.now() })
        await steer(sessionID, budgetLimit(goal))
        return
      }
      if (goal.status !== "active") return
      await steer(sessionID, continuation(goal))
    }

    const respond = (goal: Goal | undefined, reportComplete = false) => {
      const publicValue = goal ? publicGoal(goal) : null
      return {
        content: JSON.stringify({
          goal: publicValue,
          remainingTokens: goal ? remainingTokens(goal) : null,
          completionBudgetReport:
            reportComplete && goal && (goal.tokenBudget !== undefined || goal.timeUsedSeconds > 0)
              ? "Goal achieved. Report final usage from this tool result's structured goal fields. If `goal.tokenBudget` is present, include token usage from `goal.tokensUsed` and `goal.tokenBudget`. If `goal.timeUsedSeconds` is greater than 0, summarize elapsed time in a concise, human-friendly form appropriate to the response language."
              : null,
        }),
      }
    }

    const rpc = await ctx.rpc.register(GoalRpc, {
      get: async (input) => ({ goal: publicMaybe(await load(sessionIDOf(input))) }),
      set: async (input, call) => {
        const sessionID = sessionIDOf(input)
        if (!(await parent(sessionID))) {
          return call.error("invalid", "Child sessions do not support goals", { message: "Child sessions do not support goals" })
        }
        return serialize(sessionID, async () => {
          const objective = optionalString(input, "objective")
          const status = optionalStatus(input)
          const tokenBudget = optionalInteger(input, "tokenBudget") ?? maxTokenBudget
          try {
            if (objective !== undefined && status === undefined) {
              const existing = await load(sessionID)
              if (existing && unfinished(existing.status)) await clear(sessionID)
              const goal = await create(sessionID, objective, tokenBudget)
              await continueIfIdle(sessionID)
              return { goal: publicGoal(goal) }
            }
            if (objective !== undefined) {
              const existing = await load(sessionID)
              if (!existing) {
                const goal = await create(sessionID, objective, tokenBudget)
                await continueIfIdle(sessionID)
                return { goal: publicGoal(goal) }
              }
              const goal = await update(sessionID, {
                objective,
                status: status ?? editedStatus(existing.status),
              })
              if (running.has(sessionID) && goal.status === "active" && existing.objective !== goal.objective) {
                await ctx.session.synthetic({ sessionID, text: objectiveUpdated(goal) })
              }
              if (goal.status === "active") await continueIfIdle(sessionID)
              return { goal: publicGoal(goal) }
            }
            if (status === undefined) {
              return call.error("invalid", "Provide an objective or status", { message: "Provide an objective or status" })
            }
            const goal = await update(sessionID, { status })
            if (goal.status === "active") await continueIfIdle(sessionID)
            return { goal: publicGoal(goal) }
          } catch (error) {
            const message = error instanceof Error ? error.message : String(error)
            return call.error("invalid", message, { message })
          }
        })
      },
      clear: async (input) => {
        const sessionID = sessionIDOf(input)
        if (!(await parent(sessionID))) return { cleared: false }
        return serialize(sessionID, async () => ({ cleared: await clear(sessionID) }))
      },
    })

    await ctx.command.transform((editor) => {
      editor.add({
        name: "goal",
        description: "Set a persistent session objective and keep working until it is complete",
        execute: async (input) => {
          const sessionID = input.sessionID
          if (!(await parent(sessionID))) {
            await notify(sessionID, `${USAGE} Goals are only available on top-level sessions.`)
            return
          }
          await serialize(sessionID, async () => {
            const command = parseCommand(input.prompt.text)
            if (command.type === "summary") {
              const goal = await account(sessionID)
              await notify(sessionID, goal ? card(publicGoal(goal)) : `${USAGE}\nNo goal is currently set.\n${USAGE_HINT}`)
              return
            }
            if (command.type === "clear") {
              await account(sessionID)
              await notify(sessionID, (await clear(sessionID)) ? "Goal cleared." : "No goal is currently set.")
              return
            }
            if (command.type === "edit") {
              const goal = await account(sessionID)
              await notify(
                sessionID,
                goal
                  ? `Current objective: ${goal.objective}\nSubmit /goal <new objective> to replace it, or use Edit session goal from the command palette.`
                  : `${USAGE}\nNo goal is currently set.`,
              )
              return
            }
            if (command.type === "pause" || command.type === "resume") {
              const goal = await load(sessionID)
              if (!goal) {
                await notify(sessionID, `${USAGE}\nNo goal is currently set.`)
                return
              }
              const next = command.type === "pause" ? "paused" : "active"
              await update(sessionID, { status: next })
              await notify(sessionID, next === "paused" ? "Goal paused." : "Goal resumed.")
              if (next === "active") await continueIfIdle(sessionID)
              return
            }
            const existing = await load(sessionID)
            if (existing && unfinished(existing.status)) await clear(sessionID)
            await create(sessionID, command.objective, maxTokenBudget)
            if (existing?.objective && existing.objective !== command.objective && running.has(sessionID)) {
              const goal = await load(sessionID)
              if (goal) await ctx.session.synthetic({ sessionID, text: objectiveUpdated(goal) })
            }
            await continueIfIdle(sessionID)
          })
        },
      })
    })

    await ctx.tool.transform((editor) => {
      editor.add({
        name: "get_goal",
        description:
          "Get the current goal for this session, including status, budgets, token and elapsed-time usage, and remaining token budget.",
        input: { type: "object", properties: {}, additionalProperties: false },
        execute: async (_input, tool) => respond(await account(tool.sessionID)),
      })
      editor.add({
        name: "create_goal",
        description: `Create a goal only when explicitly requested by the user or system/developer instructions; do not infer goals from ordinary tasks.
Set token_budget only when an explicit token budget is requested. Fails if an unfinished goal exists; use update_goal only for status.`,
        input: {
          type: "object",
          properties: {
            objective: {
              type: "string",
              description:
                "Required. The concrete objective to start pursuing. This starts a new active goal when no goal exists or replaces the current goal when it is complete.",
            },
            token_budget: {
              type: "integer",
              description: "Positive token budget for the new goal. Omit unless explicitly requested.",
            },
          },
          required: ["objective"],
          additionalProperties: false,
        },
        execute: async (input, tool) => {
          const args = createArgs(input)
          if (!args) return { content: "objective is required" }
          if (!(await parent(tool.sessionID))) return { content: "Child sessions do not support goals" }
          return serialize(tool.sessionID, async () => {
            const existing = await load(tool.sessionID)
            if (existing && unfinished(existing.status)) {
              return {
                content:
                  "cannot create a new goal because this session has an unfinished goal; complete the existing goal first",
              }
            }
            try {
              const goal = await create(tool.sessionID, args.objective, args.tokenBudget ?? maxTokenBudget)
              return respond(goal)
            } catch (error) {
              return { content: error instanceof Error ? error.message : String(error) }
            }
          })
        },
      })
      editor.add({
        name: "update_goal",
        description: `Update the existing goal.
Use this tool only to mark the goal achieved or genuinely blocked.
Set status to \`complete\` only when the objective has actually been achieved and no required work remains.
Set status to \`blocked\` only when the same blocking condition has repeated for at least three consecutive goal turns, counting the original/user-triggered turn and any automatic continuations, and the agent cannot make meaningful progress without user input or an external-state change.
If the user resumes a goal that was previously marked \`blocked\`, treat the resumed run as a fresh blocked audit. If the same blocking condition then repeats for at least three consecutive resumed goal turns, set status to \`blocked\` again.
Once the blocked threshold is satisfied, do not keep reporting that you are still blocked while leaving the goal active; set status to \`blocked\`.
Do not use \`blocked\` merely because the work is hard, slow, uncertain, incomplete, or would benefit from clarification.
Do not mark a goal complete merely because its budget is nearly exhausted or because you are stopping work.
You cannot use this tool to pause, resume, budget-limit, or usage-limit a goal; those status changes are controlled by the user or system.
When marking a budgeted goal achieved with status \`complete\`, report the final token usage from the tool result to the user.`,
        input: {
          type: "object",
          properties: {
            status: {
              type: "string",
              enum: ["complete", "blocked"],
              description:
                "Required. Set to `complete` only when the objective is achieved and no required work remains. Set to `blocked` only after the same blocking condition has recurred for at least three consecutive goal turns and the agent is at an impasse. After a previously blocked goal is resumed, the resumed run starts a fresh blocked audit.",
            },
          },
          required: ["status"],
          additionalProperties: false,
        },
        execute: async (input, tool) => {
          const status = updateArgs(input)
          if (!status) {
            return {
              content:
                "update_goal can only mark the existing goal complete or blocked; pause, resume, budget-limited, and usage-limited status changes are controlled by the user or system",
            }
          }
          return serialize(tool.sessionID, async () => {
            try {
              const goal = await update(tool.sessionID, { status })
              return respond(goal, status === "complete")
            } catch (error) {
              return { content: error instanceof Error ? error.message : String(error) }
            }
          })
        },
      })
    })

    void (async () => {
      for await (const event of ctx.event.subscribe({ signal: controller.signal })) {
        if (event.type === "session.execution.started") {
          continuing.delete(event.data.sessionID)
          running.add(event.data.sessionID)
          continue
        }
        if (event.type === "session.deleted") {
          running.delete(event.data.sessionID)
          continuing.delete(event.data.sessionID)
          await ctx.storage.remove(key(event.data.sessionID)).catch(() => undefined)
          continue
        }
        if (
          event.type !== "session.execution.succeeded" &&
          event.type !== "session.execution.failed" &&
          event.type !== "session.execution.interrupted"
        ) {
          continue
        }
        const sessionID = event.data.sessionID
        running.delete(sessionID)
        continuing.delete(sessionID)
        await serialize(sessionID, async () => {
          if (event.type === "session.execution.interrupted" && event.data.reason === "user") {
            const goal = await account(sessionID)
            if (goal?.status === "active") await update(sessionID, { status: "paused" })
            return
          }
          if (event.type === "session.execution.failed") {
            const goal = await account(sessionID)
            if (goal?.status !== "active" && goal?.status !== "budgetLimited") return
            await update(sessionID, { status: usageLimitedError(event.data.error) ? "usageLimited" : "blocked" })
            return
          }
          if (event.type === "session.execution.interrupted") return
          await continueIfIdle(sessionID)
        }).catch(() => undefined)
      }
    })()

    return () => controller.abort()
  },
})

function key(sessionID: string) {
  return `session/${sessionID}`
}

function stored(goal: Goal) {
  const value: { [key: string]: string | number | boolean } = {
    id: goal.id,
    sessionID: goal.sessionID,
    objective: goal.objective,
    status: goal.status,
    tokensUsed: goal.tokensUsed,
    timeUsedSeconds: goal.timeUsedSeconds,
    createdAt: goal.createdAt,
    updatedAt: goal.updatedAt,
    accountedTokens: goal.accountedTokens,
    accountedAt: goal.accountedAt,
    budgetReported: goal.budgetReported,
  }
  if (goal.tokenBudget !== undefined) value.tokenBudget = goal.tokenBudget
  return value
}

function field(input: unknown, name: string) {
  if (!input || typeof input !== "object") return undefined
  return Object.getOwnPropertyDescriptor(input, name)?.value
}

function sessionIDOf(input: unknown) {
  const value = field(input, "sessionID")
  if (typeof value !== "string") throw new Error("sessionID is required")
  return value
}

function optionalString(input: unknown, name: string) {
  const value = field(input, name)
  return typeof value === "string" ? value.trim() : undefined
}

function optionalInteger(input: unknown, name: string) {
  const value = field(input, name)
  return typeof value === "number" && Number.isInteger(value) ? value : undefined
}

function optionalStatus(input: unknown): Status | undefined {
  const value = optionalString(input, "status")
  if (
    value === "active" ||
    value === "paused" ||
    value === "blocked" ||
    value === "usageLimited" ||
    value === "budgetLimited" ||
    value === "complete"
  ) {
    return value
  }
  return undefined
}

function publicMaybe(goal: Goal | undefined): PublicGoal | null {
  return goal ? publicGoal(goal) : null
}

function createArgs(input: unknown) {
  const objective = field(input, "objective")
  if (typeof objective !== "string") return undefined
  const tokenBudget = field(input, "token_budget")
  return {
    objective: objective.trim(),
    tokenBudget: typeof tokenBudget === "number" && Number.isInteger(tokenBudget) ? tokenBudget : undefined,
  }
}

function updateArgs(input: unknown) {
  const status = field(input, "status")
  if (status === "complete" || status === "blocked") return status
  return undefined
}
