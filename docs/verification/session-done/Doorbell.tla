---- MODULE Doorbell ----
EXTENDS Integers, TLC

CONSTANTS MaxSends, MaxManual, RaceFix, Recheck, Loss, Timers

(* --algorithm Doorbell
variables
    ocState = "idle",
    pendingSucc = FALSE,
    ocIdles = 0,
    failedIdles = {},
    inflight = FALSE,
    admittedPos = 0,
    admittedCount = 0,
    servedUpTo = 0,
    finishedUpTo = 0,
    sends = 0,
    manuals = 0,
    running = FALSE,
    through = 0,
    parked = FALSE,
    worked = FALSE,
    admittedAt = -1,
    parkAuto = TRUE,
    parkFor = 0,
    reqIdles = 0,
    ocBell = FALSE,
    unparkBell = FALSE,
    browserBell = FALSE,
    screenParked = FALSE,
    earlyEnd = FALSE,
    freshSync = FALSE;

define
    OcActive == ocState # "idle"
    Done == pc["relay"] = "Done" /\ pc["person"] = "Done" /\ ~inflight /\ ~OcActive
    Early == IF parkAuto THEN parkFor > finishedUpTo ELSE ocIdles <= reqIdles
    CanEnd == IF RaceFix
              THEN parked /\ admittedAt >= 0 /\ through > admittedAt /\ ~running
              ELSE parked /\ worked /\ ~running
    NoEarlyEnd == ~earlyEnd
    Owed == parkAuto \/ ocIdles > reqIdles
    FreshSyncReady == freshSync /\ Owed => (~parked \/ CanEnd)
    ScreenCatchesUp == Done /\ Owed ~> ~screenParked
end define;

fair process Relay = "relay"
begin
Send:
    while sends < MaxSends do
        sends := sends + 1 || freshSync := FALSE;
        if ~RaceFix then
            parked := TRUE || worked := FALSE || parkAuto := TRUE || parkFor := sends;
            browserBell := TRUE;
        end if;
Proxy:
        inflight := TRUE;
Respond:
        await ~inflight;
        if RaceFix then
            parked := TRUE || worked := FALSE || admittedAt := admittedPos || parkAuto := TRUE || parkFor := admittedCount;
            browserBell := TRUE;
            unparkBell := TRUE;
        end if;
    end while;
end process;

fair process Person = "person"
variables thr = 0;
begin
Want:
    while manuals < MaxManual do
        either
            manuals := manuals + 1;
Look:
            thr := ocIdles;
Write:
            parked := TRUE || worked := FALSE || admittedAt := thr || parkAuto := FALSE || reqIdles := thr || freshSync := FALSE;
            browserBell := TRUE;
            unparkBell := TRUE;
        or
            manuals := MaxManual;
        end either;
    end while;
end process;

fair process OpenCode = "opencode"
begin
Serve:
    while TRUE do
        either
            await inflight;
            inflight := FALSE || admittedPos := ocIdles || admittedCount := admittedCount + 1;
            if ocState = "idle" then
                ocState := "busy";
            elsif ocState \in {"settling", "marked"} then
                pendingSucc := TRUE;
            end if;
            ocBell := TRUE;
        or
            await ocState = "busy";
            ocState := "settling" || servedUpTo := admittedCount;
        or
            await ocState = "settling";
            with f \in BOOLEAN do
                ocIdles := ocIdles + 1 || finishedUpTo := servedUpTo || ocState := "marked";
                failedIdles := IF f THEN failedIdles \union {ocIdles} ELSE failedIdles;
            end with;
            ocBell := TRUE;
        or
            await ocState = "marked";
            if pendingSucc then
                ocState := "busy" || pendingSucc := FALSE;
            else
                ocState := "idle";
            end if;
            ocBell := TRUE;
        end either;
    end while;
end process;

process Lose = "loss"
begin
Drop:
    while Loss do
        either
            await ocBell;
            ocBell := FALSE;
        or
            await browserBell;
            browserBell := FALSE;
        end either;
    end while;
end process;

fair process Sync = "sync"
variables snapBusy = FALSE, lastThrough = 0, snapIdles = 0, startedDone = FALSE, busyNow = FALSE;
begin
Wait:
    while TRUE do
        either
            await ocBell;
            ocBell := FALSE;
        or
            await Timers;
        end either;
        startedDone := Done;
Active:
        snapBusy := OcActive;
Stored:
        lastThrough := through;
Idle:
        snapIdles := IF snapBusy THEN lastThrough ELSE ocIdles;
Again:
        busyNow := snapBusy \/ (Recheck /\ snapIdles > lastThrough /\ OcActive);
Commit:
        running := busyNow;
        if snapIdles > lastThrough then
            through := IF snapIdles > through THEN snapIdles ELSE through;
            if parked /\ snapIdles \in failedIdles /\ (~RaceFix \/ (snapIdles > admittedAt /\ ~busyNow)) then
                earlyEnd := earlyEnd \/ Early;
                parked := FALSE || worked := FALSE;
                browserBell := TRUE;
            elsif parked /\ ~worked then
                worked := TRUE;
            end if;
        end if;
        unparkBell := TRUE;
        if startedDone then
            freshSync := TRUE;
        end if;
    end while;
end process;

fair process Unpark = "unpark"
begin
Ring:
    while TRUE do
        either
            await unparkBell;
            unparkBell := FALSE;
        or
            await Timers;
        end either;
End:
        if CanEnd then
            earlyEnd := earlyEnd \/ Early;
            parked := FALSE || worked := FALSE;
            browserBell := TRUE;
        end if;
    end while;
end process;

fair process Browser = "browser"
begin
Fetch:
    while TRUE do
        either
            await browserBell;
            browserBell := FALSE;
        or
            await Timers;
        end either;
Show:
        screenParked := parked;
    end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, ocState, pendingSucc, ocIdles, failedIdles, inflight, 
          admittedPos, admittedCount, servedUpTo, finishedUpTo, sends, 
          manuals, running, through, parked, worked, admittedAt, parkAuto, 
          parkFor, reqIdles, ocBell, unparkBell, browserBell, screenParked, 
          earlyEnd, freshSync

(* define statement *)
OcActive == ocState # "idle"
Done == pc["relay"] = "Done" /\ pc["person"] = "Done" /\ ~inflight /\ ~OcActive
Early == IF parkAuto THEN parkFor > finishedUpTo ELSE ocIdles <= reqIdles
CanEnd == IF RaceFix
          THEN parked /\ admittedAt >= 0 /\ through > admittedAt /\ ~running
          ELSE parked /\ worked /\ ~running
NoEarlyEnd == ~earlyEnd
Owed == parkAuto \/ ocIdles > reqIdles
FreshSyncReady == freshSync /\ Owed => (~parked \/ CanEnd)
ScreenCatchesUp == Done /\ Owed ~> ~screenParked

VARIABLES thr, snapBusy, lastThrough, snapIdles, startedDone, busyNow

vars == << pc, ocState, pendingSucc, ocIdles, failedIdles, inflight, 
           admittedPos, admittedCount, servedUpTo, finishedUpTo, sends, 
           manuals, running, through, parked, worked, admittedAt, parkAuto, 
           parkFor, reqIdles, ocBell, unparkBell, browserBell, screenParked, 
           earlyEnd, freshSync, thr, snapBusy, lastThrough, snapIdles, 
           startedDone, busyNow >>

ProcSet == {"relay"} \cup {"person"} \cup {"opencode"} \cup {"loss"} \cup {"sync"} \cup {"unpark"} \cup {"browser"}

Init == (* Global variables *)
        /\ ocState = "idle"
        /\ pendingSucc = FALSE
        /\ ocIdles = 0
        /\ failedIdles = {}
        /\ inflight = FALSE
        /\ admittedPos = 0
        /\ admittedCount = 0
        /\ servedUpTo = 0
        /\ finishedUpTo = 0
        /\ sends = 0
        /\ manuals = 0
        /\ running = FALSE
        /\ through = 0
        /\ parked = FALSE
        /\ worked = FALSE
        /\ admittedAt = -1
        /\ parkAuto = TRUE
        /\ parkFor = 0
        /\ reqIdles = 0
        /\ ocBell = FALSE
        /\ unparkBell = FALSE
        /\ browserBell = FALSE
        /\ screenParked = FALSE
        /\ earlyEnd = FALSE
        /\ freshSync = FALSE
        (* Process Person *)
        /\ thr = 0
        (* Process Sync *)
        /\ snapBusy = FALSE
        /\ lastThrough = 0
        /\ snapIdles = 0
        /\ startedDone = FALSE
        /\ busyNow = FALSE
        /\ pc = [self \in ProcSet |-> CASE self = "relay" -> "Send"
                                        [] self = "person" -> "Want"
                                        [] self = "opencode" -> "Serve"
                                        [] self = "loss" -> "Drop"
                                        [] self = "sync" -> "Wait"
                                        [] self = "unpark" -> "Ring"
                                        [] self = "browser" -> "Fetch"]

Send == /\ pc["relay"] = "Send"
        /\ IF sends < MaxSends
              THEN /\ /\ freshSync' = FALSE
                      /\ sends' = sends + 1
                   /\ IF ~RaceFix
                         THEN /\ /\ parkAuto' = TRUE
                                 /\ parkFor' = sends'
                                 /\ parked' = TRUE
                                 /\ worked' = FALSE
                              /\ browserBell' = TRUE
                         ELSE /\ TRUE
                              /\ UNCHANGED << parked, worked, parkAuto, 
                                              parkFor, browserBell >>
                   /\ pc' = [pc EXCEPT !["relay"] = "Proxy"]
              ELSE /\ pc' = [pc EXCEPT !["relay"] = "Done"]
                   /\ UNCHANGED << sends, parked, worked, parkAuto, parkFor, 
                                   browserBell, freshSync >>
        /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                        admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                        manuals, running, through, admittedAt, reqIdles, 
                        ocBell, unparkBell, screenParked, earlyEnd, thr, 
                        snapBusy, lastThrough, snapIdles, startedDone, busyNow >>

Proxy == /\ pc["relay"] = "Proxy"
         /\ inflight' = TRUE
         /\ pc' = [pc EXCEPT !["relay"] = "Respond"]
         /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, 
                         admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                         sends, manuals, running, through, parked, worked, 
                         admittedAt, parkAuto, parkFor, reqIdles, ocBell, 
                         unparkBell, browserBell, screenParked, earlyEnd, 
                         freshSync, thr, snapBusy, lastThrough, snapIdles, 
                         startedDone, busyNow >>

Respond == /\ pc["relay"] = "Respond"
           /\ ~inflight
           /\ IF RaceFix
                 THEN /\ /\ admittedAt' = admittedPos
                         /\ parkAuto' = TRUE
                         /\ parkFor' = admittedCount
                         /\ parked' = TRUE
                         /\ worked' = FALSE
                      /\ browserBell' = TRUE
                      /\ unparkBell' = TRUE
                 ELSE /\ TRUE
                      /\ UNCHANGED << parked, worked, admittedAt, parkAuto, 
                                      parkFor, unparkBell, browserBell >>
           /\ pc' = [pc EXCEPT !["relay"] = "Send"]
           /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, 
                           inflight, admittedPos, admittedCount, servedUpTo, 
                           finishedUpTo, sends, manuals, running, through, 
                           reqIdles, ocBell, screenParked, earlyEnd, freshSync, 
                           thr, snapBusy, lastThrough, snapIdles, startedDone, 
                           busyNow >>

Relay == Send \/ Proxy \/ Respond

Want == /\ pc["person"] = "Want"
        /\ IF manuals < MaxManual
              THEN /\ \/ /\ manuals' = manuals + 1
                         /\ pc' = [pc EXCEPT !["person"] = "Look"]
                      \/ /\ manuals' = MaxManual
                         /\ pc' = [pc EXCEPT !["person"] = "Want"]
              ELSE /\ pc' = [pc EXCEPT !["person"] = "Done"]
                   /\ UNCHANGED manuals
        /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                        admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                        sends, running, through, parked, worked, admittedAt, 
                        parkAuto, parkFor, reqIdles, ocBell, unparkBell, 
                        browserBell, screenParked, earlyEnd, freshSync, thr, 
                        snapBusy, lastThrough, snapIdles, startedDone, busyNow >>

Look == /\ pc["person"] = "Look"
        /\ thr' = ocIdles
        /\ pc' = [pc EXCEPT !["person"] = "Write"]
        /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                        admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                        sends, manuals, running, through, parked, worked, 
                        admittedAt, parkAuto, parkFor, reqIdles, ocBell, 
                        unparkBell, browserBell, screenParked, earlyEnd, 
                        freshSync, snapBusy, lastThrough, snapIdles, 
                        startedDone, busyNow >>

Write == /\ pc["person"] = "Write"
         /\ /\ admittedAt' = thr
            /\ freshSync' = FALSE
            /\ parkAuto' = FALSE
            /\ parked' = TRUE
            /\ reqIdles' = thr
            /\ worked' = FALSE
         /\ browserBell' = TRUE
         /\ unparkBell' = TRUE
         /\ pc' = [pc EXCEPT !["person"] = "Want"]
         /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                         admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                         sends, manuals, running, through, parkFor, ocBell, 
                         screenParked, earlyEnd, thr, snapBusy, lastThrough, 
                         snapIdles, startedDone, busyNow >>

Person == Want \/ Look \/ Write

Serve == /\ pc["opencode"] = "Serve"
         /\ \/ /\ inflight
               /\ /\ admittedCount' = admittedCount + 1
                  /\ admittedPos' = ocIdles
                  /\ inflight' = FALSE
               /\ IF ocState = "idle"
                     THEN /\ ocState' = "busy"
                          /\ UNCHANGED pendingSucc
                     ELSE /\ IF ocState \in {"settling", "marked"}
                                THEN /\ pendingSucc' = TRUE
                                ELSE /\ TRUE
                                     /\ UNCHANGED pendingSucc
                          /\ UNCHANGED ocState
               /\ ocBell' = TRUE
               /\ UNCHANGED <<ocIdles, failedIdles, servedUpTo, finishedUpTo>>
            \/ /\ ocState = "busy"
               /\ /\ ocState' = "settling"
                  /\ servedUpTo' = admittedCount
               /\ UNCHANGED <<pendingSucc, ocIdles, failedIdles, inflight, admittedPos, admittedCount, finishedUpTo, ocBell>>
            \/ /\ ocState = "settling"
               /\ \E f \in BOOLEAN:
                    /\ /\ finishedUpTo' = servedUpTo
                       /\ ocIdles' = ocIdles + 1
                       /\ ocState' = "marked"
                    /\ failedIdles' = (IF f THEN failedIdles \union {ocIdles'} ELSE failedIdles)
               /\ ocBell' = TRUE
               /\ UNCHANGED <<pendingSucc, inflight, admittedPos, admittedCount, servedUpTo>>
            \/ /\ ocState = "marked"
               /\ IF pendingSucc
                     THEN /\ /\ ocState' = "busy"
                             /\ pendingSucc' = FALSE
                     ELSE /\ ocState' = "idle"
                          /\ UNCHANGED pendingSucc
               /\ ocBell' = TRUE
               /\ UNCHANGED <<ocIdles, failedIdles, inflight, admittedPos, admittedCount, servedUpTo, finishedUpTo>>
         /\ pc' = [pc EXCEPT !["opencode"] = "Serve"]
         /\ UNCHANGED << sends, manuals, running, through, parked, worked, 
                         admittedAt, parkAuto, parkFor, reqIdles, unparkBell, 
                         browserBell, screenParked, earlyEnd, freshSync, thr, 
                         snapBusy, lastThrough, snapIdles, startedDone, 
                         busyNow >>

