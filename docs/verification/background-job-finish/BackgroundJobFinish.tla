------------------------- MODULE BackgroundJobFinish -------------------------
EXTENDS Naturals

CONSTANTS MaxFails, RetryForever, StopRetryOnShutdown, SaveCleanWhenAbandoned,
          SaveCancelled, WakeWhileStopping

ASSUME MaxFails \in Nat
ASSUME {RetryForever, StopRetryOnShutdown, SaveCleanWhenAbandoned, SaveCancelled, WakeWhileStopping} \subseteq BOOLEAN

(* --algorithm BackgroundJobFinish
variables
    fails \in 0..MaxFails,
    dbForever \in BOOLEAN,
    attempts = 0,
    outcome = "running",
    row = "running",
    note = "none",
    woken = FALSE,
    delivered = 0,
    gaveUp = FALSE,
    wokeWhileStopping = FALSE,
    stopping = FALSE,
    stopped = FALSE,
    restarted = FALSE;

define
    DbRefuses == dbForever \/ fails > 0

    TypeOK ==
        /\ outcome \in {"running", "ok", "cancelled"}
        /\ row \in {"running", "completed", "failed", "killed"}
        /\ note \in {"none", "pending", "delivered"}
        /\ delivered \in 0..2
        /\ attempts \in 0..(MaxFails + 1)

    ExactlyOnce == delivered <= 1

    FinishedNeverKilled == outcome = "ok" /\ row = "killed" => gaveUp

    CancelledNeverCompleted == outcome = "cancelled" => row # "completed"

    NoWakeWhileStopping == ~wokeWhileStopping

    SavedOnceDbWorks == outcome = "ok" /\ ~dbForever ~> row = "completed" \/ stopping

    SavedNoteDelivered == row \in {"completed", "failed"} ~> delivered = 1

    ShutdownNotBlocked == stopping ~> stopped

    NeverSavedAfterRefusals == ~(row = "completed" /\ attempts > 1)

    NeverGaveUpOnShutdown == ~gaveUp
end define;

fair process Worker = "worker"
begin
Work:
    either
        outcome := "ok";
    or
        await stopping;
        outcome := "cancelled";
    end either;
Decide:
    if outcome = "cancelled" /\ ~SaveCancelled then
        goto Done;
    elsif outcome = "ok" /\ stopping /\ ~SaveCleanWhenAbandoned then
        goto Done;
    end if;
Save:
    attempts := IF attempts > MaxFails THEN attempts ELSE attempts + 1;
    if DbRefuses then
        if fails > 0 then
            fails := fails - 1;
        end if;
        goto Retry;
    else
        row := "completed";
        note := "pending";
        goto Wake;
    end if;
Retry:
    if stopping /\ StopRetryOnShutdown then
        gaveUp := TRUE;
        goto Done;
    elsif ~RetryForever /\ attempts >= MaxFails then
        goto Done;
    else
        goto Save;
    end if;
Wake:
    if stopping /\ ~stopped then
        if WakeWhileStopping then
            woken := TRUE;
            wokeWhileStopping := TRUE;
        end if;
    else
        woken := TRUE;
    end if;
end process;

fair process Shutdown = "shutdown"
begin
Start:
    either
        goto Off;
    or
        stopping := TRUE;
    end either;
WaitWorker:
    await pc["worker"] = "Done";
    stopped := TRUE;
Restart:
    if row = "running" then
        row := "killed";
        note := "pending";
    end if;
    restarted := TRUE;
Off:
    skip;
end process;

fair process Delivery = "delivery"
begin
Deliver:
    while TRUE do
        await note = "pending" /\ (woken \/ restarted) /\ (~stopping \/ stopped);
        note := "delivered";
        delivered := delivered + 1;
    end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "69dde584" /\ chksum(tla) = "3fc749de")
VARIABLES pc, fails, dbForever, attempts, outcome, row, note, woken, 
          delivered, gaveUp, wokeWhileStopping, stopping, stopped, restarted

