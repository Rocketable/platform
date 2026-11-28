import { ClientError } from "@opencode/client"
import { Predicate } from "effect"

const gatewayStatuses = new Set([502, 503, 504])

export function unreachable(error: unknown) {
  if (!(error instanceof ClientError)) return false

  if (error.reason === "Transport") return true

  return (
    error.reason === "UnexpectedStatus" &&
    Predicate.hasProperty(error.cause, "status") &&
    gatewayStatuses.has(Number(error.cause.status))
  )
}
