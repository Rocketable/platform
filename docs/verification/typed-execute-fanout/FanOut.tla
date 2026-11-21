------------------------------- MODULE FanOut -------------------------------
EXTENDS Naturals, FiniteSets, Sequences

CONSTANTS N, Limit, Slots, Op, Nested, Cancelable, Review, HangFrom,
          RecordCancelled, CheckBeforeCall, CancelSiblings, CrashIsFailure,
          RaceWaitsForSuccess, ReleaseWhileNested, FirstInTime

ASSUME N \in Nat \ {0} /\ Limit \in 1..N /\ Slots \in Nat \ {0} /\ HangFrom \in 1..(N + 2)
ASSUME Op \in {"gather", "try", "race", "race_first"}
ASSUME {Nested, Cancelable, Review, RecordCancelled, CheckBeforeCall, CancelSiblings,
        CrashIsFailure, RaceWaitsForSuccess, ReleaseWhileNested, FirstInTime} \subseteq BOOLEAN

MainId == 0
Branches == 1..N
ChildId == N + 1
TurnId == N + 2
Procs == 1..(N + 1)

IsRace == Op \in {"race", "race_first"}
IsFailure(v) == v \in {"failure", "crash"}
Min(S) == CHOOSE x \in S : \A y \in S : x <= y
Hangs(p) == p >= HangFrom