OpenCode == Serve

Drop == /\ pc["loss"] = "Drop"
        /\ IF Loss
              THEN /\ \/ /\ ocBell
                         /\ ocBell' = FALSE
                         /\ UNCHANGED browserBell
                      \/ /\ browserBell
                         /\ browserBell' = FALSE
                         /\ UNCHANGED ocBell
                   /\ pc' = [pc EXCEPT !["loss"] = "Drop"]
              ELSE /\ pc' = [pc EXCEPT !["loss"] = "Done"]
                   /\ UNCHANGED << ocBell, browserBell >>
        /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                        admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                        sends, manuals, running, through, parked, worked, 
                        admittedAt, parkAuto, parkFor, reqIdles, unparkBell, 
                        screenParked, earlyEnd, freshSync, thr, snapBusy, 
                        lastThrough, snapIdles, startedDone, busyNow >>

Lose == Drop

Wait == /\ pc["sync"] = "Wait"
        /\ \/ /\ ocBell
              /\ ocBell' = FALSE
           \/ /\ Timers
              /\ UNCHANGED ocBell
        /\ startedDone' = Done
        /\ pc' = [pc EXCEPT !["sync"] = "Active"]
        /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                        admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                        sends, manuals, running, through, parked, worked, 
                        admittedAt, parkAuto, parkFor, reqIdles, unparkBell, 
                        browserBell, screenParked, earlyEnd, freshSync, thr, 
                        snapBusy, lastThrough, snapIdles, busyNow >>

