------------------------ MODULE ComposerVoiceReadiness ------------------------
EXTENDS Naturals

CONSTANTS Guarded, MaxClicks

Inputs == {"agents", "identity", "config", "settings"}
Outcomes == {"success", "error"}
Statuses == Outcomes \cup {"pending"}
Initial == [q \in Inputs |-> "pending"]
Speakers == {"none", "server-default", "cove", "cedar"}
VoiceCatalogs == {"empty", "default-only", "offers-saved"}

Pending(snapshot) ==
    snapshot["agents"] = "pending" \/ snapshot["identity"] = "pending" \/
    (snapshot["identity"] = "success" /\ snapshot["settings"] = "pending")

Speaker(snapshot, catalog) ==
    IF snapshot["agents"] # "success" \/ catalog = "empty" THEN "server-default"
    ELSE IF snapshot["settings"] = "success" /\ catalog = "offers-saved" THEN "cedar"
    ELSE "cove"

(* --algorithm ComposerVoiceReadiness
variables
    warm \in BOOLEAN,
    received = IF warm THEN [Initial EXCEPT !["identity"] = "success", !["config"] = "success"] ELSE Initial,
    shown = received,
    composerEnabled = warm,
    active = warm,
    sending \in BOOLEAN,
    catalog \in VoiceCatalogs,
    clicks = 0,
    lastClick = [snapshot |-> Initial, enabled |-> FALSE, wasActive |-> FALSE,
                 effect |-> "none", speaker |-> "none"],
    blockedForPreference = FALSE;

process Query \in Inputs
begin
Settle:
    await received[self] = "pending" /\ (self # "settings" \/ shown["identity"] = "success");
    with outcome \in Outcomes do
        received[self] := outcome;
    end with;
end process;

fair process View = "view"
begin
Commit:
    while TRUE do
        await shown # received;
        shown := received;
    end while;
end process;

process Draft = "draft"
begin
Hydrate:
    await ~composerEnabled /\ shown["identity"] = "success" /\ shown["config"] = "success";
    composerEnabled := TRUE;
end process;

process User = "user"
begin
Click:
    while clicks < MaxClicks do
        with effect = IF ~composerEnabled THEN "ignored"
                      ELSE IF active THEN "hangup"
                      ELSE IF sending \/ (Guarded /\ Pending(shown)) THEN "ignored"
                      ELSE "start" do
            lastClick := [snapshot |-> shown, enabled |-> composerEnabled, wasActive |-> active, effect |-> effect,
                          speaker |-> IF effect = "start" THEN Speaker(shown, catalog) ELSE "none"];
            active := IF effect = "ignored" THEN active ELSE effect = "start";
            clicks := clicks + 1;
            blockedForPreference := blockedForPreference \/
                (composerEnabled /\ effect = "ignored" /\ ~sending /\
                 shown["agents"] = "success" /\ shown["settings"] = "pending");
        end with;
    end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "20ed194c" /\ chksum(tla) = "bf014ceb")
VARIABLES pc, warm, received, shown, composerEnabled, active, sending, 
          catalog, clicks, lastClick, blockedForPreference

vars == << pc, warm, received, shown, composerEnabled, active, sending, 
           catalog, clicks, lastClick, blockedForPreference >>

ProcSet == (Inputs) \cup {"view"} \cup {"draft"} \cup {"user"}

Init == (* Global variables *)
        /\ warm \in BOOLEAN
        /\ received = IF warm THEN [Initial EXCEPT !["identity"] = "success", !["config"] = "success"] ELSE Initial
        /\ shown = received
        /\ composerEnabled = warm
        /\ active = warm
        /\ sending \in BOOLEAN
        /\ catalog \in VoiceCatalogs
        /\ clicks = 0
        /\ lastClick = [snapshot |-> Initial, enabled |-> FALSE, wasActive |-> FALSE,
                        effect |-> "none", speaker |-> "none"]
        /\ blockedForPreference = FALSE
        /\ pc = [self \in ProcSet |-> CASE self \in Inputs -> "Settle"
                                        [] self = "view" -> "Commit"
                                        [] self = "draft" -> "Hydrate"
                                        [] self = "user" -> "Click"]

Settle(self) == /\ pc[self] = "Settle"
                /\ received[self] = "pending" /\ (self # "settings" \/ shown["identity"] = "success")
                /\ \E outcome \in Outcomes:
                     received' = [received EXCEPT ![self] = outcome]
                /\ pc' = [pc EXCEPT ![self] = "Done"]
                /\ UNCHANGED << warm, shown, composerEnabled, active, sending, 
                                catalog, clicks, lastClick, 
                                blockedForPreference >>

Query(self) == Settle(self)

Commit == /\ pc["view"] = "Commit"
          /\ shown # received
          /\ shown' = received
          /\ pc' = [pc EXCEPT !["view"] = "Commit"]
          /\ UNCHANGED << warm, received, composerEnabled, active, sending, 
                          catalog, clicks, lastClick, blockedForPreference >>

View == Commit

Hydrate == /\ pc["draft"] = "Hydrate"
           /\ ~composerEnabled /\ shown["identity"] = "success" /\ shown["config"] = "success"
           /\ composerEnabled' = TRUE
           /\ pc' = [pc EXCEPT !["draft"] = "Done"]
           /\ UNCHANGED << warm, received, shown, active, sending, catalog, 
                           clicks, lastClick, blockedForPreference >>

Draft == Hydrate

Click == /\ pc["user"] = "Click"
         /\ IF clicks < MaxClicks
               THEN /\ LET effect == IF ~composerEnabled THEN "ignored"
                                     ELSE IF active THEN "hangup"
                                     ELSE IF sending \/ (Guarded /\ Pending(shown)) THEN "ignored"
                                     ELSE "start" IN
                         /\ lastClick' = [snapshot |-> shown, enabled |-> composerEnabled, wasActive |-> active, effect |-> effect,
                                          speaker |-> IF effect = "start" THEN Speaker(shown, catalog) ELSE "none"]
                         /\ active' = (IF effect = "ignored" THEN active ELSE effect = "start")
                         /\ clicks' = clicks + 1
                         /\ blockedForPreference' = (                    blockedForPreference \/
                                                     (composerEnabled /\ effect = "ignored" /\ ~sending /\
                                                      shown["agents"] = "success" /\ shown["settings"] = "pending"))
                    /\ pc' = [pc EXCEPT !["user"] = "Click"]
               ELSE /\ pc' = [pc EXCEPT !["user"] = "Done"]
                    /\ UNCHANGED << active, clicks, lastClick, 
                                    blockedForPreference >>
         /\ UNCHANGED << warm, received, shown, composerEnabled, sending, 
                         catalog >>