(* --algorithm FanOut
variables
    turnCancelled = FALSE,
    groupCancelled = FALSE,
    active = 0,
    slotsHeld = 0,
    holder = [p \in Procs |-> FALSE],
    phase = [p \in Procs |-> "idle"],
    groupErr = "none",
    decided = FALSE,
    firstFailure = 0,
    settled = FALSE,
    winner = "none",
    winnerFrom = 0,
    firstFinish = 0,
    results = [b \in Branches |-> "none"],
    writes = [b \in Branches |-> 0],
    callRec = [p \in Procs |-> "none"],
    review = [p \in Procs |-> "none"],
    endedBy = [p \in Procs |-> "none"],
    startedAfterDecision = FALSE,
    callStartedCancelled = FALSE,
    childDone = FALSE,
    result = "pending",
    resultFrom = 0;

define
    Cancelled == turnCancelled \/ groupCancelled
    MainDone == pc[MainId] = "Done"
    CommittedFailures == {b \in Branches : IsFailure(results[b])}

    TypeOK ==
        /\ turnCancelled \in BOOLEAN
        /\ groupCancelled \in BOOLEAN
        /\ active \in 0..N
        /\ slotsHeld \in 0..(N + 1)
        /\ holder \in [Procs -> BOOLEAN]
        /\ phase \in [Procs -> {"idle", "started", "done"}]
        /\ groupErr \in {"none", "failed", "goerr"}
        /\ decided \in BOOLEAN
        /\ firstFailure \in 0..N
        /\ settled \in BOOLEAN
        /\ winner \in {"none", "success", "failure", "crash", "goerr"}
        /\ winnerFrom \in 0..N
        /\ firstFinish \in 0..N
        /\ results \in [Branches -> {"none", "success", "failure", "crash"}]
        /\ writes \in [Branches -> 0..2]
        /\ callRec \in [Procs -> {"none", "running", "ok", "error", "denied", "cancelled"}]
        /\ review \in [Procs -> {"none", "reviewing", "allow", "deny", "cancelled"}]
        /\ endedBy \in [Procs -> {"none", "tool", "guardian", "cancel"}]
        /\ startedAfterDecision \in BOOLEAN
        /\ callStartedCancelled \in BOOLEAN
        /\ childDone \in BOOLEAN
        /\ result \in {"pending", "list", "success", "failure", "crash", "stopped"}
        /\ resultFrom \in 0..N

    SlotCap == slotsHeld <= Slots /\ slotsHeld = Cardinality({p \in Procs : holder[p]})

    GroupLimit == active <= Limit

    OneResultPerSlot ==
        /\ \A b \in Branches : writes[b] <= 1
        /\ MainDone /\ result = "list" => \A b \in Branches : results[b] # "none"

    GatherCompletes == Op = "gather" /\ MainDone /\ ~turnCancelled => result = "list"

    StopsOnlyOnCancel == MainDone /\ result = "stopped" => turnCancelled

    TryReturnsFirstFailure ==
        Op = "try" /\ MainDone =>
            /\ IsFailure(result) => resultFrom = firstFailure
            /\ result = "list" => \A b \in Branches : results[b] = "success"
            /\ result # "stopped" /\ CommittedFailures # {} => IsFailure(result)

    NoBranchStartsAfterDecision == ~startedAfterDecision

    NoCallStartsAfterCancel == ~callStartedCancelled

    CancelledCallsRecordedCancelled ==
        \A p \in Procs : endedBy[p] = "cancel" => callRec[p] = "cancelled" /\ review[p] # "deny"

    DeniedOnlyByGuardian == \A p \in Procs : callRec[p] = "denied" => endedBy[p] = "guardian"

    AllStoppedWhenDone ==
        MainDone => \A p \in Procs : phase[p] # "started" /\ callRec[p] # "running" /\ review[p] # "reviewing"

    RaceNoFailureWhileSuccess ==
        Op = "race" /\ MainDone =>
            /\ (\E b \in Branches : results[b] = "success") => result \in {"success", "stopped"}
            /\ IsFailure(result) /\ ~turnCancelled => \A b \in Branches : IsFailure(results[b])

    RaceFirstReturnsFirstFinished ==
        Op = "race_first" /\ MainDone /\ result # "stopped" => resultFrom = firstFinish

    Terminates == <>MainDone

    TurnCancelStops == turnCancelled ~> MainDone

    DecisionStops == decided ~> MainDone

    NeverCancelledInReview ==
        \A p \in Procs : ~(endedBy[p] = "cancel" /\ review[p] \in {"cancelled", "deny"})
    NeverCancelledWhileRunning ==
        \A p \in Procs : ~(endedBy[p] = "cancel" /\ review[p] \in {"none", "allow"})
    NeverFirstAboveLowest == ~(Op = "try" /\ MainDone /\ IsFailure(result) /\ resultFrom # Min(CommittedFailures))
    NeverGatherCrashInList ==
        ~(MainDone /\ result = "list" /\ \E b \in Branches : results[b] = "crash")
    NeverRaceLossThenWin ==
        ~(Op = "race" /\ MainDone /\ result = "success" /\ IsFailure(results[firstFinish]))
    NeverRaceAllLose == ~(Op = "race" /\ MainDone /\ IsFailure(result))
    NeverSlotHandOff == ~(Nested /\ pc[1] = "BReacquire" /\ slotsHeld = Slots)
end define;

procedure ToolCall()
begin
ToolCheck:
    if Cancelled then
        if CheckBeforeCall then
            return;
        else
            either
                return;
            or
                callStartedCancelled := TRUE;
                callRec[self] := "running";
                review[self] := IF Review THEN "reviewing" ELSE "none";
            end either;
        end if;
    else
        callRec[self] := "running";
        review[self] := IF Review THEN "reviewing" ELSE "none";
    end if;
ReviewEnd:
    if Review then
        either
            await Cancelled;
            endedBy[self] := "cancel";
            if RecordCancelled then
                review[self] := "cancelled";
                callRec[self] := "cancelled";
            else
                review[self] := "deny";
                callRec[self] := "denied";
            end if;
            return;
        or
            await ~Hangs(self);
            review[self] := "deny";
            callRec[self] := "denied";
            endedBy[self] := "guardian";
            return;
        or
            await ~Hangs(self);
            review[self] := "allow";
        end either;
    end if;
Running:
    either
        await Cancelled;
        callRec[self] := IF RecordCancelled THEN "cancelled" ELSE "error";
        endedBy[self] := "cancel";
    or
        await ~Hangs(self);
        callRec[self] := "ok";
        endedBy[self] := "tool";
    or
        await ~Hangs(self);
        callRec[self] := "error";
        endedBy[self] := "tool";
    end either;
    return;
end procedure;

fair process Main = MainId
variables i = 1;
begin
MSpawn:
    while i <= N do
        if IsRace /\ Cancelled then
            goto MWait;
        end if;
MGo:
        await active < Limit;
        active := active + 1;
        phase[i] := "started";
        i := i + 1;
    end while;
MWait:
    await \A b \in 1..(i - 1) : phase[b] = "done";
    if IsRace then
        if winner \in {"none", "goerr"} then
            result := "stopped";
        else
            result := winner;
            resultFrom := winnerFrom;
        end if;
    elsif groupErr = "goerr" then
        result := "stopped";
    elsif groupErr = "failed" then
        result := results[IF FirstInTime THEN firstFailure ELSE Min(CommittedFailures)];
        resultFrom := IF FirstInTime THEN firstFailure ELSE Min(CommittedFailures);
    else
        result := "list";
    end if;
end process;

fair process Branch \in Branches
variables r = "none", goErr = FALSE;
begin
BSpawned:
    await phase[self] = "started";
BAcquire:
    either
        await Cancelled;
        goErr := TRUE;
        goto BDone;
    or
        await ~Cancelled /\ slotsHeld < Slots;
        slotsHeld := slotsHeld + 1;
        holder[self] := TRUE;
    end either;
BPreCheck:
    if Cancelled then
        goErr := TRUE;
        goto BCommit;
    elsif Nested /\ self = 1 then
        startedAfterDecision := startedAfterDecision \/ decided;
        if ReleaseWhileNested then
            slotsHeld := slotsHeld - 1;
            holder[self] := FALSE;
        end if;
        phase[ChildId] := "started";
        goto BNestWait;
    else
        startedAfterDecision := startedAfterDecision \/ decided;
        call ToolCall();
        goto BOutcome;
    end if;
BNestWait:
    await childDone;
BReacquire:
    if ReleaseWhileNested then
        await slotsHeld < Slots;
        slotsHeld := slotsHeld + 1;
        holder[self] := TRUE;
    end if;
BOutcome:
    if Cancelled then
        goErr := TRUE;
    else
        either
            r := "success";
        or
            r := "failure";
        or
            if CrashIsFailure then
                r := "crash";
            else
                goErr := TRUE;
            end if;
        end either;
    end if;
BCommit:
    if ~goErr then
        results[self] := r;
        writes[self] := writes[self] + 1;
        if IsFailure(r) /\ firstFailure = 0 then
            firstFailure := self;
        end if;
    end if;
    if IsRace /\ ~settled then
        if firstFinish = 0 then
            firstFinish := self;
        end if;
        if Op = "race_first" \/ ~RaceWaitsForSuccess \/ (~goErr /\ r = "success") then
            settled := TRUE;
            decided := TRUE;
            winner := IF goErr THEN "goerr" ELSE r;
            winnerFrom := self;
            groupCancelled := TRUE;
        elsif winner = "none" then
            winner := IF goErr THEN "goerr" ELSE r;
            winnerFrom := self;
        end if;
    end if;
BRelease:
    if holder[self] then
        slotsHeld := slotsHeld - 1;
        holder[self] := FALSE;
    end if;
BDone:
    if ~IsRace /\ groupErr = "none" then
        if goErr then
            groupErr := "goerr";
            groupCancelled := TRUE;
        elsif Op = "try" /\ IsFailure(r) then
            groupErr := "failed";
            decided := TRUE;
            if CancelSiblings then
                groupCancelled := TRUE;
            end if;
        end if;
    end if;
    active := active - 1;
    phase[self] := "done";
end process;

fair process Child = ChildId
begin
CSpawned:
    await phase[ChildId] = "started";
CAcquire:
    either
        await Cancelled;
        goto CDone;
    or
        await ~Cancelled /\ slotsHeld < Slots;
        slotsHeld := slotsHeld + 1;
        holder[ChildId] := TRUE;
    end either;
CPreCheck:
    if Cancelled then
        goto CRelease;
    else
        call ToolCall();
        goto CRelease;
    end if;
CRelease:
    if holder[ChildId] then
        slotsHeld := slotsHeld - 1;
        holder[ChildId] := FALSE;
    end if;
CDone:
    childDone := TRUE;
    phase[ChildId] := "done";
end process;

process Turn = TurnId
begin
TCancel:
    if Cancelable then
        turnCancelled := TRUE;
    end if;
end process;

end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "29f9ca95" /\ chksum(tla) = "ab1ffba2")
VARIABLES pc, turnCancelled, groupCancelled, active, slotsHeld, holder, phase, 
          groupErr, decided, firstFailure, settled, winner, winnerFrom, 
          firstFinish, results, writes, callRec, review, endedBy, 
          startedAfterDecision, callStartedCancelled, childDone, result, 
          resultFrom, stack

(* define statement *)
Cancelled == turnCancelled \/ groupCancelled
MainDone == pc[MainId] = "Done"
CommittedFailures == {b \in Branches : IsFailure(results[b])}

TypeOK ==
    /\ turnCancelled \in BOOLEAN
    /\ groupCancelled \in BOOLEAN
    /\ active \in 0..N
    /\ slotsHeld \in 0..(N + 1)
    /\ holder \in [Procs -> BOOLEAN]
    /\ phase \in [Procs -> {"idle", "started", "done"}]
    /\ groupErr \in {"none", "failed", "goerr"}
    /\ decided \in BOOLEAN
    /\ firstFailure \in 0..N
    /\ settled \in BOOLEAN
    /\ winner \in {"none", "success", "failure", "crash", "goerr"}
    /\ winnerFrom \in 0..N
    /\ firstFinish \in 0..N
    /\ results \in [Branches -> {"none", "success", "failure", "crash"}]
    /\ writes \in [Branches -> 0..2]
    /\ callRec \in [Procs -> {"none", "running", "ok", "error", "denied", "cancelled"}]
    /\ review \in [Procs -> {"none", "reviewing", "allow", "deny", "cancelled"}]
    /\ endedBy \in [Procs -> {"none", "tool", "guardian", "cancel"}]
    /\ startedAfterDecision \in BOOLEAN
    /\ callStartedCancelled \in BOOLEAN
    /\ childDone \in BOOLEAN
    /\ result \in {"pending", "list", "success", "failure", "crash", "stopped"}
    /\ resultFrom \in 0..N

SlotCap == slotsHeld <= Slots /\ slotsHeld = Cardinality({p \in Procs : holder[p]})

GroupLimit == active <= Limit

OneResultPerSlot ==
    /\ \A b \in Branches : writes[b] <= 1
    /\ MainDone /\ result = "list" => \A b \in Branches : results[b] # "none"

GatherCompletes == Op = "gather" /\ MainDone /\ ~turnCancelled => result = "list"

StopsOnlyOnCancel == MainDone /\ result = "stopped" => turnCancelled

TryReturnsFirstFailure ==
    Op = "try" /\ MainDone =>
        /\ IsFailure(result) => resultFrom = firstFailure
        /\ result = "list" => \A b \in Branches : results[b] = "success"
        /\ result # "stopped" /\ CommittedFailures # {} => IsFailure(result)

NoBranchStartsAfterDecision == ~startedAfterDecision

NoCallStartsAfterCancel == ~callStartedCancelled

CancelledCallsRecordedCancelled ==
    \A p \in Procs : endedBy[p] = "cancel" => callRec[p] = "cancelled" /\ review[p] # "deny"

DeniedOnlyByGuardian == \A p \in Procs : callRec[p] = "denied" => endedBy[p] = "guardian"

AllStoppedWhenDone ==
    MainDone => \A p \in Procs : phase[p] # "started" /\ callRec[p] # "running" /\ review[p] # "reviewing"

RaceNoFailureWhileSuccess ==
    Op = "race" /\ MainDone =>
        /\ (\E b \in Branches : results[b] = "success") => result \in {"success", "stopped"}
        /\ IsFailure(result) /\ ~turnCancelled => \A b \in Branches : IsFailure(results[b])

RaceFirstReturnsFirstFinished ==
    Op = "race_first" /\ MainDone /\ result # "stopped" => resultFrom = firstFinish

Terminates == <>MainDone

TurnCancelStops == turnCancelled ~> MainDone

DecisionStops == decided ~> MainDone

NeverCancelledInReview ==
    \A p \in Procs : ~(endedBy[p] = "cancel" /\ review[p] \in {"cancelled", "deny"})
NeverCancelledWhileRunning ==
    \A p \in Procs : ~(endedBy[p] = "cancel" /\ review[p] \in {"none", "allow"})
NeverFirstAboveLowest == ~(Op = "try" /\ MainDone /\ IsFailure(result) /\ resultFrom # Min(CommittedFailures))
NeverGatherCrashInList ==
    ~(MainDone /\ result = "list" /\ \E b \in Branches : results[b] = "crash")
NeverRaceLossThenWin ==
    ~(Op = "race" /\ MainDone /\ result = "success" /\ IsFailure(results[firstFinish]))
NeverRaceAllLose == ~(Op = "race" /\ MainDone /\ IsFailure(result))
NeverSlotHandOff == ~(Nested /\ pc[1] = "BReacquire" /\ slotsHeld = Slots)

VARIABLES i, r, goErr

vars == << pc, turnCancelled, groupCancelled, active, slotsHeld, holder, 
           phase, groupErr, decided, firstFailure, settled, winner, 
           winnerFrom, firstFinish, results, writes, callRec, review, endedBy, 
           startedAfterDecision, callStartedCancelled, childDone, result, 
           resultFrom, stack, i, r, goErr >>

ProcSet == {MainId} \cup (Branches) \cup {ChildId} \cup {TurnId}

Init == (* Global variables *)
        /\ turnCancelled = FALSE
        /\ groupCancelled = FALSE
        /\ active = 0
        /\ slotsHeld = 0
        /\ holder = [p \in Procs |-> FALSE]
        /\ phase = [p \in Procs |-> "idle"]
        /\ groupErr = "none"
        /\ decided = FALSE
        /\ firstFailure = 0
        /\ settled = FALSE
        /\ winner = "none"
        /\ winnerFrom = 0
        /\ firstFinish = 0
        /\ results = [b \in Branches |-> "none"]
        /\ writes = [b \in Branches |-> 0]
        /\ callRec = [p \in Procs |-> "none"]
        /\ review = [p \in Procs |-> "none"]
        /\ endedBy = [p \in Procs |-> "none"]
        /\ startedAfterDecision = FALSE
        /\ callStartedCancelled = FALSE
        /\ childDone = FALSE
        /\ result = "pending"
        /\ resultFrom = 0
        (* Process Main *)
        /\ i = 1
        (* Process Branch *)
        /\ r = [self \in Branches |-> "none"]
        /\ goErr = [self \in Branches |-> FALSE]
        /\ stack = [self \in ProcSet |-> << >>]
        /\ pc = [self \in ProcSet |-> CASE self = MainId -> "MSpawn"
                                        [] self \in Branches -> "BSpawned"
                                        [] self = ChildId -> "CSpawned"
                                        [] self = TurnId -> "TCancel"]

ToolCheck(self) == /\ pc[self] = "ToolCheck"
                   /\ IF Cancelled
                         THEN /\ IF CheckBeforeCall
                                    THEN /\ pc' = [pc EXCEPT ![self] = Head(stack[self]).pc]
                                         /\ stack' = [stack EXCEPT ![self] = Tail(stack[self])]
                                         /\ UNCHANGED << callRec, review, 
                                                         callStartedCancelled >>
                                    ELSE /\ \/ /\ pc' = [pc EXCEPT ![self] = Head(stack[self]).pc]
                                               /\ stack' = [stack EXCEPT ![self] = Tail(stack[self])]
                                               /\ UNCHANGED <<callRec, review, callStartedCancelled>>
                                            \/ /\ callStartedCancelled' = TRUE
                                               /\ callRec' = [callRec EXCEPT ![self] = "running"]
                                               /\ review' = [review EXCEPT ![self] = IF Review THEN "reviewing" ELSE "none"]
                                               /\ pc' = [pc EXCEPT ![self] = "ReviewEnd"]
                                               /\ stack' = stack
                         ELSE /\ callRec' = [callRec EXCEPT ![self] = "running"]
                              /\ review' = [review EXCEPT ![self] = IF Review THEN "reviewing" ELSE "none"]
                              /\ pc' = [pc EXCEPT ![self] = "ReviewEnd"]
                              /\ UNCHANGED << callStartedCancelled, stack >>
                   /\ UNCHANGED << turnCancelled, groupCancelled, active, 
                                   slotsHeld, holder, phase, groupErr, decided, 
                                   firstFailure, settled, winner, winnerFrom, 
                                   firstFinish, results, writes, endedBy, 
                                   startedAfterDecision, childDone, result, 
                                   resultFrom, i, r, goErr >>

ReviewEnd(self) == /\ pc[self] = "ReviewEnd"
                   /\ IF Review
                         THEN /\ \/ /\ Cancelled
                                    /\ endedBy' = [endedBy EXCEPT ![self] = "cancel"]
                                    /\ IF RecordCancelled
                                          THEN /\ review' = [review EXCEPT ![self] = "cancelled"]
                                               /\ callRec' = [callRec EXCEPT ![self] = "cancelled"]
                                          ELSE /\ review' = [review EXCEPT ![self] = "deny"]
                                               /\ callRec' = [callRec EXCEPT ![self] = "denied"]
                                    /\ pc' = [pc EXCEPT ![self] = Head(stack[self]).pc]
                                    /\ stack' = [stack EXCEPT ![self] = Tail(stack[self])]
                                 \/ /\ ~Hangs(self)
                                    /\ review' = [review EXCEPT ![self] = "deny"]
                                    /\ callRec' = [callRec EXCEPT ![self] = "denied"]
                                    /\ endedBy' = [endedBy EXCEPT ![self] = "guardian"]
                                    /\ pc' = [pc EXCEPT ![self] = Head(stack[self]).pc]
                                    /\ stack' = [stack EXCEPT ![self] = Tail(stack[self])]
                                 \/ /\ ~Hangs(self)
                                    /\ review' = [review EXCEPT ![self] = "allow"]
                                    /\ pc' = [pc EXCEPT ![self] = "Running"]
                                    /\ UNCHANGED <<callRec, endedBy, stack>>
                         ELSE /\ pc' = [pc EXCEPT ![self] = "Running"]
                              /\ UNCHANGED << callRec, review, endedBy, stack >>
                   /\ UNCHANGED << turnCancelled, groupCancelled, active, 
                                   slotsHeld, holder, phase, groupErr, decided, 
                                   firstFailure, settled, winner, winnerFrom, 
                                   firstFinish, results, writes, 
                                   startedAfterDecision, callStartedCancelled, 
                                   childDone, result, resultFrom, i, r, goErr >>

Running(self) == /\ pc[self] = "Running"
                 /\ \/ /\ Cancelled
                       /\ callRec' = [callRec EXCEPT ![self] = IF RecordCancelled THEN "cancelled" ELSE "error"]
                       /\ endedBy' = [endedBy EXCEPT ![self] = "cancel"]
                    \/ /\ ~Hangs(self)
                       /\ callRec' = [callRec EXCEPT ![self] = "ok"]
                       /\ endedBy' = [endedBy EXCEPT ![self] = "tool"]
                    \/ /\ ~Hangs(self)
                       /\ callRec' = [callRec EXCEPT ![self] = "error"]
                       /\ endedBy' = [endedBy EXCEPT ![self] = "tool"]
                 /\ pc' = [pc EXCEPT ![self] = Head(stack[self]).pc]
                 /\ stack' = [stack EXCEPT ![self] = Tail(stack[self])]
                 /\ UNCHANGED << turnCancelled, groupCancelled, active, 
                                 slotsHeld, holder, phase, groupErr, decided, 
                                 firstFailure, settled, winner, winnerFrom, 
                                 firstFinish, results, writes, review, 
                                 startedAfterDecision, callStartedCancelled, 
                                 childDone, result, resultFrom, i, r, goErr >>

ToolCall(self) == ToolCheck(self) \/ ReviewEnd(self) \/ Running(self)

MSpawn == /\ pc[MainId] = "MSpawn"
          /\ IF i <= N
                THEN /\ IF IsRace /\ Cancelled
                           THEN /\ pc' = [pc EXCEPT ![MainId] = "MWait"]
                           ELSE /\ pc' = [pc EXCEPT ![MainId] = "MGo"]
                ELSE /\ pc' = [pc EXCEPT ![MainId] = "MWait"]
          /\ UNCHANGED << turnCancelled, groupCancelled, active, slotsHeld, 
                          holder, phase, groupErr, decided, firstFailure, 
                          settled, winner, winnerFrom, firstFinish, results, 
                          writes, callRec, review, endedBy, 
                          startedAfterDecision, callStartedCancelled, 
                          childDone, result, resultFrom, stack, i, r, goErr >>

MGo == /\ pc[MainId] = "MGo"
       /\ active < Limit
       /\ active' = active + 1
       /\ phase' = [phase EXCEPT ![i] = "started"]
       /\ i' = i + 1
       /\ pc' = [pc EXCEPT ![MainId] = "MSpawn"]
       /\ UNCHANGED << turnCancelled, groupCancelled, slotsHeld, holder, 
                       groupErr, decided, firstFailure, settled, winner, 
                       winnerFrom, firstFinish, results, writes, callRec, 
                       review, endedBy, startedAfterDecision, 
                       callStartedCancelled, childDone, result, resultFrom, 
                       stack, r, goErr >>

MWait == /\ pc[MainId] = "MWait"
         /\ \A b \in 1..(i - 1) : phase[b] = "done"
         /\ IF IsRace
               THEN /\ IF winner \in {"none", "goerr"}
                          THEN /\ result' = "stopped"
                               /\ UNCHANGED resultFrom
                          ELSE /\ result' = winner
                               /\ resultFrom' = winnerFrom
               ELSE /\ IF groupErr = "goerr"
                          THEN /\ result' = "stopped"
                               /\ UNCHANGED resultFrom
                          ELSE /\ IF groupErr = "failed"
                                     THEN /\ result' = results[IF FirstInTime THEN firstFailure ELSE Min(CommittedFailures)]
                                          /\ resultFrom' = IF FirstInTime THEN firstFailure ELSE Min(CommittedFailures)
                                     ELSE /\ result' = "list"
                                          /\ UNCHANGED resultFrom
         /\ pc' = [pc EXCEPT ![MainId] = "Done"]
         /\ UNCHANGED << turnCancelled, groupCancelled, active, slotsHeld, 
                         holder, phase, groupErr, decided, firstFailure, 
                         settled, winner, winnerFrom, firstFinish, results, 
                         writes, callRec, review, endedBy, 
                         startedAfterDecision, callStartedCancelled, childDone, 
                         stack, i, r, goErr >>

Main == MSpawn \/ MGo \/ MWait

BSpawned(self) == /\ pc[self] = "BSpawned"
                  /\ phase[self] = "started"
                  /\ pc' = [pc EXCEPT ![self] = "BAcquire"]
                  /\ UNCHANGED << turnCancelled, groupCancelled, active, 
                                  slotsHeld, holder, phase, groupErr, decided, 
                                  firstFailure, settled, winner, winnerFrom, 
                                  firstFinish, results, writes, callRec, 
                                  review, endedBy, startedAfterDecision, 
                                  callStartedCancelled, childDone, result, 
                                  resultFrom, stack, i, r, goErr >>

BAcquire(self) == /\ pc[self] = "BAcquire"
                  /\ \/ /\ Cancelled
                        /\ goErr' = [goErr EXCEPT ![self] = TRUE]
                        /\ pc' = [pc EXCEPT ![self] = "BDone"]
                        /\ UNCHANGED <<slotsHeld, holder>>
                     \/ /\ ~Cancelled /\ slotsHeld < Slots
                        /\ slotsHeld' = slotsHeld + 1
                        /\ holder' = [holder EXCEPT ![self] = TRUE]
                        /\ pc' = [pc EXCEPT ![self] = "BPreCheck"]
                        /\ goErr' = goErr
                  /\ UNCHANGED << turnCancelled, groupCancelled, active, phase, 
                                  groupErr, decided, firstFailure, settled, 
                                  winner, winnerFrom, firstFinish, results, 
                                  writes, callRec, review, endedBy, 
                                  startedAfterDecision, callStartedCancelled, 
                                  childDone, result, resultFrom, stack, i, r >>

BPreCheck(self) == /\ pc[self] = "BPreCheck"
                   /\ IF Cancelled
                         THEN /\ goErr' = [goErr EXCEPT ![self] = TRUE]
                              /\ pc' = [pc EXCEPT ![self] = "BCommit"]
                              /\ UNCHANGED << slotsHeld, holder, phase, 
                                              startedAfterDecision, stack >>
                         ELSE /\ IF Nested /\ self = 1
                                    THEN /\ startedAfterDecision' = (startedAfterDecision \/ decided)
                                         /\ IF ReleaseWhileNested
                                               THEN /\ slotsHeld' = slotsHeld - 1
                                                    /\ holder' = [holder EXCEPT ![self] = FALSE]
                                               ELSE /\ TRUE
                                                    /\ UNCHANGED << slotsHeld, 
                                                                    holder >>
                                         /\ phase' = [phase EXCEPT ![ChildId] = "started"]
                                         /\ pc' = [pc EXCEPT ![self] = "BNestWait"]
                                         /\ stack' = stack
                                    ELSE /\ startedAfterDecision' = (startedAfterDecision \/ decided)
                                         /\ stack' = [stack EXCEPT ![self] = << [ procedure |->  "ToolCall",
                                                                                  pc        |->  "BOutcome" ] >>
                                                                              \o stack[self]]
                                         /\ pc' = [pc EXCEPT ![self] = "ToolCheck"]
                                         /\ UNCHANGED << slotsHeld, holder, 
                                                         phase >>
                              /\ goErr' = goErr
                   /\ UNCHANGED << turnCancelled, groupCancelled, active, 
                                   groupErr, decided, firstFailure, settled, 
                                   winner, winnerFrom, firstFinish, results, 
                                   writes, callRec, review, endedBy, 
                                   callStartedCancelled, childDone, result, 
                                   resultFrom, i, r >>