Active == /\ pc["sync"] = "Active"
          /\ snapBusy' = OcActive
          /\ pc' = [pc EXCEPT !["sync"] = "Stored"]
          /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                          admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                          sends, manuals, running, through, parked, worked, 
                          admittedAt, parkAuto, parkFor, reqIdles, ocBell, 
                          unparkBell, browserBell, screenParked, earlyEnd, 
                          freshSync, thr, lastThrough, snapIdles, startedDone, 
                          busyNow >>

Stored == /\ pc["sync"] = "Stored"
          /\ lastThrough' = through
          /\ pc' = [pc EXCEPT !["sync"] = "Idle"]
          /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                          admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                          sends, manuals, running, through, parked, worked, 
                          admittedAt, parkAuto, parkFor, reqIdles, ocBell, 
                          unparkBell, browserBell, screenParked, earlyEnd, 
                          freshSync, thr, snapBusy, snapIdles, startedDone, 
                          busyNow >>

Idle == /\ pc["sync"] = "Idle"
        /\ snapIdles' = IF snapBusy THEN lastThrough ELSE ocIdles
        /\ pc' = [pc EXCEPT !["sync"] = "Again"]
        /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                        admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                        sends, manuals, running, through, parked, worked, 
                        admittedAt, parkAuto, parkFor, reqIdles, ocBell, 
                        unparkBell, browserBell, screenParked, earlyEnd, 
                        freshSync, thr, snapBusy, lastThrough, startedDone, 
                        busyNow >>

