------------------------ MODULE OpenCodePruneSchedule ------------------------
(* When RocketClaw prunes one OpenCode server's local session copies, and how
   that prune shares time with the server's sync loop (SyncOpenCode in
   internal/rocketclaw/backend/opencode.go).

   The sync loop runs a pass every tick. A prune is one request to the server,
   which answers whenever it likes: as slow as it wants, or never. Its answer
   succeeds or fails. Time is counted in ticks; Period is the prune's day.

   - Separate = FALSE is the code as built: the sync loop prunes at the end of a
     pass when Period has passed since the last prune that succeeded, and waits
     for that prune before its next pass.
   - Separate = TRUE is the change: a loop of its own prunes at once, then waits
     Period after each attempt ends, whatever its outcome.
   - WaitFirst (with Separate) is a loop that waits Period before its first
     prune, as a plain ticker would; not a candidate.

   The sync loop, the prune loop and the clock keep working (weak fairness).
   The server and the shutdown do not have to act. *)
EXTENDS Integers, Sequences

CONSTANTS Period, MaxTime, Separate, WaitFirst

ASSUME Period \in Nat \ {0} /\ MaxTime \in Nat
ASSUME Separate \in BOOLEAN /\ WaitFirst \in BOOLEAN

Attempt == [start: 0..MaxTime, end: -1..MaxTime, ok: BOOLEAN]