BNestWait(self) == /\ pc[self] = "BNestWait"
                   /\ childDone
                   /\ pc' = [pc EXCEPT ![self] = "BReacquire"]
                   /\ UNCHANGED << turnCancelled, groupCancelled, active, 
                                   slotsHeld, holder, phase, groupErr, decided, 
                                   firstFailure, settled, winner, winnerFrom, 
                                   firstFinish, results, writes, callRec, 
                                   review, endedBy, startedAfterDecision, 
                                   callStartedCancelled, childDone, result, 
                                   resultFrom, stack, i, r, goErr >>

BReacquire(self) == /\ pc[self] = "BReacquire"
                    /\ IF ReleaseWhileNested
                          THEN /\ slotsHeld < Slots
                               /\ slotsHeld' = slotsHeld + 1
                               /\ holder' = [holder EXCEPT ![self] = TRUE]
                          ELSE /\ TRUE
                               /\ UNCHANGED << slotsHeld, holder >>
                    /\ pc' = [pc EXCEPT ![self] = "BOutcome"]
                    /\ UNCHANGED << turnCancelled, groupCancelled, active, 
                                    phase, groupErr, decided, firstFailure, 
                                    settled, winner, winnerFrom, firstFinish, 
                                    results, writes, callRec, review, endedBy, 
                                    startedAfterDecision, callStartedCancelled, 
                                    childDone, result, resultFrom, stack, i, r, 
                                    goErr >>

