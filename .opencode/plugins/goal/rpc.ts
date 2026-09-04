const Goal = {
  type: "object",
  properties: {
    id: { type: "string" },
    sessionID: { type: "string" },
    objective: { type: "string" },
    status: {
      type: "string",
      enum: ["active", "paused", "blocked", "usageLimited", "budgetLimited", "complete"],
    },
    tokenBudget: { type: "integer" },
    tokensUsed: { type: "integer" },
    timeUsedSeconds: { type: "integer" },
    createdAt: { type: "integer" },
    updatedAt: { type: "integer" },
  },
  required: ["id", "sessionID", "objective", "status", "tokensUsed", "timeUsedSeconds", "createdAt", "updatedAt"],
  additionalProperties: false,
} as const

const SessionID = {
  type: "object",
  properties: { sessionID: { type: "string" } },
  required: ["sessionID"],
  additionalProperties: false,
} as const

export const GoalRpc = {
  id: "goal",
  methods: {
    get: {
      input: SessionID,
      output: {
        type: "object",
        properties: { goal: { anyOf: [Goal, { type: "null" }] } },
        required: ["goal"],
        additionalProperties: false,
      },
    },
    set: {
      input: {
        type: "object",
        properties: {
          sessionID: { type: "string" },
          objective: { type: "string" },
          status: {
            type: "string",
            enum: ["active", "paused", "blocked", "usageLimited", "budgetLimited", "complete"],
          },
          tokenBudget: { type: "integer" },
        },
        required: ["sessionID"],
        additionalProperties: false,
      },
      output: {
        type: "object",
        properties: { goal: Goal },
        required: ["goal"],
        additionalProperties: false,
      },
      errors: {
        invalid: {
          type: "object",
          properties: { message: { type: "string" } },
          required: ["message"],
          additionalProperties: false,
        },
      },
    },
    clear: {
      input: SessionID,
      output: {
        type: "object",
        properties: { cleared: { type: "boolean" } },
        required: ["cleared"],
        additionalProperties: false,
      },
    },
  },
  events: {
    updated: {
      schema: {
        type: "object",
        properties: {
          sessionID: { type: "string" },
          goal: Goal,
        },
        required: ["sessionID", "goal"],
        additionalProperties: false,
      },
    },
    cleared: {
      schema: {
        type: "object",
        properties: { sessionID: { type: "string" } },
        required: ["sessionID"],
        additionalProperties: false,
      },
    },
  },
}
