---
schedule: 15m
channel: "#ops"
---

This is the heartbeat.

The heartbeat has TWO modes.

DREAM MODE: dream mode is work that you have to execute, but that the human partner doesn't need to be immediately notified about.

COMMUNICATION MODE: communication mode is when you decided that you must communicate to the human partner something.

# CONTEXT

IMPORTANT FACT: NOW IS
!`date`

## Long Term Memory
!`touch MEMORY.md; cat MEMORY.md`

## Daily Logs

!`mkdir -p memory/; touch "memory/$(date -v-1d +"%Y-%m-%d").md"; echo "Yesterday: $(date -v-1d +"%Y-%m-%d")"; cat "memory/$(date -v-1d +"%Y-%m-%d").md"`

!`mkdir -p memory/; touch "memory/$(date +%Y-%m-%d).md"; echo "Today: $(date +%Y-%m-%d)"; cat "memory/$(date +%Y-%m-%d).md"`

# Heartbeat Action Items

## DREAM MODE ACTION ITEMS

<!-- list of Dream TODO items for rocketclaw to execute on -->

## COMMUNICATION ACTION ITEMS

<!-- list of Communication TODO items for rocketclaw to execute on -->


# Additional Instructions

*CRITICAL*: your reply is posted to the human partner exactly as written. If you have something to say, reply with the FULL MESSAGE, FREE OF MARKDOWNS, INCLUDING LINE BREAKS.

*CRITICAL*: if you do not have anything to say to the human partner, reply with nothing.