BOutcome(self) == /\ pc[self] = "BOutcome"
                  /\ IF Cancelled
                        THEN /\ goErr' = [goErr EXCEPT ![self] = TRUE]
                             /\ r' = r
                        ELSE /\ \/ /\ r' = [r EXCEPT ![self] = "success"]
                                   /\ goErr' = goErr
                                \/ /\ r' = [r EXCEPT ![self] = "failure"]
                                   /\ goErr' = goErr
                                \/ /\ IF CrashIsFailure
                                         THEN /\ r' = [r EXCEPT ![self] = "crash"]
                                              /\ goErr' = goErr
                                         ELSE /\ goErr' = [goErr EXCEPT ![self] = TRUE]
                                              /\ r' = r
                  /\ pc' = [pc EXCEPT ![self] = "BCommit"]
                  /\ UNCHANGED << turnCancelled, groupCancelled, active, 
                                  slotsHeld, holder, phase, groupErr, decided, 
                                  firstFailure, settled, winner, winnerFrom, 
                                  firstFinish, results, writes, callRec, 
                                  review, endedBy, startedAfterDecision, 
                                  callStartedCancelled, childDone, result, 
                                  resultFrom, stack, i >>

BCommit(self) == /\ pc[self] = "BCommit"
                 /\ IF ~goErr[self]
                       THEN /\ results' = [results EXCEPT ![self] = r[self]]
                            /\ writes' = [writes EXCEPT ![self] = writes[self] + 1]
                            /\ IF IsFailure(r[self]) /\ firstFailure = 0
                                  THEN /\ firstFailure' = self
                                  ELSE /\ TRUE
                                       /\ UNCHANGED firstFailure
                       ELSE /\ TRUE
                            /\ UNCHANGED << firstFailure, results, writes >>
                 /\ IF IsRace /\ ~settled
                       THEN /\ IF firstFinish = 0
                                  THEN /\ firstFinish' = self
                                  ELSE /\ TRUE
                                       /\ UNCHANGED firstFinish
                            /\ IF Op = "race_first" \/ ~RaceWaitsForSuccess \/ (~goErr[self] /\ r[self] = "success")
                                  THEN /\ settled' = TRUE
                                       /\ decided' = TRUE
                                       /\ winner' = IF goErr[self] THEN "goerr" ELSE r[self]
                                       /\ winnerFrom' = self
                                       /\ groupCancelled' = TRUE
                                  ELSE /\ IF winner = "none"
                                             THEN /\ winner' = IF goErr[self] THEN "goerr" ELSE r[self]
                                                  /\ winnerFrom' = self
                                             ELSE /\ TRUE
                                                  /\ UNCHANGED << winner, 
                                                                  winnerFrom >>
                                       /\ UNCHANGED << groupCancelled, decided, 
                                                       settled >>
                       ELSE /\ TRUE
                            /\ UNCHANGED << groupCancelled, decided, settled, 
                                            winner, winnerFrom, firstFinish >>
                 /\ pc' = [pc EXCEPT ![self] = "BRelease"]
                 /\ UNCHANGED << turnCancelled, active, slotsHeld, holder, 
                                 phase, groupErr, callRec, review, endedBy, 
                                 startedAfterDecision, callStartedCancelled, 
                                 childDone, result, resultFrom, stack, i, r, 
                                 goErr >>

