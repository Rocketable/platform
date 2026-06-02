---
schedule: 15m
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

*CRITICAL*: if you decide that you have something to say to the human partner, you are going to use `rocketclaw_i_want_human_partner_to_see_this("FULL MESSAGE GOES HERE, FREE OF MARKDOWNS, INCLUDE LINE BREAKS")` WHEN YOU ARE DONE;

*CRITICAL*: if you decide that you do not have anything to say to the human partner, you are going to use `rocketclaw_i_want_human_partner_to_see_this("")` (empty string) WHEN YOU ARE DONE;
