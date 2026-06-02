export const NotificationPlugin = async ({ client }) => {
  return {
    event: async ({ event }) => {
      if (event.type === "session.compacted") {
        try {
          await client.session.prompt({
            path: { id: event.properties.sessionID },
            body: {
              parts: [{
                type: "text",
                text: `🔴 **Conversation Compacted**\n\n` +
                      `You must stop immediately. Notify human partner that a compaction happened and that you have stopped to prevent accidents.\n\n`,
                metadata: {
                  source: "compaction-detector",
                  timestamp: Date.now()
                }
              }]
            }
          });
        } catch (error) {
          console.error("Error handling compaction: " + error.message);
        }
      }
    },
  }
}