BRelease(self) == /\ pc[self] = "BRelease"
                  /\ IF holder[self]
                        THEN /\ slotsHeld' = slotsHeld - 1
                             /\ holder' = [holder EXCEPT ![self] = FALSE]
                        ELSE /\ TRUE
                             /\ UNCHANGED << slotsHeld, holder >>
                  /\ pc' = [pc EXCEPT ![self] = "BDone"]
                  /\ UNCHANGED << turnCancelled, groupCancelled, active, phase, 
                                  groupErr, decided, firstFailure, settled, 
                                  winner, winnerFrom, firstFinish, results, 
                                  writes, callRec, review, endedBy, 
                                  startedAfterDecision, callStartedCancelled, 
                                  childDone, result, resultFrom, stack, i, r, 
                                  goErr >>

BDone(self) == /\ pc[self] = "BDone"
               /\ IF ~IsRace /\ groupErr = "none"
                     THEN /\ IF goErr[self]
                                THEN /\ groupErr' = "goerr"
                                     /\ groupCancelled' = TRUE
                                     /\ UNCHANGED decided
                                ELSE /\ IF Op = "try" /\ IsFailure(r[self])
                                           THEN /\ groupErr' = "failed"
                                                /\ decided' = TRUE
                                                /\ IF CancelSiblings
                                                      THEN /\ groupCancelled' = TRUE
                                                      ELSE /\ TRUE
                                                           /\ UNCHANGED groupCancelled
                                           ELSE /\ TRUE
                                                /\ UNCHANGED << groupCancelled, 
                                                                groupErr, 
                                                                decided >>
                     ELSE /\ TRUE
                          /\ UNCHANGED << groupCancelled, groupErr, decided >>
               /\ active' = active - 1
               /\ phase' = [phase EXCEPT ![self] = "done"]
               /\ pc' = [pc EXCEPT ![self] = "Done"]
               /\ UNCHANGED << turnCancelled, slotsHeld, holder, firstFailure, 
                               settled, winner, winnerFrom, firstFinish, 
                               results, writes, callRec, review, endedBy, 
                               startedAfterDecision, callStartedCancelled, 
                               childDone, result, resultFrom, stack, i, r, 
                               goErr >>