(* --algorithm OpenCodePruneSchedule
variables
    now = 0,
    stop = FALSE,
    syncedAt = -1,
    lastPruned = -Period,
    lastEnd = 0,
    requesting = FALSE,
    replied = FALSE,
    attempts = <<>>;

define
    TypeOK ==
        /\ now \in 0..MaxTime
        /\ stop \in BOOLEAN
        /\ syncedAt \in -1..MaxTime
        /\ lastPruned \in -Period..MaxTime
        /\ lastEnd \in 0..MaxTime
        /\ requesting \in BOOLEAN
        /\ replied \in BOOLEAN
        /\ \A i \in 1..Len(attempts) : attempts[i] \in Attempt

    AtMostOncePerPeriod ==
        \A i \in 1..Len(attempts) - 1 : attempts[i + 1].start - attempts[i].start >= Period

    NoBackToBackRetry ==
        \A i \in 1..Len(attempts) - 1 : ~attempts[i].ok => attempts[i + 1].start - attempts[i].end >= Period

    PrunesBeforeWaiting == pc["prune"] = "PruneWait" => Len(attempts) >= 1

    SyncKeepsUp == \A t \in 0..MaxTime : (now = t) ~> (syncedAt >= t \/ stop)

    PruneRepeats == (pc["prune"] = "PruneWait" /\ lastEnd + Period <= MaxTime) ~> (pc["prune"] # "PruneWait")

    ShutdownStops == stop ~> (pc["sync"] = "Done" /\ pc["prune"] = "Done")

    NeverRetriedAfterFailure == \A i \in 1..Len(attempts) - 1 : attempts[i].ok
    NeverSyncsDuringPrune == ~(requesting /\ pc["sync"] = "SyncPass")
end define;

macro begin_attempt() begin
    requesting := TRUE;
    attempts := Append(attempts, [start |-> now, end |-> -1, ok |-> FALSE]);
end macro;

macro end_attempt(ok) begin
    attempts[Len(attempts)] := [attempts[Len(attempts)] EXCEPT !.end = now, !.ok = ok];
    requesting := FALSE;
    replied := FALSE;
    lastEnd := now;
end macro;

fair process sync = "sync"
begin
SyncWait:
    while TRUE do
        await now > syncedAt \/ stop;
        if stop then
            goto Done;
        end if;
SyncPass:
        syncedAt := now;
        if ~Separate /\ now - lastPruned >= Period then
            begin_attempt();
SyncPrune:
            await replied \/ stop;
            if stop then
                goto Done;
            else
                with ok \in BOOLEAN do
                    end_attempt(ok);
                    if ok then
                        lastPruned := now;
                    end if;
                end with;
            end if;
        end if;
    end while;
end process;

fair process prune = "prune"
begin
PruneFirst:
    if ~Separate then
        goto Done;
    elsif WaitFirst then
        lastEnd := now;
        goto PruneWait;
    end if;
PruneStart:
    begin_attempt();
PruneReply:
    await replied \/ stop;
    if stop then
        goto Done;
    else
        with ok \in BOOLEAN do
            end_attempt(ok);
        end with;
    end if;
PruneWait:
    await now >= lastEnd + Period \/ stop;
    if stop then
        goto Done;
    else
        goto PruneStart;
    end if;
end process;

fair process clock = "clock"
begin
Tick:
    while now < MaxTime do
        now := now + 1;
    end while;
end process;

process server = "server"
begin
Answer:
    while TRUE do
        await requesting /\ ~replied;
        replied := TRUE;
    end while;
end process;

process shutdown = "shutdown"
begin
Stop:
    stop := TRUE;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, now, stop, syncedAt, lastPruned, lastEnd, requesting, replied, 
          attempts

(* define statement *)
TypeOK ==
    /\ now \in 0..MaxTime
    /\ stop \in BOOLEAN
    /\ syncedAt \in -1..MaxTime
    /\ lastPruned \in -Period..MaxTime
    /\ lastEnd \in 0..MaxTime
    /\ requesting \in BOOLEAN
    /\ replied \in BOOLEAN
    /\ \A i \in 1..Len(attempts) : attempts[i] \in Attempt

AtMostOncePerPeriod ==
    \A i \in 1..Len(attempts) - 1 : attempts[i + 1].start - attempts[i].start >= Period

NoBackToBackRetry ==
    \A i \in 1..Len(attempts) - 1 : ~attempts[i].ok => attempts[i + 1].start - attempts[i].end >= Period

PrunesBeforeWaiting == pc["prune"] = "PruneWait" => Len(attempts) >= 1

SyncKeepsUp == \A t \in 0..MaxTime : (now = t) ~> (syncedAt >= t \/ stop)

PruneRepeats == (pc["prune"] = "PruneWait" /\ lastEnd + Period <= MaxTime) ~> (pc["prune"] # "PruneWait")

ShutdownStops == stop ~> (pc["sync"] = "Done" /\ pc["prune"] = "Done")

NeverRetriedAfterFailure == \A i \in 1..Len(attempts) - 1 : attempts[i].ok
NeverSyncsDuringPrune == ~(requesting /\ pc["sync"] = "SyncPass")


vars == << pc, now, stop, syncedAt, lastPruned, lastEnd, requesting, replied, 
           attempts >>

ProcSet == {"sync"} \cup {"prune"} \cup {"clock"} \cup {"server"} \cup {"shutdown"}

Init == (* Global variables *)
        /\ now = 0
        /\ stop = FALSE
        /\ syncedAt = -1
        /\ lastPruned = -Period
        /\ lastEnd = 0
        /\ requesting = FALSE
        /\ replied = FALSE
        /\ attempts = <<>>
        /\ pc = [self \in ProcSet |-> CASE self = "sync" -> "SyncWait"
                                        [] self = "prune" -> "PruneFirst"
                                        [] self = "clock" -> "Tick"
                                        [] self = "server" -> "Answer"
                                        [] self = "shutdown" -> "Stop"]

SyncWait == /\ pc["sync"] = "SyncWait"
            /\ now > syncedAt \/ stop
            /\ IF stop
                  THEN /\ pc' = [pc EXCEPT !["sync"] = "Done"]
                  ELSE /\ pc' = [pc EXCEPT !["sync"] = "SyncPass"]
            /\ UNCHANGED << now, stop, syncedAt, lastPruned, lastEnd, 
                            requesting, replied, attempts >>

SyncPass == /\ pc["sync"] = "SyncPass"
            /\ syncedAt' = now
            /\ IF ~Separate /\ now - lastPruned >= Period
                  THEN /\ requesting' = TRUE
                       /\ attempts' = Append(attempts, [start |-> now, end |-> -1, ok |-> FALSE])
                       /\ pc' = [pc EXCEPT !["sync"] = "SyncPrune"]
                  ELSE /\ pc' = [pc EXCEPT !["sync"] = "SyncWait"]
                       /\ UNCHANGED << requesting, attempts >>
            /\ UNCHANGED << now, stop, lastPruned, lastEnd, replied >>

SyncPrune == /\ pc["sync"] = "SyncPrune"
             /\ replied \/ stop
             /\ IF stop
                   THEN /\ pc' = [pc EXCEPT !["sync"] = "Done"]
                        /\ UNCHANGED << lastPruned, lastEnd, requesting, 
                                        replied, attempts >>
                   ELSE /\ \E ok \in BOOLEAN:
                             /\ attempts' = [attempts EXCEPT ![Len(attempts)] = [attempts[Len(attempts)] EXCEPT !.end = now, !.ok = ok]]
                             /\ requesting' = FALSE
                             /\ replied' = FALSE
                             /\ lastEnd' = now
                             /\ IF ok
                                   THEN /\ lastPruned' = now
                                   ELSE /\ TRUE
                                        /\ UNCHANGED lastPruned
                        /\ pc' = [pc EXCEPT !["sync"] = "SyncWait"]
             /\ UNCHANGED << now, stop, syncedAt >>

sync == SyncWait \/ SyncPass \/ SyncPrune

PruneFirst == /\ pc["prune"] = "PruneFirst"
              /\ IF ~Separate
                    THEN /\ pc' = [pc EXCEPT !["prune"] = "Done"]
                         /\ UNCHANGED lastEnd
                    ELSE /\ IF WaitFirst
                               THEN /\ lastEnd' = now
                                    /\ pc' = [pc EXCEPT !["prune"] = "PruneWait"]
                               ELSE /\ pc' = [pc EXCEPT !["prune"] = "PruneStart"]
                                    /\ UNCHANGED lastEnd
              /\ UNCHANGED << now, stop, syncedAt, lastPruned, requesting, 
                              replied, attempts >>

PruneStart == /\ pc["prune"] = "PruneStart"
              /\ requesting' = TRUE
              /\ attempts' = Append(attempts, [start |-> now, end |-> -1, ok |-> FALSE])
              /\ pc' = [pc EXCEPT !["prune"] = "PruneReply"]
              /\ UNCHANGED << now, stop, syncedAt, lastPruned, lastEnd, 
                              replied >>

PruneReply == /\ pc["prune"] = "PruneReply"
              /\ replied \/ stop
              /\ IF stop
                    THEN /\ pc' = [pc EXCEPT !["prune"] = "Done"]
                         /\ UNCHANGED << lastEnd, requesting, replied, 
                                         attempts >>
                    ELSE /\ \E ok \in BOOLEAN:
                              /\ attempts' = [attempts EXCEPT ![Len(attempts)] = [attempts[Len(attempts)] EXCEPT !.end = now, !.ok = ok]]
                              /\ requesting' = FALSE
                              /\ replied' = FALSE
                              /\ lastEnd' = now
                         /\ pc' = [pc EXCEPT !["prune"] = "PruneWait"]
              /\ UNCHANGED << now, stop, syncedAt, lastPruned >>

PruneWait == /\ pc["prune"] = "PruneWait"
             /\ now >= lastEnd + Period \/ stop
             /\ IF stop
                   THEN /\ pc' = [pc EXCEPT !["prune"] = "Done"]
                   ELSE /\ pc' = [pc EXCEPT !["prune"] = "PruneStart"]
             /\ UNCHANGED << now, stop, syncedAt, lastPruned, lastEnd, 
                             requesting, replied, attempts >>

prune == PruneFirst \/ PruneStart \/ PruneReply \/ PruneWait

Tick == /\ pc["clock"] = "Tick"
        /\ IF now < MaxTime
              THEN /\ now' = now + 1
                   /\ pc' = [pc EXCEPT !["clock"] = "Tick"]
              ELSE /\ pc' = [pc EXCEPT !["clock"] = "Done"]
                   /\ now' = now
        /\ UNCHANGED << stop, syncedAt, lastPruned, lastEnd, requesting, 
                        replied, attempts >>

clock == Tick

Answer == /\ pc["server"] = "Answer"
          /\ requesting /\ ~replied
          /\ replied' = TRUE
          /\ pc' = [pc EXCEPT !["server"] = "Answer"]
          /\ UNCHANGED << now, stop, syncedAt, lastPruned, lastEnd, requesting, 
                          attempts >>

server == Answer

Stop == /\ pc["shutdown"] = "Stop"
        /\ stop' = TRUE
        /\ pc' = [pc EXCEPT !["shutdown"] = "Done"]
        /\ UNCHANGED << now, syncedAt, lastPruned, lastEnd, requesting, 
                        replied, attempts >>

shutdown == Stop

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == sync \/ prune \/ clock \/ server \/ shutdown
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(sync)
        /\ WF_vars(prune)
        /\ WF_vars(clock)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION
==============================================================================