Again == /\ pc["sync"] = "Again"
         /\ busyNow' = (snapBusy \/ (Recheck /\ snapIdles > lastThrough /\ OcActive))
         /\ pc' = [pc EXCEPT !["sync"] = "Commit"]
         /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                         admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                         sends, manuals, running, through, parked, worked, 
                         admittedAt, parkAuto, parkFor, reqIdles, ocBell, 
                         unparkBell, browserBell, screenParked, earlyEnd, 
                         freshSync, thr, snapBusy, lastThrough, snapIdles, 
                         startedDone >>

Commit == /\ pc["sync"] = "Commit"
          /\ running' = busyNow
          /\ IF snapIdles > lastThrough
                THEN /\ through' = (IF snapIdles > through THEN snapIdles ELSE through)
                     /\ IF parked /\ snapIdles \in failedIdles /\ (~RaceFix \/ (snapIdles > admittedAt /\ ~busyNow))
                           THEN /\ earlyEnd' = (earlyEnd \/ Early)
                                /\ /\ parked' = FALSE
                                   /\ worked' = FALSE
                                /\ browserBell' = TRUE
                           ELSE /\ IF parked /\ ~worked
                                      THEN /\ worked' = TRUE
                                      ELSE /\ TRUE
                                           /\ UNCHANGED worked
                                /\ UNCHANGED << parked, browserBell, earlyEnd >>
                ELSE /\ TRUE
                     /\ UNCHANGED << through, parked, worked, browserBell, 
                                     earlyEnd >>
          /\ unparkBell' = TRUE
          /\ IF startedDone
                THEN /\ freshSync' = TRUE
                ELSE /\ TRUE
                     /\ UNCHANGED freshSync
          /\ pc' = [pc EXCEPT !["sync"] = "Wait"]
          /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                          admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                          sends, manuals, admittedAt, parkAuto, parkFor, 
                          reqIdles, ocBell, screenParked, thr, snapBusy, 
                          lastThrough, snapIdles, startedDone, busyNow >>