Branch(self) == BSpawned(self) \/ BAcquire(self) \/ BPreCheck(self)
                   \/ BNestWait(self) \/ BReacquire(self) \/ BOutcome(self)
                   \/ BCommit(self) \/ BRelease(self) \/ BDone(self)

CSpawned == /\ pc[ChildId] = "CSpawned"
            /\ phase[ChildId] = "started"
            /\ pc' = [pc EXCEPT ![ChildId] = "CAcquire"]
            /\ UNCHANGED << turnCancelled, groupCancelled, active, slotsHeld, 
                            holder, phase, groupErr, decided, firstFailure, 
                            settled, winner, winnerFrom, firstFinish, results, 
                            writes, callRec, review, endedBy, 
                            startedAfterDecision, callStartedCancelled, 
                            childDone, result, resultFrom, stack, i, r, goErr >>

CAcquire == /\ pc[ChildId] = "CAcquire"
            /\ \/ /\ Cancelled
                  /\ pc' = [pc EXCEPT ![ChildId] = "CDone"]
                  /\ UNCHANGED <<slotsHeld, holder>>
               \/ /\ ~Cancelled /\ slotsHeld < Slots
                  /\ slotsHeld' = slotsHeld + 1
                  /\ holder' = [holder EXCEPT ![ChildId] = TRUE]
                  /\ pc' = [pc EXCEPT ![ChildId] = "CPreCheck"]
            /\ UNCHANGED << turnCancelled, groupCancelled, active, phase, 
                            groupErr, decided, firstFailure, settled, winner, 
                            winnerFrom, firstFinish, results, writes, callRec, 
                            review, endedBy, startedAfterDecision, 
                            callStartedCancelled, childDone, result, 
                            resultFrom, stack, i, r, goErr >>