(* define statement *)
DbRefuses == dbForever \/ fails > 0

TypeOK ==
    /\ outcome \in {"running", "ok", "cancelled"}
    /\ row \in {"running", "completed", "failed", "killed"}
    /\ note \in {"none", "pending", "delivered"}
    /\ delivered \in 0..2
    /\ attempts \in 0..(MaxFails + 1)

ExactlyOnce == delivered <= 1

FinishedNeverKilled == outcome = "ok" /\ row = "killed" => gaveUp

CancelledNeverCompleted == outcome = "cancelled" => row # "completed"

NoWakeWhileStopping == ~wokeWhileStopping

SavedOnceDbWorks == outcome = "ok" /\ ~dbForever ~> row = "completed" \/ stopping

SavedNoteDelivered == row \in {"completed", "failed"} ~> delivered = 1

ShutdownNotBlocked == stopping ~> stopped

NeverSavedAfterRefusals == ~(row = "completed" /\ attempts > 1)

NeverGaveUpOnShutdown == ~gaveUp


vars == << pc, fails, dbForever, attempts, outcome, row, note, woken, 
           delivered, gaveUp, wokeWhileStopping, stopping, stopped, restarted
        >>

ProcSet == {"worker"} \cup {"shutdown"} \cup {"delivery"}

Init == (* Global variables *)
        /\ fails \in 0..MaxFails
        /\ dbForever \in BOOLEAN
        /\ attempts = 0
        /\ outcome = "running"
        /\ row = "running"
        /\ note = "none"
        /\ woken = FALSE
        /\ delivered = 0
        /\ gaveUp = FALSE
        /\ wokeWhileStopping = FALSE
        /\ stopping = FALSE
        /\ stopped = FALSE
        /\ restarted = FALSE
        /\ pc = [self \in ProcSet |-> CASE self = "worker" -> "Work"
                                        [] self = "shutdown" -> "Start"
                                        [] self = "delivery" -> "Deliver"]

Work == /\ pc["worker"] = "Work"
        /\ \/ /\ outcome' = "ok"
           \/ /\ stopping
              /\ outcome' = "cancelled"
        /\ pc' = [pc EXCEPT !["worker"] = "Decide"]
        /\ UNCHANGED << fails, dbForever, attempts, row, note, woken, 
                        delivered, gaveUp, wokeWhileStopping, stopping, 
                        stopped, restarted >>

Decide == /\ pc["worker"] = "Decide"
          /\ IF outcome = "cancelled" /\ ~SaveCancelled
                THEN /\ pc' = [pc EXCEPT !["worker"] = "Done"]
                ELSE /\ IF outcome = "ok" /\ stopping /\ ~SaveCleanWhenAbandoned
                           THEN /\ pc' = [pc EXCEPT !["worker"] = "Done"]
                           ELSE /\ pc' = [pc EXCEPT !["worker"] = "Save"]
          /\ UNCHANGED << fails, dbForever, attempts, outcome, row, note, 
                          woken, delivered, gaveUp, wokeWhileStopping, 
                          stopping, stopped, restarted >>

Save == /\ pc["worker"] = "Save"
        /\ attempts' = (IF attempts > MaxFails THEN attempts ELSE attempts + 1)
        /\ IF DbRefuses
              THEN /\ IF fails > 0
                         THEN /\ fails' = fails - 1
                         ELSE /\ TRUE
                              /\ fails' = fails
                   /\ pc' = [pc EXCEPT !["worker"] = "Retry"]
                   /\ UNCHANGED << row, note >>
              ELSE /\ row' = "completed"
                   /\ note' = "pending"
                   /\ pc' = [pc EXCEPT !["worker"] = "Wake"]
                   /\ fails' = fails
        /\ UNCHANGED << dbForever, outcome, woken, delivered, gaveUp, 
                        wokeWhileStopping, stopping, stopped, restarted >>