User == Click

Next == View \/ Draft \/ User
           \/ (\E self \in Inputs: Query(self))

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(View)

\* END TRANSLATION 

TypeOK ==
    /\ received \in [Inputs -> Statuses]
    /\ shown \in [Inputs -> Statuses]
    /\ warm \in BOOLEAN /\ composerEnabled \in BOOLEAN
    /\ active \in BOOLEAN /\ sending \in BOOLEAN /\ catalog \in VoiceCatalogs
    /\ clicks \in 0..MaxClicks
    /\ blockedForPreference \in BOOLEAN
    /\ lastClick \in [snapshot: [Inputs -> Statuses], enabled: BOOLEAN, wasActive: BOOLEAN,
                     effect: {"none", "hangup", "ignored", "start"}, speaker: Speakers]

SettingsNeedsIdentity ==
    received["settings"] \in Outcomes => shown["identity"] = "success"

ComposerNeedsScope ==
    composerEnabled => shown["identity"] = "success" /\ shown["config"] = "success"

DisabledComposerIgnoresClick ==
    clicks > 0 /\ ~lastClick.enabled => lastClick.effect = "ignored"

ColdIdentityErrorCannotStart ==
    ~warm /\ lastClick.snapshot["identity"] = "error" => lastClick.effect = "ignored"

NoStartWithUnresolvedInputs ==
    lastClick.effect = "start" =>
        /\ lastClick.enabled
        /\ lastClick.snapshot["agents"] \in Outcomes
        /\ lastClick.snapshot["settings"] \in Outcomes

ReadyClickStarts ==
    lastClick.enabled /\ ~lastClick.wasActive /\ ~sending /\
    lastClick.snapshot["agents"] \in Outcomes /\ lastClick.snapshot["settings"] \in Outcomes
    => lastClick.effect = "start"

ActiveClickHangsUp == lastClick.enabled /\ lastClick.wasActive => lastClick.effect = "hangup" /\ ~active

SendingBlocksFreshStart ==
    clicks > 0 /\ ~lastClick.wasActive /\ sending => lastClick.effect = "ignored"

StartUsesVoice ==
    lastClick.effect = "start" =>
        IF lastClick.snapshot["agents"] = "error" \/ catalog = "empty" THEN lastClick.speaker = "server-default"
        ELSE IF lastClick.snapshot["settings"] = "success" /\ catalog = "offers-saved" THEN lastClick.speaker = "cedar"
        ELSE lastClick.speaker = "cove"

NoDefaultWhilePreferencePending ==
    ~(~warm /\ catalog = "offers-saved" /\ lastClick.speaker = "cove" /\
      lastClick.snapshot["identity"] = "success" /\ lastClick.snapshot["settings"] = "pending")

RenderedInputsCatchUp == <>[](shown = received)

NoPreferredStartAfterBlockedClick == ~(~warm /\ blockedForPreference /\ lastClick.speaker = "cedar")

NoStartAfterSettingsError ==
    ~(~warm /\ lastClick.speaker = "cove" /\ lastClick.snapshot["settings"] = "error")

NoBlockedClickAfterIdentityError ==
    ~(~warm /\ ~sending /\ lastClick.effect = "ignored" /\ ~lastClick.enabled /\
      lastClick.snapshot["identity"] = "error" /\ lastClick.snapshot["settings"] = "pending" /\
      lastClick.snapshot["agents"] = "success" /\ lastClick.snapshot["config"] = "success")

NoStartAfterAgentError ==
    ~(~warm /\ lastClick.speaker = "server-default" /\ lastClick.snapshot["agents"] = "error" /\
      lastClick.snapshot["settings"] = "success")

NoStartWithEmptyVoiceCatalog ==
    ~(~warm /\ catalog = "empty" /\ lastClick.speaker = "server-default" /\
      lastClick.snapshot = [q \in Inputs |-> "success"])

NoHangupWhilePending ==
    ~(lastClick.enabled /\ lastClick.effect = "hangup" /\ sending /\
      lastClick.snapshot["settings"] = "pending" /\ lastClick.snapshot["agents"] = "pending")

=============================================================================