CPreCheck == /\ pc[ChildId] = "CPreCheck"
             /\ IF Cancelled
                   THEN /\ pc' = [pc EXCEPT ![ChildId] = "CRelease"]
                        /\ stack' = stack
                   ELSE /\ stack' = [stack EXCEPT ![ChildId] = << [ procedure |->  "ToolCall",
                                                                    pc        |->  "CRelease" ] >>
                                                                \o stack[ChildId]]
                        /\ pc' = [pc EXCEPT ![ChildId] = "ToolCheck"]
             /\ UNCHANGED << turnCancelled, groupCancelled, active, slotsHeld, 
                             holder, phase, groupErr, decided, firstFailure, 
                             settled, winner, winnerFrom, firstFinish, results, 
                             writes, callRec, review, endedBy, 
                             startedAfterDecision, callStartedCancelled, 
                             childDone, result, resultFrom, i, r, goErr >>

CRelease == /\ pc[ChildId] = "CRelease"
            /\ IF holder[ChildId]
                  THEN /\ slotsHeld' = slotsHeld - 1
                       /\ holder' = [holder EXCEPT ![ChildId] = FALSE]
                  ELSE /\ TRUE
                       /\ UNCHANGED << slotsHeld, holder >>
            /\ pc' = [pc EXCEPT ![ChildId] = "CDone"]
            /\ UNCHANGED << turnCancelled, groupCancelled, active, phase, 
                            groupErr, decided, firstFailure, settled, winner, 
                            winnerFrom, firstFinish, results, writes, callRec, 
                            review, endedBy, startedAfterDecision, 
                            callStartedCancelled, childDone, result, 
                            resultFrom, stack, i, r, goErr >>