Sync == Wait \/ Active \/ Stored \/ Idle \/ Again \/ Commit

Ring == /\ pc["unpark"] = "Ring"
        /\ \/ /\ unparkBell
              /\ unparkBell' = FALSE
           \/ /\ Timers
              /\ UNCHANGED unparkBell
        /\ pc' = [pc EXCEPT !["unpark"] = "End"]
        /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                        admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                        sends, manuals, running, through, parked, worked, 
                        admittedAt, parkAuto, parkFor, reqIdles, ocBell, 
                        browserBell, screenParked, earlyEnd, freshSync, thr, 
                        snapBusy, lastThrough, snapIdles, startedDone, busyNow >>

End == /\ pc["unpark"] = "End"
       /\ IF CanEnd
             THEN /\ earlyEnd' = (earlyEnd \/ Early)
                  /\ /\ parked' = FALSE
                     /\ worked' = FALSE
                  /\ browserBell' = TRUE
             ELSE /\ TRUE
                  /\ UNCHANGED << parked, worked, browserBell, earlyEnd >>
       /\ pc' = [pc EXCEPT !["unpark"] = "Ring"]
       /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                       admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                       sends, manuals, running, through, admittedAt, parkAuto, 
                       parkFor, reqIdles, ocBell, unparkBell, screenParked, 
                       freshSync, thr, snapBusy, lastThrough, snapIdles, 
                       startedDone, busyNow >>

