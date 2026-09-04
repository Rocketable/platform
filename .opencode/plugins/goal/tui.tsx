import { Plugin } from "@opencode-ai/plugin/tui"
import { createEffect, Show } from "solid-js"
import { decodePublicGoal, editedStatus, footerLabel, type PublicGoal, type Status } from "./goal.js"
import { GoalRpc } from "./rpc.js"

export default Plugin.define({
  id: "goal.tui",
  setup(context) {
    const goal = context.client.rpc(GoalRpc)
    const [goals, setGoals] = context.storage.memory("goals", {
      initial: {} as Record<string, PublicGoal | null>,
    })

    const remember = (sessionID: string, value: PublicGoal | null) => {
      setGoals((draft) => {
        draft[sessionID] = value
      })
    }

    const stopUpdated = goal.events.on("updated", (event) => {
      const sessionID = eventSessionID(event.data)
      const current = decodePublicGoal(eventGoal(event.data))
      if (sessionID && current) remember(sessionID, current)
    })
    const stopCleared = goal.events.on("cleared", (event) => {
      const sessionID = eventSessionID(event.data)
      if (sessionID) remember(sessionID, null)
    })

    context.ui.slot({
      prepend: "prompt.footer.status",
      render: (input) => <GoalStatus context={context} sessionID={input.sessionID} />,
    })

    context.ui.slot({
      append: "app",
      render() {
        context.keymap.layer(() => {
          const route = context.ui.router.current()
          const sessionID = route.type === "session" ? route.sessionID : undefined
          const current = sessionID ? goals[sessionID] : undefined
          return {
            mode: "global",
            commands: [
              {
                id: "goal.edit",
                title: "Edit session goal",
                group: "Session",
                palette: true,
                enabled: () => Boolean(sessionID && current),
                async run() {
                  if (!sessionID || !current) return
                  const objective = await context.ui.dialog.prompt({
                    title: "Edit goal",
                    description: "Type a goal objective and press Enter",
                    value: current.objective,
                  })
                  if (!objective?.trim()) return
                  try {
                    await goal.set({
                      sessionID,
                      objective: objective.trim(),
                      status: editedStatus(current.status),
                    })
                  } catch (error) {
                    context.ui.toast.show({
                      title: "Goal",
                      message: errorMessage(error),
                      variant: "error",
                    })
                  }
                },
              },
            ],
          }
        })
        return null
      },
    })

    return () => {
      stopUpdated()
      stopCleared()
    }
  },
})

function GoalStatus(props: { context: Plugin.Context; sessionID?: string }) {
  const goal = props.context.client.rpc(GoalRpc)
  const [goals, setGoals] = props.context.storage.memory("goals", {
    initial: {} as Record<string, PublicGoal | null>,
  })

  createEffect(() => {
    const sessionID = props.sessionID
    if (!sessionID) return
    void goal
      .get({ sessionID })
      .then((result) => {
        const current = decodeGet(result)
        if (current === undefined) return
        setGoals((draft) => {
          draft[sessionID] = current
        })
      })
      .catch(() => undefined)
  })

  const current = () => (props.sessionID ? goals[props.sessionID] : undefined)
  return (
    <Show when={current()}>
      {(value) => (
        <text fg={statusColor(props.context, value().status)} wrapMode="none">
          {footerLabel(value().status)}
        </text>
      )}
    </Show>
  )
}

function statusColor(context: Plugin.Context, status: Status) {
  if (status === "complete") return context.theme.text.feedback.success.default
  if (status === "active") return context.theme.text.subdued
  return context.theme.text.feedback.warning.default
}

function decodeGet(value: unknown) {
  const goal = eventGoal(value)
  if (goal === null) return null
  return decodePublicGoal(goal)
}

function eventSessionID(value: unknown) {
  if (!value || typeof value !== "object") return undefined
  const sessionID = Object.getOwnPropertyDescriptor(value, "sessionID")?.value
  return typeof sessionID === "string" ? sessionID : undefined
}

function eventGoal(value: unknown) {
  if (!value || typeof value !== "object") return undefined
  return Object.getOwnPropertyDescriptor(value, "goal")?.value
}

function errorMessage(error: unknown) {
  if (error instanceof Error) return error.message
  if (error && typeof error === "object" && "message" in error && typeof error.message === "string") {
    return error.message
  }
  return String(error)
}