CDone == /\ pc[ChildId] = "CDone"
         /\ childDone' = TRUE
         /\ phase' = [phase EXCEPT ![ChildId] = "done"]
         /\ pc' = [pc EXCEPT ![ChildId] = "Done"]
         /\ UNCHANGED << turnCancelled, groupCancelled, active, slotsHeld, 
                         holder, groupErr, decided, firstFailure, settled, 
                         winner, winnerFrom, firstFinish, results, writes, 
                         callRec, review, endedBy, startedAfterDecision, 
                         callStartedCancelled, result, resultFrom, stack, i, r, 
                         goErr >>

Child == CSpawned \/ CAcquire \/ CPreCheck \/ CRelease \/ CDone

TCancel == /\ pc[TurnId] = "TCancel"
           /\ IF Cancelable
                 THEN /\ turnCancelled' = TRUE
                 ELSE /\ TRUE
                      /\ UNCHANGED turnCancelled
           /\ pc' = [pc EXCEPT ![TurnId] = "Done"]
           /\ UNCHANGED << groupCancelled, active, slotsHeld, holder, phase, 
                           groupErr, decided, firstFailure, settled, winner, 
                           winnerFrom, firstFinish, results, writes, callRec, 
                           review, endedBy, startedAfterDecision, 
                           callStartedCancelled, childDone, result, resultFrom, 
                           stack, i, r, goErr >>

Turn == TCancel

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == Main \/ Child \/ Turn
           \/ (\E self \in ProcSet: ToolCall(self))
           \/ (\E self \in Branches: Branch(self))
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Main)
        /\ \A self \in Branches : WF_vars(Branch(self)) /\ WF_vars(ToolCall(self))
        /\ WF_vars(Child) /\ WF_vars(ToolCall(ChildId))

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION 

=============================================================================
