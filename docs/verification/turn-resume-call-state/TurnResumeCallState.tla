------------------------- MODULE TurnResumeCallState -------------------------
(* The tool calls of one model response inside a RocketClaw turn
   (internal/rocketclaw/harness/looper.go dispatchToolCalls, replayCall,
   recordCall), with the server stopping between any two database writes and
   the turn resuming on the next start.

   Two stores are written, never together:
   - the journal (steps rows): per call, an empty "started" step, then the
     call's output and its state (callStep.State);
   - the visible trace (turns.output_trace_json, SaveTrace): the whole
     in-memory trace of the run, rewritten on every observe/finishCall.
   A resumed run starts its in-memory trace from the trace saved with the
   turn (saveTurn), which, while the response's calls are pending, holds none
   of them; its first SaveTrace therefore drops calls it has not observed yet.

   Variant switches (TRUE is the committed shape for AbortedState,
   BackgroundState and IdleStopSettles; RecordFirst is a proposed shape and
   PreFix the code before calls journaled their state):
   - PreFix: a resumed turn shows every journaled call completed.
   - AbortedState: a non-resumable call cut off by the stop is journaled with
     state "stopped" (FALSE: with no state, so a later resume reads it as
     an older build's step and shows it completed).
   - BackgroundState: a call moved to the background is journaled with state
     "background" (FALSE: "completed", or "stopped" when the turn was
     stopped before the call returned).
   - RecordFirst: the output is journaled before the final state is shown
     (as built: finishCall, then recordCall).
   - IdleStopSettles: stopping a turn nobody runs (stopIdleTurn) settles
     every call still shown working or in review (FALSE: left as is). *)
EXTENDS Naturals, FiniteSets

CONSTANTS Calls, Resumable, BgCalls, MaxCrashes,
          OldBuildFirst, UserStop, Cancels, IdleStop,
          PreFix, AbortedState, BackgroundState, RecordFirst, IdleStopSettles

ASSUME Resumable \subseteq Calls /\ BgCalls \subseteq Resumable /\ MaxCrashes \in Nat
ASSUME \A flag \in {OldBuildFirst, UserStop, Cancels, IdleStop, PreFix, AbortedState,
                    BackgroundState, RecordFirst, IdleStopSettles} : flag \in BOOLEAN

Final == {"completed", "failed", "stopped", "background"}
Running == {"review", "working"}

(* --algorithm TurnResumeCallState
variables
    jStarted = [c \in Calls |-> FALSE],
    jOut = [c \in Calls |-> "none"],
    jState = [c \in Calls |-> "none"],
    outputsSaved = FALSE,
    savedTrace = [c \in Calls |-> "none"],
    terminal = "none",
    visible = [c \in Calls |-> "none"],
    jobRow = [c \in Calls |-> FALSE],
    build = IF OldBuildFirst THEN "old" ELSE "new",
    mem = [c \in Calls |-> "none"],
    observed = [c \in Calls |-> FALSE],
    cs = [c \in Calls |-> "idle"],
    outcome = [c \in Calls |-> "none"],
    replayAs = [c \in Calls |-> "none"],
    stopReq = FALSE,
    shuttingDown = FALSE,
    errStop = FALSE,
    fresh = FALSE,
    crashes = 0,
    finishedAs = [c \in Calls |-> "none"],
    legacy = [c \in Calls |-> FALSE],
    replayedAs = [c \in Calls |-> "none"],
    replays = [c \in Calls |-> 0],
    firstFinal = [c \in Calls |-> "none"],
    execs = [c \in Calls |-> 0],
    rerunAfterJournal = FALSE,
    reusedRow = FALSE,
    jobsStarted = [c \in Calls |-> 0];

define
    OutcomeState(o) == CASE o = "completed" -> "completed"
                         [] o = "failed" -> "failed"
                         [] o = "bg" -> "completed"
                         [] OTHER -> "stopped"

    JournalState(c) == IF jState[c] = "none" THEN "completed" ELSE jState[c]

    ReplayState(c) == IF build = "old" \/ PreFix THEN "completed" ELSE JournalState(c)

    RecordedState(c) ==
        IF build = "old" THEN "none"
        ELSE IF outcome[c] = "bg" /\ BackgroundState THEN "background"
        ELSE IF outcome[c] = "bg" /\ stopReq THEN "stopped"
        ELSE OutcomeState(outcome[c])

    ShownAtFinish(c) == IF outcome[c] = "bg" THEN "background" ELSE OutcomeState(outcome[c])

    Outcomes(c) ==
        IF jobRow[c] THEN {"bg"}
        ELSE IF stopReq THEN {"stopErr", "stopNil"}
        ELSE IF shuttingDown /\ c \in Resumable THEN {"cancel", "stopNil"}
        ELSE {"completed", "failed"}
             \cup (IF c \in BgCalls THEN {"bg"} ELSE {})
             \cup (IF Cancels /\ c \in Resumable /\ crashes < MaxCrashes THEN {"cancel"} ELSE {})

    Unjournaled(o) == o \in {"stopErr", "cancel"}

    AfterRun(c, o) ==
        IF o = "bg" THEN "bgStart"
        ELSE IF RecordFirst /\ ~Unjournaled(o) THEN "record"
        ELSE "finish"

    AfterShow(c) ==
        IF Unjournaled(outcome[c]) \/ RecordFirst THEN "done" ELSE "record"

    IdleSettled(c) ==
        IF visible[c] \notin Running THEN visible[c]
        ELSE IF jOut[c] = "none" THEN "stopped"
        ELSE JournalState(c)

    TypeOK ==
        /\ jStarted \in [Calls -> BOOLEAN]
        /\ jOut \in [Calls -> {"none", "real", "aborted"}]
        /\ jState \in [Calls -> {"none"} \cup Final]
        /\ visible \in [Calls -> {"none"} \cup Running \cup Final]
        /\ mem \in [Calls -> {"none"} \cup Running \cup Final]
        /\ terminal \in {"none", "complete", "stopped"}
        /\ cs \in [Calls -> {"idle", "working", "replay", "run", "bgStart", "bgShow", "finish", "record", "done"}]
        /\ crashes \in 0..MaxCrashes
        /\ execs \in [Calls -> 0..(MaxCrashes + 1)]

    KeepsFinishedState ==
        \A c \in Calls : jOut[c] # "none" /\ ~legacy[c] /\ visible[c] \in Final
            => visible[c] = finishedAs[c]

    NoReexecution == ~rerunAfterJournal

    OneBackgroundJob == \A c \in Calls : jobsStarted[c] <= 1

    JournaledNeverRunning ==
        \A c \in Calls : terminal # "none" /\ jOut[c] # "none" => visible[c] \notin Running

    NoRunningAfterTurn == terminal # "none" => \A c \in Calls : visible[c] \notin Running

    LegacyResumesCompleted ==
        \A c \in Calls : legacy[c] /\ replayedAs[c] # "none" => replayedAs[c] = "completed"

    VisibleFinalKept ==
        \A c \in Calls : visible[c] \in Final => visible[c] = firstFinal[c]

    TurnEnds == <>(terminal # "none")

    NeverReplayedFailed == \A c \in Calls : replayedAs[c] # "failed"
    NeverReplayedBackground == \A c \in Calls : replayedAs[c] # "background"
    NeverStoppedReplayedTwice == \A c \in Calls : ~(replays[c] >= 2 /\ replayedAs[c] = "stopped")
    NeverRerun == \A c \in Calls : execs[c] <= 1
    NeverReusedJobRow == ~reusedRow
    NeverLegacyFailedResumed ==
        \A c \in Calls : ~(legacy[c] /\ finishedAs[c] = "failed" /\ replayedAs[c] = "completed")
    NeverUserStopped == terminal # "stopped"
end define;

macro Show(c, s) begin
    if mem[c] # "background" then
        with m = [mem EXCEPT ![c] = s] do
            mem := m;
            visible := m;
        end with;
        if firstFinal[c] = "none" /\ s \in Final then
            firstFinal[c] := s;
        end if;
    end if;
end macro;

fair process server = "server"
begin
Step:
    while terminal = "none" do
        either
            await crashes < MaxCrashes;
            crashes := crashes + 1;
            build := "new";
            mem := IF outputsSaved THEN savedTrace ELSE [c \in Calls |-> "none"];
            observed := [c \in Calls |-> FALSE];
            cs := [c \in Calls |-> "idle"];
            outcome := [c \in Calls |-> "none"];
            replayAs := [c \in Calls |-> "none"];
            stopReq := FALSE;
            shuttingDown := FALSE;
            errStop := FALSE;
            fresh := TRUE;
        or
            await UserStop /\ ~stopReq /\ ~shuttingDown;
            stopReq := TRUE;
            fresh := FALSE;
        or
            await IdleStop /\ fresh;
            terminal := "stopped";
            if IdleStopSettles then
                visible := [c \in Calls |-> IdleSettled(c)];
            end if;
        or
            await ~outputsSaved;
            with c \in {d \in Calls : ~observed[d]} do
                observed[c] := TRUE;
                fresh := FALSE;
                Show(c, "review");
            end with;
        or
            await ~outputsSaved /\ \A d \in Calls : observed[d];
            with c \in {d \in Calls : cs[d] # "done"} do
                fresh := FALSE;
                if cs[c] = "idle" then
                    if ~jStarted[c] then
                        jStarted[c] := TRUE;
                        cs[c] := "working";
                    elsif jOut[c] = "none" /\ c \in Resumable then
                        Show(c, "working");
                        cs[c] := "run";
                    elsif jOut[c] = "none" then
                        jOut[c] := "aborted";
                        jState[c] := IF build = "new" /\ AbortedState THEN "stopped" ELSE "none";
                        finishedAs[c] := "stopped";
                        legacy[c] := build = "old";
                        replayAs[c] := "stopped";
                        cs[c] := "replay";
                    else
                        replayAs[c] := ReplayState(c);
                        cs[c] := "replay";
                    end if;
                elsif cs[c] = "working" then
                    Show(c, "working");
                    cs[c] := "run";
                elsif cs[c] = "replay" then
                    Show(c, replayAs[c]);
                    if build = "new" then
                        replayedAs[c] := replayAs[c];
                        replays[c] := replays[c] + 1;
                    end if;
                    cs[c] := "done";
                elsif cs[c] = "run" then
                    execs[c] := execs[c] + 1;
                    rerunAfterJournal := rerunAfterJournal \/ jOut[c] # "none";
                    with o \in Outcomes(c) do
                        outcome[c] := o;
                        cs[c] := AfterRun(c, o);
                        if o = "cancel" then
                            shuttingDown := TRUE;
                        end if;
                    end with;
                elsif cs[c] = "bgStart" then
                    if ~jobRow[c] then
                        jobRow[c] := TRUE;
                        jobsStarted[c] := jobsStarted[c] + 1;
                        cs[c] := "bgShow";
                    else
                        reusedRow := TRUE;
                        Show(c, "background");
                        cs[c] := IF RecordFirst THEN "record" ELSE "finish";
                    end if;
                elsif cs[c] = "bgShow" then
                    Show(c, "background");
                    cs[c] := IF RecordFirst THEN "record" ELSE "finish";
                elsif cs[c] = "finish" then
                    Show(c, OutcomeState(outcome[c]));
                    if Unjournaled(outcome[c]) then
                        errStop := TRUE;
                    end if;
                    cs[c] := AfterShow(c);
                else
                    jOut[c] := "real";
                    jState[c] := RecordedState(c);
                    legacy[c] := build = "old";
                    finishedAs[c] := ShownAtFinish(c);
                    cs[c] := IF RecordFirst THEN "finish" ELSE "done";
                end if;
            end with;
        or
            await ~outputsSaved /\ \A c \in Calls : cs[c] = "done";
            await ~errStop \/ (stopReq /\ ~shuttingDown);
            if errStop then
                terminal := "stopped";
            else
                outputsSaved := TRUE;
                savedTrace := mem;
                fresh := FALSE;
            end if;
        or
            await outputsSaved;
            terminal := IF stopReq THEN "stopped" ELSE "complete";
            if ~stopReq then
                visible := mem;
            end if;
        end either;
    end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "7517042a" /\ chksum(tla) = "e25c5490")
VARIABLES pc, jStarted, jOut, jState, outputsSaved, savedTrace, terminal, 
          visible, jobRow, build, mem, observed, cs, outcome, replayAs, 
          stopReq, shuttingDown, errStop, fresh, crashes, finishedAs, legacy, 
          replayedAs, replays, firstFinal, execs, rerunAfterJournal, 
          reusedRow, jobsStarted

(* define statement *)
OutcomeState(o) == CASE o = "completed" -> "completed"
                     [] o = "failed" -> "failed"
                     [] o = "bg" -> "completed"
                     [] OTHER -> "stopped"

JournalState(c) == IF jState[c] = "none" THEN "completed" ELSE jState[c]

ReplayState(c) == IF build = "old" \/ PreFix THEN "completed" ELSE JournalState(c)

RecordedState(c) ==
    IF build = "old" THEN "none"
    ELSE IF outcome[c] = "bg" /\ BackgroundState THEN "background"
    ELSE IF outcome[c] = "bg" /\ stopReq THEN "stopped"
    ELSE OutcomeState(outcome[c])

ShownAtFinish(c) == IF outcome[c] = "bg" THEN "background" ELSE OutcomeState(outcome[c])

Outcomes(c) ==
    IF jobRow[c] THEN {"bg"}
    ELSE IF stopReq THEN {"stopErr", "stopNil"}
    ELSE IF shuttingDown /\ c \in Resumable THEN {"cancel", "stopNil"}
    ELSE {"completed", "failed"}
         \cup (IF c \in BgCalls THEN {"bg"} ELSE {})
         \cup (IF Cancels /\ c \in Resumable /\ crashes < MaxCrashes THEN {"cancel"} ELSE {})

Unjournaled(o) == o \in {"stopErr", "cancel"}

AfterRun(c, o) ==
    IF o = "bg" THEN "bgStart"
    ELSE IF RecordFirst /\ ~Unjournaled(o) THEN "record"
    ELSE "finish"

AfterShow(c) ==
    IF Unjournaled(outcome[c]) \/ RecordFirst THEN "done" ELSE "record"

IdleSettled(c) ==
    IF visible[c] \notin Running THEN visible[c]
    ELSE IF jOut[c] = "none" THEN "stopped"
    ELSE JournalState(c)

TypeOK ==
    /\ jStarted \in [Calls -> BOOLEAN]
    /\ jOut \in [Calls -> {"none", "real", "aborted"}]
    /\ jState \in [Calls -> {"none"} \cup Final]
    /\ visible \in [Calls -> {"none"} \cup Running \cup Final]
    /\ mem \in [Calls -> {"none"} \cup Running \cup Final]
    /\ terminal \in {"none", "complete", "stopped"}
    /\ cs \in [Calls -> {"idle", "working", "replay", "run", "bgStart", "bgShow", "finish", "record", "done"}]
    /\ crashes \in 0..MaxCrashes
    /\ execs \in [Calls -> 0..(MaxCrashes + 1)]

KeepsFinishedState ==
    \A c \in Calls : jOut[c] # "none" /\ ~legacy[c] /\ visible[c] \in Final
        => visible[c] = finishedAs[c]

NoReexecution == ~rerunAfterJournal

OneBackgroundJob == \A c \in Calls : jobsStarted[c] <= 1

JournaledNeverRunning ==
    \A c \in Calls : terminal # "none" /\ jOut[c] # "none" => visible[c] \notin Running

NoRunningAfterTurn == terminal # "none" => \A c \in Calls : visible[c] \notin Running

LegacyResumesCompleted ==
    \A c \in Calls : legacy[c] /\ replayedAs[c] # "none" => replayedAs[c] = "completed"

VisibleFinalKept ==
    \A c \in Calls : visible[c] \in Final => visible[c] = firstFinal[c]

TurnEnds == <>(terminal # "none")

NeverReplayedFailed == \A c \in Calls : replayedAs[c] # "failed"
NeverReplayedBackground == \A c \in Calls : replayedAs[c] # "background"
NeverStoppedReplayedTwice == \A c \in Calls : ~(replays[c] >= 2 /\ replayedAs[c] = "stopped")
NeverRerun == \A c \in Calls : execs[c] <= 1
NeverReusedJobRow == ~reusedRow
NeverLegacyFailedResumed ==
    \A c \in Calls : ~(legacy[c] /\ finishedAs[c] = "failed" /\ replayedAs[c] = "completed")
NeverUserStopped == terminal # "stopped"


vars == << pc, jStarted, jOut, jState, outputsSaved, savedTrace, terminal, 
           visible, jobRow, build, mem, observed, cs, outcome, replayAs, 
           stopReq, shuttingDown, errStop, fresh, crashes, finishedAs, legacy, 
           replayedAs, replays, firstFinal, execs, rerunAfterJournal, 
           reusedRow, jobsStarted >>

ProcSet == {"server"}

Init == (* Global variables *)
        /\ jStarted = [c \in Calls |-> FALSE]
        /\ jOut = [c \in Calls |-> "none"]
        /\ jState = [c \in Calls |-> "none"]
        /\ outputsSaved = FALSE
        /\ savedTrace = [c \in Calls |-> "none"]
        /\ terminal = "none"
        /\ visible = [c \in Calls |-> "none"]
        /\ jobRow = [c \in Calls |-> FALSE]
        /\ build = IF OldBuildFirst THEN "old" ELSE "new"
        /\ mem = [c \in Calls |-> "none"]
        /\ observed = [c \in Calls |-> FALSE]
        /\ cs = [c \in Calls |-> "idle"]
        /\ outcome = [c \in Calls |-> "none"]
        /\ replayAs = [c \in Calls |-> "none"]
        /\ stopReq = FALSE
        /\ shuttingDown = FALSE
        /\ errStop = FALSE
        /\ fresh = FALSE
        /\ crashes = 0
        /\ finishedAs = [c \in Calls |-> "none"]
        /\ legacy = [c \in Calls |-> FALSE]
        /\ replayedAs = [c \in Calls |-> "none"]
        /\ replays = [c \in Calls |-> 0]
        /\ firstFinal = [c \in Calls |-> "none"]
        /\ execs = [c \in Calls |-> 0]
        /\ rerunAfterJournal = FALSE
        /\ reusedRow = FALSE
        /\ jobsStarted = [c \in Calls |-> 0]
        /\ pc = [self \in ProcSet |-> "Step"]

Step == /\ pc["server"] = "Step"
        /\ IF terminal = "none"
              THEN /\ \/ /\ crashes < MaxCrashes
                         /\ crashes' = crashes + 1
                         /\ build' = "new"
                         /\ mem' = IF outputsSaved THEN savedTrace ELSE [c \in Calls |-> "none"]
                         /\ observed' = [c \in Calls |-> FALSE]
                         /\ cs' = [c \in Calls |-> "idle"]
                         /\ outcome' = [c \in Calls |-> "none"]
                         /\ replayAs' = [c \in Calls |-> "none"]
                         /\ stopReq' = FALSE
                         /\ shuttingDown' = FALSE
                         /\ errStop' = FALSE
                         /\ fresh' = TRUE
                         /\ UNCHANGED <<jStarted, jOut, jState, outputsSaved, savedTrace, terminal, visible, jobRow, finishedAs, legacy, replayedAs, replays, firstFinal, execs, rerunAfterJournal, reusedRow, jobsStarted>>
                      \/ /\ UserStop /\ ~stopReq /\ ~shuttingDown
                         /\ stopReq' = TRUE
                         /\ fresh' = FALSE
                         /\ UNCHANGED <<jStarted, jOut, jState, outputsSaved, savedTrace, terminal, visible, jobRow, build, mem, observed, cs, outcome, replayAs, shuttingDown, errStop, crashes, finishedAs, legacy, replayedAs, replays, firstFinal, execs, rerunAfterJournal, reusedRow, jobsStarted>>
                      \/ /\ IdleStop /\ fresh
                         /\ terminal' = "stopped"
                         /\ IF IdleStopSettles
                               THEN /\ visible' = [c \in Calls |-> IdleSettled(c)]
                               ELSE /\ TRUE
                                    /\ UNCHANGED visible
                         /\ UNCHANGED <<jStarted, jOut, jState, outputsSaved, savedTrace, jobRow, build, mem, observed, cs, outcome, replayAs, stopReq, shuttingDown, errStop, fresh, crashes, finishedAs, legacy, replayedAs, replays, firstFinal, execs, rerunAfterJournal, reusedRow, jobsStarted>>
                      \/ /\ ~outputsSaved
                         /\ \E c \in {d \in Calls : ~observed[d]}:
                              /\ observed' = [observed EXCEPT ![c] = TRUE]
                              /\ fresh' = FALSE
                              /\ IF mem[c] # "background"
                                    THEN /\ LET m == [mem EXCEPT ![c] = "review"] IN
                                              /\ mem' = m
                                              /\ visible' = m
                                         /\ IF firstFinal[c] = "none" /\ "review" \in Final
                                               THEN /\ firstFinal' = [firstFinal EXCEPT ![c] = "review"]
                                               ELSE /\ TRUE
                                                    /\ UNCHANGED firstFinal
                                    ELSE /\ TRUE
                                         /\ UNCHANGED << visible, mem, 
                                                         firstFinal >>
                         /\ UNCHANGED <<jStarted, jOut, jState, outputsSaved, savedTrace, terminal, jobRow, build, cs, outcome, replayAs, stopReq, shuttingDown, errStop, crashes, finishedAs, legacy, replayedAs, replays, execs, rerunAfterJournal, reusedRow, jobsStarted>>
                      \/ /\ ~outputsSaved /\ \A d \in Calls : observed[d]
                         /\ \E c \in {d \in Calls : cs[d] # "done"}:
                              /\ fresh' = FALSE
                              /\ IF cs[c] = "idle"
                                    THEN /\ IF ~jStarted[c]
                                               THEN /\ jStarted' = [jStarted EXCEPT ![c] = TRUE]
                                                    /\ cs' = [cs EXCEPT ![c] = "working"]
                                                    /\ UNCHANGED << jOut, 
                                                                    jState, 
                                                                    visible, 
                                                                    mem, 
                                                                    replayAs, 
                                                                    finishedAs, 
                                                                    legacy, 
                                                                    firstFinal >>
                                               ELSE /\ IF jOut[c] = "none" /\ c \in Resumable
                                                          THEN /\ IF mem[c] # "background"
                                                                     THEN /\ LET m == [mem EXCEPT ![c] = "working"] IN
                                                                               /\ mem' = m
                                                                               /\ visible' = m
                                                                          /\ IF firstFinal[c] = "none" /\ "working" \in Final
                                                                                THEN /\ firstFinal' = [firstFinal EXCEPT ![c] = "working"]
                                                                                ELSE /\ TRUE
                                                                                     /\ UNCHANGED firstFinal
                                                                     ELSE /\ TRUE
                                                                          /\ UNCHANGED << visible, 
                                                                                          mem, 
                                                                                          firstFinal >>
                                                               /\ cs' = [cs EXCEPT ![c] = "run"]
                                                               /\ UNCHANGED << jOut, 
                                                                               jState, 
                                                                               replayAs, 
                                                                               finishedAs, 
                                                                               legacy >>
                                                          ELSE /\ IF jOut[c] = "none"
                                                                     THEN /\ jOut' = [jOut EXCEPT ![c] = "aborted"]
                                                                          /\ jState' = [jState EXCEPT ![c] = IF build = "new" /\ AbortedState THEN "stopped" ELSE "none"]
                                                                          /\ finishedAs' = [finishedAs EXCEPT ![c] = "stopped"]
                                                                          /\ legacy' = [legacy EXCEPT ![c] = build = "old"]
                                                                          /\ replayAs' = [replayAs EXCEPT ![c] = "stopped"]
                                                                          /\ cs' = [cs EXCEPT ![c] = "replay"]
                                                                     ELSE /\ replayAs' = [replayAs EXCEPT ![c] = ReplayState(c)]
                                                                          /\ cs' = [cs EXCEPT ![c] = "replay"]
                                                                          /\ UNCHANGED << jOut, 
                                                                                          jState, 
                                                                                          finishedAs, 
                                                                                          legacy >>
                                                               /\ UNCHANGED << visible, 
                                                                               mem, 
                                                                               firstFinal >>
                                                    /\ UNCHANGED jStarted
                                         /\ UNCHANGED << jobRow, outcome, 
                                                         shuttingDown, errStop, 
                                                         replayedAs, replays, 
                                                         execs, 
                                                         rerunAfterJournal, 
                                                         reusedRow, 
                                                         jobsStarted >>
                                    ELSE /\ IF cs[c] = "working"
                                               THEN /\ IF mem[c] # "background"
                                                          THEN /\ LET m == [mem EXCEPT ![c] = "working"] IN
                                                                    /\ mem' = m
                                                                    /\ visible' = m
                                                               /\ IF firstFinal[c] = "none" /\ "working" \in Final
                                                                     THEN /\ firstFinal' = [firstFinal EXCEPT ![c] = "working"]
                                                                     ELSE /\ TRUE
                                                                          /\ UNCHANGED firstFinal
                                                          ELSE /\ TRUE
                                                               /\ UNCHANGED << visible, 
                                                                               mem, 
                                                                               firstFinal >>
                                                    /\ cs' = [cs EXCEPT ![c] = "run"]
                                                    /\ UNCHANGED << jOut, 
                                                                    jState, 
                                                                    jobRow, 
                                                                    outcome, 
                                                                    shuttingDown, 
                                                                    errStop, 
                                                                    finishedAs, 
                                                                    legacy, 
                                                                    replayedAs, 
                                                                    replays, 
                                                                    execs, 
                                                                    rerunAfterJournal, 
                                                                    reusedRow, 
                                                                    jobsStarted >>
                                               ELSE /\ IF cs[c] = "replay"
                                                          THEN /\ IF mem[c] # "background"
                                                                     THEN /\ LET m == [mem EXCEPT ![c] = (replayAs[c])] IN
                                                                               /\ mem' = m
                                                                               /\ visible' = m
                                                                          /\ IF firstFinal[c] = "none" /\ (replayAs[c]) \in Final
                                                                                THEN /\ firstFinal' = [firstFinal EXCEPT ![c] = replayAs[c]]
                                                                                ELSE /\ TRUE
                                                                                     /\ UNCHANGED firstFinal
                                                                     ELSE /\ TRUE
                                                                          /\ UNCHANGED << visible, 
                                                                                          mem, 
                                                                                          firstFinal >>
                                                               /\ IF build = "new"
                                                                     THEN /\ replayedAs' = [replayedAs EXCEPT ![c] = replayAs[c]]
                                                                          /\ replays' = [replays EXCEPT ![c] = replays[c] + 1]
                                                                     ELSE /\ TRUE
                                                                          /\ UNCHANGED << replayedAs, 
                                                                                          replays >>
                                                               /\ cs' = [cs EXCEPT ![c] = "done"]
                                                               /\ UNCHANGED << jOut, 
                                                                               jState, 
                                                                               jobRow, 
                                                                               outcome, 
                                                                               shuttingDown, 
                                                                               errStop, 
                                                                               finishedAs, 
                                                                               legacy, 
                                                                               execs, 
                                                                               rerunAfterJournal, 
                                                                               reusedRow, 
                                                                               jobsStarted >>
                                                          ELSE /\ IF cs[c] = "run"
                                                                     THEN /\ execs' = [execs EXCEPT ![c] = execs[c] + 1]
                                                                          /\ rerunAfterJournal' = (rerunAfterJournal \/ jOut[c] # "none")
                                                                          /\ \E o \in Outcomes(c):
                                                                               /\ outcome' = [outcome EXCEPT ![c] = o]
                                                                               /\ cs' = [cs EXCEPT ![c] = AfterRun(c, o)]
                                                                               /\ IF o = "cancel"
                                                                                     THEN /\ shuttingDown' = TRUE
                                                                                     ELSE /\ TRUE
                                                                                          /\ UNCHANGED shuttingDown
                                                                          /\ UNCHANGED << jOut, 
                                                                                          jState, 
                                                                                          visible, 
                                                                                          jobRow, 
                                                                                          mem, 
                                                                                          errStop, 
                                                                                          finishedAs, 
                                                                                          legacy, 
                                                                                          firstFinal, 
                                                                                          reusedRow, 
                                                                                          jobsStarted >>
                                                                     ELSE /\ IF cs[c] = "bgStart"
                                                                                THEN /\ IF ~jobRow[c]
                                                                                           THEN /\ jobRow' = [jobRow EXCEPT ![c] = TRUE]
                                                                                                /\ jobsStarted' = [jobsStarted EXCEPT ![c] = jobsStarted[c] + 1]
                                                                                                /\ cs' = [cs EXCEPT ![c] = "bgShow"]
                                                                                                /\ UNCHANGED << visible, 
                                                                                                                mem, 
                                                                                                                firstFinal, 
                                                                                                                reusedRow >>
                                                                                           ELSE /\ reusedRow' = TRUE
                                                                                                /\ IF mem[c] # "background"
                                                                                                      THEN /\ LET m == [mem EXCEPT ![c] = "background"] IN
                                                                                                                /\ mem' = m
                                                                                                                /\ visible' = m
                                                                                                           /\ IF firstFinal[c] = "none" /\ "background" \in Final
                                                                                                                 THEN /\ firstFinal' = [firstFinal EXCEPT ![c] = "background"]
                                                                                                                 ELSE /\ TRUE
                                                                                                                      /\ UNCHANGED firstFinal
                                                                                                      ELSE /\ TRUE
                                                                                                           /\ UNCHANGED << visible, 
                                                                                                                           mem, 
                                                                                                                           firstFinal >>
                                                                                                /\ cs' = [cs EXCEPT ![c] = IF RecordFirst THEN "record" ELSE "finish"]
                                                                                                /\ UNCHANGED << jobRow, 
                                                                                                                jobsStarted >>
                                                                                     /\ UNCHANGED << jOut, 
                                                                                                     jState, 
                                                                                                     errStop, 
                                                                                                     finishedAs, 
                                                                                                     legacy >>
                                                                                ELSE /\ IF cs[c] = "bgShow"
                                                                                           THEN /\ IF mem[c] # "background"
                                                                                                      THEN /\ LET m == [mem EXCEPT ![c] = "background"] IN
                                                                                                                /\ mem' = m
                                                                                                                /\ visible' = m
                                                                                                           /\ IF firstFinal[c] = "none" /\ "background" \in Final
                                                                                                                 THEN /\ firstFinal' = [firstFinal EXCEPT ![c] = "background"]
                                                                                                                 ELSE /\ TRUE
                                                                                                                      /\ UNCHANGED firstFinal
                                                                                                      ELSE /\ TRUE
                                                                                                           /\ UNCHANGED << visible, 
                                                                                                                           mem, 
                                                                                                                           firstFinal >>
                                                                                                /\ cs' = [cs EXCEPT ![c] = IF RecordFirst THEN "record" ELSE "finish"]
                                                                                                /\ UNCHANGED << jOut, 
                                                                                                                jState, 
                                                                                                                errStop, 
                                                                                                                finishedAs, 
                                                                                                                legacy >>
                                                                                           ELSE /\ IF cs[c] = "finish"
                                                                                                      THEN /\ IF mem[c] # "background"
                                                                                                                 THEN /\ LET m == [mem EXCEPT ![c] = (OutcomeState(outcome[c]))] IN
                                                                                                                           /\ mem' = m
                                                                                                                           /\ visible' = m
                                                                                                                      /\ IF firstFinal[c] = "none" /\ (OutcomeState(outcome[c])) \in Final
                                                                                                                            THEN /\ firstFinal' = [firstFinal EXCEPT ![c] = OutcomeState(outcome[c])]
                                                                                                                            ELSE /\ TRUE
                                                                                                                                 /\ UNCHANGED firstFinal
                                                                                                                 ELSE /\ TRUE
                                                                                                                      /\ UNCHANGED << visible, 
                                                                                                                                      mem, 
                                                                                                                                      firstFinal >>
                                                                                                           /\ IF Unjournaled(outcome[c])
                                                                                                                 THEN /\ errStop' = TRUE
                                                                                                                 ELSE /\ TRUE
                                                                                                                      /\ UNCHANGED errStop
                                                                                                           /\ cs' = [cs EXCEPT ![c] = AfterShow(c)]
                                                                                                           /\ UNCHANGED << jOut, 
                                                                                                                           jState, 
                                                                                                                           finishedAs, 
                                                                                                                           legacy >>
                                                                                                      ELSE /\ jOut' = [jOut EXCEPT ![c] = "real"]
                                                                                                           /\ jState' = [jState EXCEPT ![c] = RecordedState(c)]
                                                                                                           /\ legacy' = [legacy EXCEPT ![c] = build = "old"]
                                                                                                           /\ finishedAs' = [finishedAs EXCEPT ![c] = ShownAtFinish(c)]
                                                                                                           /\ cs' = [cs EXCEPT ![c] = IF RecordFirst THEN "finish" ELSE "done"]
                                                                                                           /\ UNCHANGED << visible, 
                                                                                                                           mem, 
                                                                                                                           errStop, 
                                                                                                                           firstFinal >>
                                                                                     /\ UNCHANGED << jobRow, 
                                                                                                     reusedRow, 
                                                                                                     jobsStarted >>
                                                                          /\ UNCHANGED << outcome, 
                                                                                          shuttingDown, 
                                                                                          execs, 
                                                                                          rerunAfterJournal >>
                                                               /\ UNCHANGED << replayedAs, 
                                                                               replays >>
                                         /\ UNCHANGED << jStarted, replayAs >>
                         /\ UNCHANGED <<outputsSaved, savedTrace, terminal, build, observed, stopReq, crashes>>
                      \/ /\ ~outputsSaved /\ \A c \in Calls : cs[c] = "done"
                         /\ ~errStop \/ (stopReq /\ ~shuttingDown)
                         /\ IF errStop
                               THEN /\ terminal' = "stopped"
                                    /\ UNCHANGED << outputsSaved, savedTrace, 
                                                    fresh >>
                               ELSE /\ outputsSaved' = TRUE
                                    /\ savedTrace' = mem
                                    /\ fresh' = FALSE
                                    /\ UNCHANGED terminal
                         /\ UNCHANGED <<jStarted, jOut, jState, visible, jobRow, build, mem, observed, cs, outcome, replayAs, stopReq, shuttingDown, errStop, crashes, finishedAs, legacy, replayedAs, replays, firstFinal, execs, rerunAfterJournal, reusedRow, jobsStarted>>
                      \/ /\ outputsSaved
                         /\ terminal' = IF stopReq THEN "stopped" ELSE "complete"
                         /\ IF ~stopReq
                               THEN /\ visible' = mem
                               ELSE /\ TRUE
                                    /\ UNCHANGED visible
                         /\ UNCHANGED <<jStarted, jOut, jState, outputsSaved, savedTrace, jobRow, build, mem, observed, cs, outcome, replayAs, stopReq, shuttingDown, errStop, fresh, crashes, finishedAs, legacy, replayedAs, replays, firstFinal, execs, rerunAfterJournal, reusedRow, jobsStarted>>
                   /\ pc' = [pc EXCEPT !["server"] = "Step"]
              ELSE /\ pc' = [pc EXCEPT !["server"] = "Done"]
                   /\ UNCHANGED << jStarted, jOut, jState, outputsSaved, 
                                   savedTrace, terminal, visible, jobRow, 
                                   build, mem, observed, cs, outcome, replayAs, 
                                   stopReq, shuttingDown, errStop, fresh, 
                                   crashes, finishedAs, legacy, replayedAs, 
                                   replays, firstFinal, execs, 
                                   rerunAfterJournal, reusedRow, jobsStarted >>

server == Step

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == server
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(server)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION 
=============================================================================