Unpark == Ring \/ End

Fetch == /\ pc["browser"] = "Fetch"
         /\ \/ /\ browserBell
               /\ browserBell' = FALSE
            \/ /\ Timers
               /\ UNCHANGED browserBell
         /\ pc' = [pc EXCEPT !["browser"] = "Show"]
         /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                         admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                         sends, manuals, running, through, parked, worked, 
                         admittedAt, parkAuto, parkFor, reqIdles, ocBell, 
                         unparkBell, screenParked, earlyEnd, freshSync, thr, 
                         snapBusy, lastThrough, snapIdles, startedDone, 
                         busyNow >>

Show == /\ pc["browser"] = "Show"
        /\ screenParked' = parked
        /\ pc' = [pc EXCEPT !["browser"] = "Fetch"]
        /\ UNCHANGED << ocState, pendingSucc, ocIdles, failedIdles, inflight, 
                        admittedPos, admittedCount, servedUpTo, finishedUpTo, 
                        sends, manuals, running, through, parked, worked, 
                        admittedAt, parkAuto, parkFor, reqIdles, ocBell, 
                        unparkBell, browserBell, earlyEnd, freshSync, thr, 
                        snapBusy, lastThrough, snapIdles, startedDone, busyNow >>

Browser == Fetch \/ Show

Next == Relay \/ Person \/ OpenCode \/ Lose \/ Sync \/ Unpark \/ Browser

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Relay)
        /\ WF_vars(Person)
        /\ WF_vars(OpenCode)
        /\ WF_vars(Sync)
        /\ WF_vars(Unpark)
        /\ WF_vars(Browser)

\* END TRANSLATION
====
