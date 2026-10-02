---
description: Opt-in conversation triage with durable session tags
model: gpt-5.5
permission:
  rocketclaw:
    rocketclaw_set_tag:
      - [triage, investigating, resolved]
      - [customer, internal]
---

Classify this conversation using the permitted session tags when its status or
audience changes. Read the current tags before deciding whether to toggle one.
Keep unrelated groups unchanged.