Retry == /\ pc["worker"] = "Retry"
         /\ IF stopping /\ StopRetryOnShutdown
               THEN /\ gaveUp' = TRUE
                    /\ pc' = [pc EXCEPT !["worker"] = "Done"]
               ELSE /\ IF ~RetryForever /\ attempts >= MaxFails
                          THEN /\ pc' = [pc EXCEPT !["worker"] = "Done"]
                          ELSE /\ pc' = [pc EXCEPT !["worker"] = "Save"]
                    /\ UNCHANGED gaveUp
         /\ UNCHANGED << fails, dbForever, attempts, outcome, row, note, woken, 
                         delivered, wokeWhileStopping, stopping, stopped, 
                         restarted >>

Wake == /\ pc["worker"] = "Wake"
        /\ IF stopping /\ ~stopped
              THEN /\ IF WakeWhileStopping
                         THEN /\ woken' = TRUE
                              /\ wokeWhileStopping' = TRUE
                         ELSE /\ TRUE
                              /\ UNCHANGED << woken, wokeWhileStopping >>
              ELSE /\ woken' = TRUE
                   /\ UNCHANGED wokeWhileStopping
        /\ pc' = [pc EXCEPT !["worker"] = "Done"]
        /\ UNCHANGED << fails, dbForever, attempts, outcome, row, note, 
                        delivered, gaveUp, stopping, stopped, restarted >>

Worker == Work \/ Decide \/ Save \/ Retry \/ Wake

Start == /\ pc["shutdown"] = "Start"
         /\ \/ /\ pc' = [pc EXCEPT !["shutdown"] = "Off"]
               /\ UNCHANGED stopping
            \/ /\ stopping' = TRUE
               /\ pc' = [pc EXCEPT !["shutdown"] = "WaitWorker"]
         /\ UNCHANGED << fails, dbForever, attempts, outcome, row, note, woken, 
                         delivered, gaveUp, wokeWhileStopping, stopped, 
                         restarted >>

WaitWorker == /\ pc["shutdown"] = "WaitWorker"
              /\ pc["worker"] = "Done"
              /\ stopped' = TRUE
              /\ pc' = [pc EXCEPT !["shutdown"] = "Restart"]
              /\ UNCHANGED << fails, dbForever, attempts, outcome, row, note, 
                              woken, delivered, gaveUp, wokeWhileStopping, 
                              stopping, restarted >>

Restart == /\ pc["shutdown"] = "Restart"
           /\ IF row = "running"
                 THEN /\ row' = "killed"
                      /\ note' = "pending"
                 ELSE /\ TRUE
                      /\ UNCHANGED << row, note >>
           /\ restarted' = TRUE
           /\ pc' = [pc EXCEPT !["shutdown"] = "Off"]
           /\ UNCHANGED << fails, dbForever, attempts, outcome, woken, 
                           delivered, gaveUp, wokeWhileStopping, stopping, 
                           stopped >>

Off == /\ pc["shutdown"] = "Off"
       /\ TRUE
       /\ pc' = [pc EXCEPT !["shutdown"] = "Done"]
       /\ UNCHANGED << fails, dbForever, attempts, outcome, row, note, woken, 
                       delivered, gaveUp, wokeWhileStopping, stopping, stopped, 
                       restarted >>

Shutdown == Start \/ WaitWorker \/ Restart \/ Off

Deliver == /\ pc["delivery"] = "Deliver"
           /\ note = "pending" /\ (woken \/ restarted) /\ (~stopping \/ stopped)
           /\ note' = "delivered"
           /\ delivered' = delivered + 1
           /\ pc' = [pc EXCEPT !["delivery"] = "Deliver"]
           /\ UNCHANGED << fails, dbForever, attempts, outcome, row, woken, 
                           gaveUp, wokeWhileStopping, stopping, stopped, 
                           restarted >>

Delivery == Deliver

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == Worker \/ Shutdown \/ Delivery
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Worker)
        /\ WF_vars(Shutdown)
        /\ WF_vars(Delivery)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION 
=============================================================================
