----------------------------- MODULE TurnSecrets -----------------------------
EXTENDS Naturals, FiniteSets

CONSTANTS Case, Variant

Names == {"n1", "n2"}
Runs == {"run1", "run2"}
RunName(k) == IF k = 1 THEN "run1" ELSE "run2"
NativeRequests == {<<"native", 1>>, <<"native", 2>>}
Requests == NativeRequests \cup {<<"run1", 1>>, <<"run2", 1>>}
Stores == {"native", "opencode"}
None == <<"none", 0>>
Empty == [n \in Names |-> None]
Final == {"saved", "declined", "withdrawn", "refused", "gone"}
Withdrawn(st, turns) == [r \in Requests |->
    IF r[1] \in turns /\ st[r] \in {"pending", "answered"} THEN "withdrawn" ELSE st[r]]

(* --algorithm TurnSecrets
variables
    store = [s \in Stores |-> Empty],
    retired = [s \in Stores |-> {}],
    status = [r \in Requests |-> "none"],
    asked = [r \in Requests |-> "n1"],
    posted = [r \in Requests |-> FALSE],
    holders = 0, foregroundDone = FALSE,
    job = "none", jobEnv = {},
    cmdEnv = {}, script = {}, ocEnv = {},
    journal = {}, modelInput = {}, used = {},
    busy = FALSE, web = [r \in Runs |-> FALSE],
    ranOut = {}, events = {}, synced = {}, seen = {},
    crashed = FALSE, wiped = {}, replay = [r \in NativeRequests |-> "none"];

define
    Held(s) == {store[s][n] : n \in Names} \ {None}
    Known(s) == Held(s) \cup retired[s]
    Redact(out, s) == out \ Known(s)
    Replaced(s, n) == IF Variant = "no-replaced-redaction" THEN {} ELSE {store[s][n]} \ {None}
    NativeEnded == foregroundDone /\ holders = 0
end define;

fair process Native = "native"
variables asks = 0, ran = FALSE;
begin
NStart:
    await Case = "native";
    holders := 1;
NLoop:
    while TRUE do
        either
            await ~crashed /\ asks < 2;
            with n \in Names do
                asked[<<"native", asks + 1>>] := n;
            end with;
            status[<<"native", asks + 1>>] := "pending";
            asks := asks + 1;
NWait:
            await ~crashed;
            either
                await status[<<"native", asks>>] = "answered";
                retired["native"] := retired["native"] \cup Replaced("native", asked[<<"native", asks>>]);
                store["native"][asked[<<"native", asks>>]] := <<"native", asks>>;
                status[<<"native", asks>>] := "saved";
            or
                await status[<<"native", asks>>] \in {"pending", "answered"};
                status[<<"native", asks>>] := "withdrawn";
            or
                await status[<<"native", asks>>] = "declined";
            end either;
        or
            await ~crashed /\ job = "none";
            job := "running";
            jobEnv := Held("native");
            holders := holders + 1;
        or
            await ~crashed /\ ~ran;
            ran := TRUE;
            cmdEnv := Held("native");
            used := used \cup {<<"native", v>> : v \in Held("native")};
NInner:
            await ~crashed;
            script := IF Variant = "no-inner-redaction" THEN cmdEnv ELSE Redact(cmdEnv, "native");
            journal := journal \cup script;
NOuter:
            await ~crashed;
            journal := journal \cup Redact(script, "native");
            modelInput := modelInput \cup Redact(script, "native");
            cmdEnv := {};
            script := {};
        or
            await ~crashed;
            goto NFinish;
        end either;
    end while;
NFinish:
    await ~crashed;
    foregroundDone := TRUE;
    if holders = 1 \/ Variant = "erase-at-run-end" then
        store["native"] := Empty;
        retired["native"] := {};
        status := Withdrawn(status, {"native"});
    end if;
    holders := holders - 1;
end process;

fair process Job = "job"
begin
JPrint:
    await ~crashed /\ job = "running";
    journal := journal \cup Redact(jobEnv, "native");
    modelInput := modelInput \cup Redact(jobEnv, "native");
JFinish:
    await ~crashed;
    job := "done";
    jobEnv := {};
    if holders = 1 then
        store["native"] := Empty;
        retired["native"] := {};
        status := Withdrawn(status, {"native"});
    end if;
    holders := holders - 1;
end process;

fair process Person = "person"
begin
PAnswer:
    while TRUE do
        with r \in {q \in Requests : status[q] \notin {"none", "refused"} /\ ~posted[q]} do
            posted[r] := TRUE;
            if status[r] = "pending" then
                either
                    status[r] := "answered";
                or
                    status[r] := "declined";
                end either;
            end if;
        end with;
    end while;
end process;

fair process OpenCode = "opencode"
variables k = 0;
begin
OPrompt:
    while k < 2 do
        await Case = "opencode";
        with w \in BOOLEAN do
            web[RunName(k + 1)] := w;
            if w /\ Variant # "erase-without-run-reset" then
                store["opencode"] := Empty;
                retired["opencode"] := {};
            end if;
        end with;
        busy := TRUE;
        k := k + 1;
OAsk:
        either
            if web[RunName(k)] then
                with n \in Names do
                    asked[<<RunName(k), 1>>] := n;
                end with;
                status[<<RunName(k), 1>>] := "pending";
            else
                status[<<RunName(k), 1>>] := "refused";
                goto ORun;
            end if;
OWait:
            either
                await status[<<RunName(k), 1>>] = "answered";
                retired["opencode"] := retired["opencode"] \cup Replaced("opencode", asked[<<RunName(k), 1>>]);
                store["opencode"][asked[<<RunName(k), 1>>]] := <<RunName(k), 1>>;
                status[<<RunName(k), 1>>] := "saved";
            or
                await status[<<RunName(k), 1>>] \in {"declined", "withdrawn", "gone"};
            or
                await status[<<RunName(k), 1>>] \in {"pending", "answered"};
                status[<<RunName(k), 1>>] := "withdrawn";
                goto OEnd;
            end either;
        or
            skip;
        end either;
ORun:
        either
            if web[RunName(k)] then
                ocEnv := Held("opencode");
                used := used \cup {<<RunName(k), v>> : v \in Held("opencode")};
            end if;
ORead:
            modelInput := modelInput \cup (Redact(ocEnv, "opencode") \ ocEnv);
            ocEnv := {};
        or
            skip;
        end either;
OEnd:
        busy := FALSE;
        ranOut := ranOut \cup {RunName(k)};
        either
            events := events \cup {RunName(k)};
        or
            skip;
        end either;
    end while;
end process;

fair process Follow = "follow"
begin
FEvent:
    while TRUE do
        with e \in events do
            events := events \ {e};
            store["opencode"] := Empty;
            retired["opencode"] := {};
            status := Withdrawn(status, Runs);
            seen := seen \cup {e};
        end with;
    end while;
end process;

fair process Sync = "sync"
begin
SPoll:
    while TRUE do
        await ~busy /\ ranOut \ synced # {};
        store["opencode"] := Empty;
        retired["opencode"] := {};
        status := Withdrawn(status, Runs);
        seen := seen \cup ranOut;
        synced := ranOut;
    end while;
end process;

process Crash = "crash"
begin
CStrike:
    crashed := TRUE;
    wiped := {r \in Requests : status[r] # "none"};
    store := [s \in Stores |-> Empty];
    retired := [s \in Stores |-> {}];
    status := [r \in Requests |-> IF status[r] \in {"pending", "answered"} THEN "gone" ELSE status[r]];
    holders := 0;
    foregroundDone := TRUE;
    job := IF job = "running" THEN "killed" ELSE job;
    jobEnv := {};
    cmdEnv := {};
    script := {};
    ocEnv := {};
CReplay:
    replay := [r \in NativeRequests |->
        IF status[r] = "none" THEN "none"
        ELSE IF store["native"][asked[r]] # None THEN "saved" ELSE "gone"];
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, store, retired, status, asked, posted, holders, foregroundDone, 
          job, jobEnv, cmdEnv, script, ocEnv, journal, modelInput, used, busy, 
          web, ranOut, events, synced, seen, crashed, wiped, replay

(* define statement *)
Held(s) == {store[s][n] : n \in Names} \ {None}
Known(s) == Held(s) \cup retired[s]
Redact(out, s) == out \ Known(s)
Replaced(s, n) == IF Variant = "no-replaced-redaction" THEN {} ELSE {store[s][n]} \ {None}
NativeEnded == foregroundDone /\ holders = 0

VARIABLES asks, ran, k

vars == << pc, store, retired, status, asked, posted, holders, foregroundDone, 
           job, jobEnv, cmdEnv, script, ocEnv, journal, modelInput, used, 
           busy, web, ranOut, events, synced, seen, crashed, wiped, replay, 
           asks, ran, k >>

ProcSet == {"native"} \cup {"job"} \cup {"person"} \cup {"opencode"} \cup {"follow"} \cup {"sync"} \cup {"crash"}

Init == (* Global variables *)
        /\ store = [s \in Stores |-> Empty]
        /\ retired = [s \in Stores |-> {}]
        /\ status = [r \in Requests |-> "none"]
        /\ asked = [r \in Requests |-> "n1"]
        /\ posted = [r \in Requests |-> FALSE]
        /\ holders = 0
        /\ foregroundDone = FALSE
        /\ job = "none"
        /\ jobEnv = {}
        /\ cmdEnv = {}
        /\ script = {}
        /\ ocEnv = {}
        /\ journal = {}
        /\ modelInput = {}
        /\ used = {}
        /\ busy = FALSE
        /\ web = [r \in Runs |-> FALSE]
        /\ ranOut = {}
        /\ events = {}
        /\ synced = {}
        /\ seen = {}
        /\ crashed = FALSE
        /\ wiped = {}
        /\ replay = [r \in NativeRequests |-> "none"]
        (* Process Native *)
        /\ asks = 0
        /\ ran = FALSE
        (* Process OpenCode *)
        /\ k = 0
        /\ pc = [self \in ProcSet |-> CASE self = "native" -> "NStart"
                                        [] self = "job" -> "JPrint"
                                        [] self = "person" -> "PAnswer"
                                        [] self = "opencode" -> "OPrompt"
                                        [] self = "follow" -> "FEvent"
                                        [] self = "sync" -> "SPoll"
                                        [] self = "crash" -> "CStrike"]

NStart == /\ pc["native"] = "NStart"
          /\ Case = "native"
          /\ holders' = 1
          /\ pc' = [pc EXCEPT !["native"] = "NLoop"]
          /\ UNCHANGED << store, retired, status, asked, posted, 
                          foregroundDone, job, jobEnv, cmdEnv, script, ocEnv, 
                          journal, modelInput, used, busy, web, ranOut, events, 
                          synced, seen, crashed, wiped, replay, asks, ran, k >>

NLoop == /\ pc["native"] = "NLoop"
         /\ \/ /\ ~crashed /\ asks < 2
               /\ \E n \in Names:
                    asked' = [asked EXCEPT ![<<"native", asks + 1>>] = n]
               /\ status' = [status EXCEPT ![<<"native", asks + 1>>] = "pending"]
               /\ asks' = asks + 1
               /\ pc' = [pc EXCEPT !["native"] = "NWait"]
               /\ UNCHANGED <<holders, job, jobEnv, cmdEnv, used, ran>>
            \/ /\ ~crashed /\ job = "none"
               /\ job' = "running"
               /\ jobEnv' = Held("native")
               /\ holders' = holders + 1
               /\ pc' = [pc EXCEPT !["native"] = "NLoop"]
               /\ UNCHANGED <<status, asked, cmdEnv, used, asks, ran>>
            \/ /\ ~crashed /\ ~ran
               /\ ran' = TRUE
               /\ cmdEnv' = Held("native")
               /\ used' = (used \cup {<<"native", v>> : v \in Held("native")})
               /\ pc' = [pc EXCEPT !["native"] = "NInner"]
               /\ UNCHANGED <<status, asked, holders, job, jobEnv, asks>>
            \/ /\ ~crashed
               /\ pc' = [pc EXCEPT !["native"] = "NFinish"]
               /\ UNCHANGED <<status, asked, holders, job, jobEnv, cmdEnv, used, asks, ran>>
         /\ UNCHANGED << store, retired, posted, foregroundDone, script, ocEnv, 
                         journal, modelInput, busy, web, ranOut, events, 
                         synced, seen, crashed, wiped, replay, k >>

NWait == /\ pc["native"] = "NWait"
         /\ ~crashed
         /\ \/ /\ status[<<"native", asks>>] = "answered"
               /\ retired' = [retired EXCEPT !["native"] = retired["native"] \cup Replaced("native", asked[<<"native", asks>>])]
               /\ store' = [store EXCEPT !["native"][asked[<<"native", asks>>]] = <<"native", asks>>]
               /\ status' = [status EXCEPT ![<<"native", asks>>] = "saved"]
            \/ /\ status[<<"native", asks>>] \in {"pending", "answered"}
               /\ status' = [status EXCEPT ![<<"native", asks>>] = "withdrawn"]
               /\ UNCHANGED <<store, retired>>
            \/ /\ status[<<"native", asks>>] = "declined"
               /\ UNCHANGED <<store, retired, status>>
         /\ pc' = [pc EXCEPT !["native"] = "NLoop"]
         /\ UNCHANGED << asked, posted, holders, foregroundDone, job, jobEnv, 
                         cmdEnv, script, ocEnv, journal, modelInput, used, 
                         busy, web, ranOut, events, synced, seen, crashed, 
                         wiped, replay, asks, ran, k >>

NInner == /\ pc["native"] = "NInner"
          /\ ~crashed
          /\ script' = (IF Variant = "no-inner-redaction" THEN cmdEnv ELSE Redact(cmdEnv, "native"))
          /\ journal' = (journal \cup script')
          /\ pc' = [pc EXCEPT !["native"] = "NOuter"]
          /\ UNCHANGED << store, retired, status, asked, posted, holders, 
                          foregroundDone, job, jobEnv, cmdEnv, ocEnv, 
                          modelInput, used, busy, web, ranOut, events, synced, 
                          seen, crashed, wiped, replay, asks, ran, k >>

NOuter == /\ pc["native"] = "NOuter"
          /\ ~crashed
          /\ journal' = (journal \cup Redact(script, "native"))
          /\ modelInput' = (modelInput \cup Redact(script, "native"))
          /\ cmdEnv' = {}
          /\ script' = {}
          /\ pc' = [pc EXCEPT !["native"] = "NLoop"]
          /\ UNCHANGED << store, retired, status, asked, posted, holders, 
                          foregroundDone, job, jobEnv, ocEnv, used, busy, web, 
                          ranOut, events, synced, seen, crashed, wiped, replay, 
                          asks, ran, k >>

NFinish == /\ pc["native"] = "NFinish"
           /\ ~crashed
           /\ foregroundDone' = TRUE
           /\ IF holders = 1 \/ Variant = "erase-at-run-end"
                 THEN /\ store' = [store EXCEPT !["native"] = Empty]
                      /\ retired' = [retired EXCEPT !["native"] = {}]
                      /\ status' = Withdrawn(status, {"native"})
                 ELSE /\ TRUE
                      /\ UNCHANGED << store, retired, status >>
           /\ holders' = holders - 1
           /\ pc' = [pc EXCEPT !["native"] = "Done"]
           /\ UNCHANGED << asked, posted, job, jobEnv, cmdEnv, script, ocEnv, 
                           journal, modelInput, used, busy, web, ranOut, 
                           events, synced, seen, crashed, wiped, replay, asks, 
                           ran, k >>

Native == NStart \/ NLoop \/ NWait \/ NInner \/ NOuter \/ NFinish

JPrint == /\ pc["job"] = "JPrint"
          /\ ~crashed /\ job = "running"
          /\ journal' = (journal \cup Redact(jobEnv, "native"))
          /\ modelInput' = (modelInput \cup Redact(jobEnv, "native"))
          /\ pc' = [pc EXCEPT !["job"] = "JFinish"]
          /\ UNCHANGED << store, retired, status, asked, posted, holders, 
                          foregroundDone, job, jobEnv, cmdEnv, script, ocEnv, 
                          used, busy, web, ranOut, events, synced, seen, 
                          crashed, wiped, replay, asks, ran, k >>

JFinish == /\ pc["job"] = "JFinish"
           /\ ~crashed
           /\ job' = "done"
           /\ jobEnv' = {}
           /\ IF holders = 1
                 THEN /\ store' = [store EXCEPT !["native"] = Empty]
                      /\ retired' = [retired EXCEPT !["native"] = {}]
                      /\ status' = Withdrawn(status, {"native"})
                 ELSE /\ TRUE
                      /\ UNCHANGED << store, retired, status >>
           /\ holders' = holders - 1
           /\ pc' = [pc EXCEPT !["job"] = "Done"]
           /\ UNCHANGED << asked, posted, foregroundDone, cmdEnv, script, 
                           ocEnv, journal, modelInput, used, busy, web, ranOut, 
                           events, synced, seen, crashed, wiped, replay, asks, 
                           ran, k >>

Job == JPrint \/ JFinish

PAnswer == /\ pc["person"] = "PAnswer"
           /\ \E r \in {q \in Requests : status[q] \notin {"none", "refused"} /\ ~posted[q]}:
                /\ posted' = [posted EXCEPT ![r] = TRUE]
                /\ IF status[r] = "pending"
                      THEN /\ \/ /\ status' = [status EXCEPT ![r] = "answered"]
                              \/ /\ status' = [status EXCEPT ![r] = "declined"]
                      ELSE /\ TRUE
                           /\ UNCHANGED status
           /\ pc' = [pc EXCEPT !["person"] = "PAnswer"]
           /\ UNCHANGED << store, retired, asked, holders, foregroundDone, job, 
                           jobEnv, cmdEnv, script, ocEnv, journal, modelInput, 
                           used, busy, web, ranOut, events, synced, seen, 
                           crashed, wiped, replay, asks, ran, k >>

Person == PAnswer

OPrompt == /\ pc["opencode"] = "OPrompt"
           /\ IF k < 2
                 THEN /\ Case = "opencode"
                      /\ \E w \in BOOLEAN:
                           /\ web' = [web EXCEPT ![RunName(k + 1)] = w]
                           /\ IF w /\ Variant # "erase-without-run-reset"
                                 THEN /\ store' = [store EXCEPT !["opencode"] = Empty]
                                      /\ retired' = [retired EXCEPT !["opencode"] = {}]
                                 ELSE /\ TRUE
                                      /\ UNCHANGED << store, retired >>
                      /\ busy' = TRUE
                      /\ k' = k + 1
                      /\ pc' = [pc EXCEPT !["opencode"] = "OAsk"]
                 ELSE /\ pc' = [pc EXCEPT !["opencode"] = "Done"]
                      /\ UNCHANGED << store, retired, busy, web, k >>
           /\ UNCHANGED << status, asked, posted, holders, foregroundDone, job, 
                           jobEnv, cmdEnv, script, ocEnv, journal, modelInput, 
                           used, ranOut, events, synced, seen, crashed, wiped, 
                           replay, asks, ran >>

OAsk == /\ pc["opencode"] = "OAsk"
        /\ \/ /\ IF web[RunName(k)]
                    THEN /\ \E n \in Names:
                              asked' = [asked EXCEPT ![<<RunName(k), 1>>] = n]
                         /\ status' = [status EXCEPT ![<<RunName(k), 1>>] = "pending"]
                         /\ pc' = [pc EXCEPT !["opencode"] = "OWait"]
                    ELSE /\ status' = [status EXCEPT ![<<RunName(k), 1>>] = "refused"]
                         /\ pc' = [pc EXCEPT !["opencode"] = "ORun"]
                         /\ asked' = asked
           \/ /\ TRUE
              /\ pc' = [pc EXCEPT !["opencode"] = "ORun"]
              /\ UNCHANGED <<status, asked>>
        /\ UNCHANGED << store, retired, posted, holders, foregroundDone, job, 
                        jobEnv, cmdEnv, script, ocEnv, journal, modelInput, 
                        used, busy, web, ranOut, events, synced, seen, crashed, 
                        wiped, replay, asks, ran, k >>

OWait == /\ pc["opencode"] = "OWait"
         /\ \/ /\ status[<<RunName(k), 1>>] = "answered"
               /\ retired' = [retired EXCEPT !["opencode"] = retired["opencode"] \cup Replaced("opencode", asked[<<RunName(k), 1>>])]
               /\ store' = [store EXCEPT !["opencode"][asked[<<RunName(k), 1>>]] = <<RunName(k), 1>>]
               /\ status' = [status EXCEPT ![<<RunName(k), 1>>] = "saved"]
               /\ pc' = [pc EXCEPT !["opencode"] = "ORun"]
            \/ /\ status[<<RunName(k), 1>>] \in {"declined", "withdrawn", "gone"}
               /\ pc' = [pc EXCEPT !["opencode"] = "ORun"]
               /\ UNCHANGED <<store, retired, status>>
            \/ /\ status[<<RunName(k), 1>>] \in {"pending", "answered"}
               /\ status' = [status EXCEPT ![<<RunName(k), 1>>] = "withdrawn"]
               /\ pc' = [pc EXCEPT !["opencode"] = "OEnd"]
               /\ UNCHANGED <<store, retired>>
         /\ UNCHANGED << asked, posted, holders, foregroundDone, job, jobEnv, 
                         cmdEnv, script, ocEnv, journal, modelInput, used, 
                         busy, web, ranOut, events, synced, seen, crashed, 
                         wiped, replay, asks, ran, k >>

ORun == /\ pc["opencode"] = "ORun"
        /\ \/ /\ IF web[RunName(k)]
                    THEN /\ ocEnv' = Held("opencode")
                         /\ used' = (used \cup {<<RunName(k), v>> : v \in Held("opencode")})
                    ELSE /\ TRUE
                         /\ UNCHANGED << ocEnv, used >>
              /\ pc' = [pc EXCEPT !["opencode"] = "ORead"]
           \/ /\ TRUE
              /\ pc' = [pc EXCEPT !["opencode"] = "OEnd"]
              /\ UNCHANGED <<ocEnv, used>>
        /\ UNCHANGED << store, retired, status, asked, posted, holders, 
                        foregroundDone, job, jobEnv, cmdEnv, script, journal, 
                        modelInput, busy, web, ranOut, events, synced, seen, 
                        crashed, wiped, replay, asks, ran, k >>

ORead == /\ pc["opencode"] = "ORead"
         /\ modelInput' = (modelInput \cup (Redact(ocEnv, "opencode") \ ocEnv))
         /\ ocEnv' = {}
         /\ pc' = [pc EXCEPT !["opencode"] = "OEnd"]
         /\ UNCHANGED << store, retired, status, asked, posted, holders, 
                         foregroundDone, job, jobEnv, cmdEnv, script, journal, 
                         used, busy, web, ranOut, events, synced, seen, 
                         crashed, wiped, replay, asks, ran, k >>

OEnd == /\ pc["opencode"] = "OEnd"
        /\ busy' = FALSE
        /\ ranOut' = (ranOut \cup {RunName(k)})
        /\ \/ /\ events' = (events \cup {RunName(k)})
           \/ /\ TRUE
              /\ UNCHANGED events
        /\ pc' = [pc EXCEPT !["opencode"] = "OPrompt"]
        /\ UNCHANGED << store, retired, status, asked, posted, holders, 
                        foregroundDone, job, jobEnv, cmdEnv, script, ocEnv, 
                        journal, modelInput, used, web, synced, seen, crashed, 
                        wiped, replay, asks, ran, k >>

OpenCode == OPrompt \/ OAsk \/ OWait \/ ORun \/ ORead \/ OEnd

FEvent == /\ pc["follow"] = "FEvent"
          /\ \E e \in events:
               /\ events' = events \ {e}
               /\ store' = [store EXCEPT !["opencode"] = Empty]
               /\ retired' = [retired EXCEPT !["opencode"] = {}]
               /\ status' = Withdrawn(status, Runs)
               /\ seen' = (seen \cup {e})
          /\ pc' = [pc EXCEPT !["follow"] = "FEvent"]
          /\ UNCHANGED << asked, posted, holders, foregroundDone, job, jobEnv, 
                          cmdEnv, script, ocEnv, journal, modelInput, used, 
                          busy, web, ranOut, synced, crashed, wiped, replay, 
                          asks, ran, k >>

Follow == FEvent

SPoll == /\ pc["sync"] = "SPoll"
         /\ ~busy /\ ranOut \ synced # {}
         /\ store' = [store EXCEPT !["opencode"] = Empty]
         /\ retired' = [retired EXCEPT !["opencode"] = {}]
         /\ status' = Withdrawn(status, Runs)
         /\ seen' = (seen \cup ranOut)
         /\ synced' = ranOut
         /\ pc' = [pc EXCEPT !["sync"] = "SPoll"]
         /\ UNCHANGED << asked, posted, holders, foregroundDone, job, jobEnv, 
                         cmdEnv, script, ocEnv, journal, modelInput, used, 
                         busy, web, ranOut, events, crashed, wiped, replay, 
                         asks, ran, k >>

Sync == SPoll

CStrike == /\ pc["crash"] = "CStrike"
           /\ crashed' = TRUE
           /\ wiped' = {r \in Requests : status[r] # "none"}
           /\ store' = [s \in Stores |-> Empty]
           /\ retired' = [s \in Stores |-> {}]
           /\ status' = [r \in Requests |-> IF status[r] \in {"pending", "answered"} THEN "gone" ELSE status[r]]
           /\ holders' = 0
           /\ foregroundDone' = TRUE
           /\ job' = (IF job = "running" THEN "killed" ELSE job)
           /\ jobEnv' = {}
           /\ cmdEnv' = {}
           /\ script' = {}
           /\ ocEnv' = {}
           /\ pc' = [pc EXCEPT !["crash"] = "CReplay"]
           /\ UNCHANGED << asked, posted, journal, modelInput, used, busy, web, 
                           ranOut, events, synced, seen, replay, asks, ran, k >>

CReplay == /\ pc["crash"] = "CReplay"
           /\ replay' =       [r \in NativeRequests |->
                        IF status[r] = "none" THEN "none"
                        ELSE IF store["native"][asked[r]] # None THEN "saved" ELSE "gone"]
           /\ pc' = [pc EXCEPT !["crash"] = "Done"]
           /\ UNCHANGED << store, retired, status, asked, posted, holders, 
                           foregroundDone, job, jobEnv, cmdEnv, script, ocEnv, 
                           journal, modelInput, used, busy, web, ranOut, 
                           events, synced, seen, crashed, wiped, asks, ran, k >>

Crash == CStrike \/ CReplay

Next == Native \/ Job \/ Person \/ OpenCode \/ Follow \/ Sync \/ Crash

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Native)
        /\ WF_vars(Job)
        /\ WF_vars(Person)
        /\ WF_vars(OpenCode)
        /\ WF_vars(Follow)
        /\ WF_vars(Sync)

\* END TRANSLATION

Ended(t) == IF t = "native" THEN NativeEnded ELSE t \in seen

JournalNeverHoldsValue == journal = {}

ModelInputNeverHoldsValue == modelInput = {}

NoValueCrossesTurns == \A u \in used : u[2][1] = u[1]

EndedTurnHoldsNothing ==
    /\ NativeEnded =>
        /\ Known("native") = {}
        /\ jobEnv = {} /\ cmdEnv = {} /\ script = {}
        /\ \A r \in NativeRequests : status[r] \notin {"pending", "answered"}
    /\ \A e \in seen :
        /\ \A v \in Known("opencode") : v[1] # e
        /\ status[<<e, 1>>] \notin {"pending", "answered"}

LateSaveStoresNothing == \A r \in Requests : status[r] = "answered" => ~Ended(r[1])

CrashLeavesNothing ==
    crashed =>
        /\ \A s \in Stores : Known(s) \cap wiped = {}
        /\ \A r \in NativeRequests : replay[r] # "saved"

RequestsSettle == \A r \in Requests :
    status[r] \in {"pending", "answered"} ~> status[r] \in Final

NativeValuesErased == foregroundDone ~> Known("native") = {}

OpenCodeValuesErased == \A e \in Runs :
    e \in ranOut ~> \A v \in Known("opencode") : v[1] # e
=============================================================================
