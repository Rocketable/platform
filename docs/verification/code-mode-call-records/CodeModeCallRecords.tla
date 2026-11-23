------------------------- MODULE CodeModeCallRecords -------------------------
EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS N, MaxRestarts, PreFix, RowLock, SnapshotWins, Recovery, SloppyPrefix

ASSUME N \in Nat \ {0} /\ MaxRestarts \in Nat
ASSUME \A flag \in {PreFix, RowLock, SnapshotWins, SloppyPrefix} : flag \in BOOLEAN
ASSUME Recovery \in {"none", "killThenCancel", "cancelThenKill"}

Other == 0
Calls == 1..N
Keys == {Other} \cup Calls
Status == {"none", "running", "ok", "cancelled"}
Final == {"ok", "cancelled"}

Rank(s) == CASE s = "none" -> 0 [] s = "running" -> 1 [] OTHER -> 2

KeysOf(e) == {e[i].key : i \in DOMAIN e}

StatusIn(e, k) == IF k \in KeysOf(e) THEN e[CHOOSE i \in DOMAIN e : e[i].key = k].st ELSE "none"

NonNone(f) == [k \in {x \in DOMAIN f : f[x] # "none"} |-> f[k]]

Restrict(f, ks) == [k \in ks |-> f[k]]

Added(recs, ks) ==
    [i \in 1..Cardinality(ks) |->
        LET k == CHOOSE x \in ks : Cardinality({y \in ks : y < x}) = i - 1
        IN [key |-> k, st |-> recs[k]]]

Merge(e, recs) ==
    [i \in DOMAIN e |-> IF e[i].key \in DOMAIN recs THEN [key |-> e[i].key, st |-> recs[e[i].key]] ELSE e[i]]
    \o Added(recs, DOMAIN recs \ KeysOf(e))

Owned(k) == k \in Calls \/ SloppyPrefix

Free == 0

None == [k \in Keys |-> "none"]
Start == [k \in Keys |-> IF k = Other THEN "running" ELSE "none"]

(* --fair algorithm CodeModeCallRecords
variables
    phase = "running",
    scratch = Start,
    entry = <<>>,
    web = <<>>,
    jobPhase = "running",
    hMutex = Free,
    rowLock = Free,
    hCalls = Start,
    snapshot = None,
    closed = FALSE,
    cpc = [c \in Calls |-> "next"],
    cst = [c \in Calls |-> 1],
    liveSeen = [c \in Calls |-> FALSE],
    tpc = "running",
    rpc = "none",
    restarts = 0,
    reported = [c \in Calls |-> "none"],
    written = [c \in Calls |-> "none"],
    mergedLate = FALSE,
    pickedUp = FALSE,
    cancelledSaved = FALSE,
    cancelledScratch = FALSE;

define
    Stored(k) == IF phase = "running" THEN scratch[k] ELSE StatusIn(entry, k)

    Record(c) == IF cst[c] = 1 THEN "running" ELSE "ok"

    Finished == rpc = "none" /\ tpc = "saved" /\ jobPhase # "running"

    TypeOK ==
        /\ phase \in {"running", "delivering"}
        /\ scratch \in [Keys -> Status]
        /\ \A i \in DOMAIN entry : entry[i] \in [key : Keys, st : Status \ {"none"}]
        /\ jobPhase \in {"running", "done", "killed"}
        /\ hMutex \in Calls \cup {Free}
        /\ rowLock \in Calls \cup {Free}
        /\ hCalls \in [Keys -> Status]
        /\ snapshot \in [Keys -> Status]
        /\ cpc \in [Calls -> {"next", "locking", "writing", "done", "dead"}]
        /\ cst \in [Calls -> 1..2]
        /\ tpc \in {"running", "snapped", "resume", "saved"}
        /\ rpc \in {"none", "start", "mid"}
        /\ restarts \in 0..MaxRestarts
        /\ reported \in [Calls -> Status]
        /\ written \in [Calls -> Status]

    NoLoss == \A c \in Calls : Rank(Stored(c)) >= Rank(reported[c])

    Latest == \A c \in Calls : Stored(c) = written[c]

    OneRow == \A i, j \in DOMAIN entry : i # j => entry[i].key # entry[j].key

    JobEndsCalls == rpc = "none" /\ jobPhase # "running" => \A c \in Calls : Stored(c) \in {"none"} \cup Final

    OtherUntouched == Stored(Other) = "running"

    WebNotified == phase = "delivering" => web = entry

    NeverMergedLate == ~mergedLate
    NeverPickedUp == ~pickedUp
    NeverCancelledSaved == ~cancelledSaved
    NeverCancelledScratchSaved == ~(cancelledScratch /\ phase = "delivering")
    NeverFinishWaits == ~(tpc = "snapped" /\ rowLock # Free)
end define;

macro Cancel() begin
    with s = [k \in Keys |-> IF Owned(k) /\ scratch[k] = "running" THEN "cancelled" ELSE scratch[k]],
         e = Merge(entry, [k \in {x \in KeysOf(entry) : Owned(x) /\ StatusIn(entry, x) = "running"} |-> "cancelled"]) do
        written := [c \in Calls |-> IF Stored(c) = "running" THEN "cancelled" ELSE written[c]];
        cancelledSaved := cancelledSaved \/ e # entry;
        cancelledScratch := cancelledScratch \/ \E c \in Calls : s[c] # scratch[c];
        scratch := s;
        entry := e;
        web := e;
    end with;
end macro;

begin
Step:
    while ~Finished do
        either
            await restarts < MaxRestarts;
            restarts := restarts + 1;
            hMutex := Free;
            rowLock := Free;
            cpc := [c \in Calls |-> IF cpc[c] = "done" THEN "done" ELSE "dead"];
            hCalls := None;
            snapshot := None;
            closed := FALSE;
            tpc := IF phase = "running" THEN "resume" ELSE "saved";
            rpc := "start";
        or
            await rpc = "start";
            if jobPhase # "running" then
                rpc := "none";
            elsif Recovery = "cancelThenKill" then
                Cancel();
                rpc := "mid";
            elsif Recovery = "killThenCancel" then
                jobPhase := "killed";
                rpc := "mid";
            else
                jobPhase := "killed";
                rpc := "none";
            end if;
        or
            await rpc = "mid";
            if Recovery = "killThenCancel" then
                Cancel();
            else
                jobPhase := "killed";
            end if;
            rpc := "none";
        or
            with c \in {d \in Calls : cpc[d] = "next"} do
                await hMutex = Free;
                if PreFix /\ closed then
                    reported[c] := Record(c);
                    if cst[c] = 1 then
                        cst[c] := 2;
                    else
                        cpc[c] := "done";
                    end if;
                else
                    hMutex := c;
                    cpc[c] := "locking";
                end if;
            end with;
        or
            with c \in {d \in Calls : cpc[d] = "locking"} do
                await ~RowLock \/ rowLock = Free;
                if RowLock then
                    rowLock := c;
                end if;
                liveSeen[c] := phase = "running";
                cpc[c] := "writing";
            end with;
        or
            with c \in {d \in Calls : cpc[d] = "writing"}, s = Record(c) do
                if liveSeen[c] \/ PreFix then
                    scratch[c] := s;
                else
                    with e = Merge(entry, [k \in {c} |-> s]) do
                        entry := e;
                        web := e;
                    end with;
                    mergedLate := TRUE;
                end if;
                written[c] := s;
                reported[c] := s;
                hCalls[c] := s;
                rowLock := Free;
                hMutex := Free;
                if cst[c] = 1 then
                    cst[c] := 2;
                    cpc[c] := "next";
                else
                    cpc[c] := "done";
                end if;
            end with;
        or
            await rpc = "none" /\ jobPhase = "running" /\ \A c \in Calls : cpc[c] = "done";
            jobPhase := "done";
        or
            await rpc = "none" /\ tpc = "running" /\ hMutex = Free;
            snapshot := hCalls;
            closed := TRUE;
            tpc := "snapped";
        or
            await rpc = "none" /\ tpc = "resume";
            snapshot := scratch;
            tpc := "snapped";
        or
            await rpc = "none" /\ tpc = "snapped" /\ rowLock = Free;
            with base = Merge(<<>>, NonNone(snapshot)), recs = NonNone(scratch),
                 e = IF PreFix THEN base
                     ELSE IF SnapshotWins THEN Merge(base, Restrict(recs, DOMAIN recs \ KeysOf(base)))
                     ELSE Merge(base, recs) do
                entry := e;
                web := e;
                pickedUp := pickedUp \/ \E k \in DOMAIN recs : Rank(recs[k]) > Rank(snapshot[k]);
            end with;
            phase := "delivering";
            scratch := None;
            tpc := "saved";
        end either;
    end while;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, phase, scratch, entry, web, jobPhase, hMutex, rowLock, hCalls, 
          snapshot, closed, cpc, cst, liveSeen, tpc, rpc, restarts, reported, 
          written, mergedLate, pickedUp, cancelledSaved, cancelledScratch

(* define statement *)
Stored(k) == IF phase = "running" THEN scratch[k] ELSE StatusIn(entry, k)

Record(c) == IF cst[c] = 1 THEN "running" ELSE "ok"

Finished == rpc = "none" /\ tpc = "saved" /\ jobPhase # "running"

TypeOK ==
    /\ phase \in {"running", "delivering"}
    /\ scratch \in [Keys -> Status]
    /\ \A i \in DOMAIN entry : entry[i] \in [key : Keys, st : Status \ {"none"}]
    /\ jobPhase \in {"running", "done", "killed"}
    /\ hMutex \in Calls \cup {Free}
    /\ rowLock \in Calls \cup {Free}
    /\ hCalls \in [Keys -> Status]
    /\ snapshot \in [Keys -> Status]
    /\ cpc \in [Calls -> {"next", "locking", "writing", "done", "dead"}]
    /\ cst \in [Calls -> 1..2]
    /\ tpc \in {"running", "snapped", "resume", "saved"}
    /\ rpc \in {"none", "start", "mid"}
    /\ restarts \in 0..MaxRestarts
    /\ reported \in [Calls -> Status]
    /\ written \in [Calls -> Status]

NoLoss == \A c \in Calls : Rank(Stored(c)) >= Rank(reported[c])

Latest == \A c \in Calls : Stored(c) = written[c]

OneRow == \A i, j \in DOMAIN entry : i # j => entry[i].key # entry[j].key

JobEndsCalls == rpc = "none" /\ jobPhase # "running" => \A c \in Calls : Stored(c) \in {"none"} \cup Final

OtherUntouched == Stored(Other) = "running"

WebNotified == phase = "delivering" => web = entry

NeverMergedLate == ~mergedLate
NeverPickedUp == ~pickedUp
NeverCancelledSaved == ~cancelledSaved
NeverCancelledScratchSaved == ~(cancelledScratch /\ phase = "delivering")
NeverFinishWaits == ~(tpc = "snapped" /\ rowLock # Free)


vars == << pc, phase, scratch, entry, web, jobPhase, hMutex, rowLock, hCalls, 
           snapshot, closed, cpc, cst, liveSeen, tpc, rpc, restarts, reported, 
           written, mergedLate, pickedUp, cancelledSaved, cancelledScratch >>

Init == (* Global variables *)
        /\ phase = "running"
        /\ scratch = Start
        /\ entry = <<>>
        /\ web = <<>>
        /\ jobPhase = "running"
        /\ hMutex = Free
        /\ rowLock = Free
        /\ hCalls = Start
        /\ snapshot = None
        /\ closed = FALSE
        /\ cpc = [c \in Calls |-> "next"]
        /\ cst = [c \in Calls |-> 1]
        /\ liveSeen = [c \in Calls |-> FALSE]
        /\ tpc = "running"
        /\ rpc = "none"
        /\ restarts = 0
        /\ reported = [c \in Calls |-> "none"]
        /\ written = [c \in Calls |-> "none"]
        /\ mergedLate = FALSE
        /\ pickedUp = FALSE
        /\ cancelledSaved = FALSE
        /\ cancelledScratch = FALSE
        /\ pc = "Step"

Step == /\ pc = "Step"
        /\ IF ~Finished
              THEN /\ \/ /\ restarts < MaxRestarts
                         /\ restarts' = restarts + 1
                         /\ hMutex' = Free
                         /\ rowLock' = Free
                         /\ cpc' = [c \in Calls |-> IF cpc[c] = "done" THEN "done" ELSE "dead"]
                         /\ hCalls' = None
                         /\ snapshot' = None
                         /\ closed' = FALSE
                         /\ tpc' = (IF phase = "running" THEN "resume" ELSE "saved")
                         /\ rpc' = "start"
                         /\ UNCHANGED <<phase, scratch, entry, web, jobPhase, cst, liveSeen, reported, written, mergedLate, pickedUp, cancelledSaved, cancelledScratch>>
                      \/ /\ rpc = "start"
                         /\ IF jobPhase # "running"
                               THEN /\ rpc' = "none"
                                    /\ UNCHANGED << scratch, entry, web, 
                                                    jobPhase, written, 
                                                    cancelledSaved, 
                                                    cancelledScratch >>
                               ELSE /\ IF Recovery = "cancelThenKill"
                                          THEN /\ LET s == [k \in Keys |-> IF Owned(k) /\ scratch[k] = "running" THEN "cancelled" ELSE scratch[k]] IN
                                                    LET e == Merge(entry, [k \in {x \in KeysOf(entry) : Owned(x) /\ StatusIn(entry, x) = "running"} |-> "cancelled"]) IN
                                                      /\ written' = [c \in Calls |-> IF Stored(c) = "running" THEN "cancelled" ELSE written[c]]
                                                      /\ cancelledSaved' = (cancelledSaved \/ e # entry)
                                                      /\ cancelledScratch' = (cancelledScratch \/ \E c \in Calls : s[c] # scratch[c])
                                                      /\ scratch' = s
                                                      /\ entry' = e
                                                      /\ web' = e
                                               /\ rpc' = "mid"
                                               /\ UNCHANGED jobPhase
                                          ELSE /\ IF Recovery = "killThenCancel"
                                                     THEN /\ jobPhase' = "killed"
                                                          /\ rpc' = "mid"
                                                     ELSE /\ jobPhase' = "killed"
                                                          /\ rpc' = "none"
                                               /\ UNCHANGED << scratch, entry, 
                                                               web, written, 
                                                               cancelledSaved, 
                                                               cancelledScratch >>
                         /\ UNCHANGED <<phase, hMutex, rowLock, hCalls, snapshot, closed, cpc, cst, liveSeen, tpc, restarts, reported, mergedLate, pickedUp>>
                      \/ /\ rpc = "mid"
                         /\ IF Recovery = "killThenCancel"
                               THEN /\ LET s == [k \in Keys |-> IF Owned(k) /\ scratch[k] = "running" THEN "cancelled" ELSE scratch[k]] IN
                                         LET e == Merge(entry, [k \in {x \in KeysOf(entry) : Owned(x) /\ StatusIn(entry, x) = "running"} |-> "cancelled"]) IN
                                           /\ written' = [c \in Calls |-> IF Stored(c) = "running" THEN "cancelled" ELSE written[c]]
                                           /\ cancelledSaved' = (cancelledSaved \/ e # entry)
                                           /\ cancelledScratch' = (cancelledScratch \/ \E c \in Calls : s[c] # scratch[c])
                                           /\ scratch' = s
                                           /\ entry' = e
                                           /\ web' = e
                                    /\ UNCHANGED jobPhase
                               ELSE /\ jobPhase' = "killed"
                                    /\ UNCHANGED << scratch, entry, web, 
                                                    written, cancelledSaved, 
                                                    cancelledScratch >>
                         /\ rpc' = "none"
                         /\ UNCHANGED <<phase, hMutex, rowLock, hCalls, snapshot, closed, cpc, cst, liveSeen, tpc, restarts, reported, mergedLate, pickedUp>>
                      \/ /\ \E c \in {d \in Calls : cpc[d] = "next"}:
                              /\ hMutex = Free
                              /\ IF PreFix /\ closed
                                    THEN /\ reported' = [reported EXCEPT ![c] = Record(c)]
                                         /\ IF cst[c] = 1
                                               THEN /\ cst' = [cst EXCEPT ![c] = 2]
                                                    /\ cpc' = cpc
                                               ELSE /\ cpc' = [cpc EXCEPT ![c] = "done"]
                                                    /\ cst' = cst
                                         /\ UNCHANGED hMutex
                                    ELSE /\ hMutex' = c
                                         /\ cpc' = [cpc EXCEPT ![c] = "locking"]
                                         /\ UNCHANGED << cst, reported >>
                         /\ UNCHANGED <<phase, scratch, entry, web, jobPhase, rowLock, hCalls, snapshot, closed, liveSeen, tpc, rpc, restarts, written, mergedLate, pickedUp, cancelledSaved, cancelledScratch>>
                      \/ /\ \E c \in {d \in Calls : cpc[d] = "locking"}:
                              /\ ~RowLock \/ rowLock = Free
                              /\ IF RowLock
                                    THEN /\ rowLock' = c
                                    ELSE /\ TRUE
                                         /\ UNCHANGED rowLock
                              /\ liveSeen' = [liveSeen EXCEPT ![c] = phase = "running"]
                              /\ cpc' = [cpc EXCEPT ![c] = "writing"]
                         /\ UNCHANGED <<phase, scratch, entry, web, jobPhase, hMutex, hCalls, snapshot, closed, cst, tpc, rpc, restarts, reported, written, mergedLate, pickedUp, cancelledSaved, cancelledScratch>>
                      \/ /\ \E c \in {d \in Calls : cpc[d] = "writing"}:
                              LET s == Record(c) IN
                                /\ IF liveSeen[c] \/ PreFix
                                      THEN /\ scratch' = [scratch EXCEPT ![c] = s]
                                           /\ UNCHANGED << entry, web, 
                                                           mergedLate >>
                                      ELSE /\ LET e == Merge(entry, [k \in {c} |-> s]) IN
                                                /\ entry' = e
                                                /\ web' = e
                                           /\ mergedLate' = TRUE
                                           /\ UNCHANGED scratch
                                /\ written' = [written EXCEPT ![c] = s]
                                /\ reported' = [reported EXCEPT ![c] = s]
                                /\ hCalls' = [hCalls EXCEPT ![c] = s]
                                /\ rowLock' = Free
                                /\ hMutex' = Free
                                /\ IF cst[c] = 1
                                      THEN /\ cst' = [cst EXCEPT ![c] = 2]
                                           /\ cpc' = [cpc EXCEPT ![c] = "next"]
                                      ELSE /\ cpc' = [cpc EXCEPT ![c] = "done"]
                                           /\ cst' = cst
                         /\ UNCHANGED <<phase, jobPhase, snapshot, closed, liveSeen, tpc, rpc, restarts, pickedUp, cancelledSaved, cancelledScratch>>
                      \/ /\ rpc = "none" /\ jobPhase = "running" /\ \A c \in Calls : cpc[c] = "done"
                         /\ jobPhase' = "done"
                         /\ UNCHANGED <<phase, scratch, entry, web, hMutex, rowLock, hCalls, snapshot, closed, cpc, cst, liveSeen, tpc, rpc, restarts, reported, written, mergedLate, pickedUp, cancelledSaved, cancelledScratch>>
                      \/ /\ rpc = "none" /\ tpc = "running" /\ hMutex = Free
                         /\ snapshot' = hCalls
                         /\ closed' = TRUE
                         /\ tpc' = "snapped"
                         /\ UNCHANGED <<phase, scratch, entry, web, jobPhase, hMutex, rowLock, hCalls, cpc, cst, liveSeen, rpc, restarts, reported, written, mergedLate, pickedUp, cancelledSaved, cancelledScratch>>
                      \/ /\ rpc = "none" /\ tpc = "resume"
                         /\ snapshot' = scratch
                         /\ tpc' = "snapped"
                         /\ UNCHANGED <<phase, scratch, entry, web, jobPhase, hMutex, rowLock, hCalls, closed, cpc, cst, liveSeen, rpc, restarts, reported, written, mergedLate, pickedUp, cancelledSaved, cancelledScratch>>
                      \/ /\ rpc = "none" /\ tpc = "snapped" /\ rowLock = Free
                         /\ LET base == Merge(<<>>, NonNone(snapshot)) IN
                              LET recs == NonNone(scratch) IN
                                LET e == IF PreFix THEN base
                                         ELSE IF SnapshotWins THEN Merge(base, Restrict(recs, DOMAIN recs \ KeysOf(base)))
                                         ELSE Merge(base, recs) IN
                                  /\ entry' = e
                                  /\ web' = e
                                  /\ pickedUp' = (pickedUp \/ \E k \in DOMAIN recs : Rank(recs[k]) > Rank(snapshot[k]))
                         /\ phase' = "delivering"
                         /\ scratch' = None
                         /\ tpc' = "saved"
                         /\ UNCHANGED <<jobPhase, hMutex, rowLock, hCalls, snapshot, closed, cpc, cst, liveSeen, rpc, restarts, reported, written, mergedLate, cancelledSaved, cancelledScratch>>
                   /\ pc' = "Step"
              ELSE /\ pc' = "Done"
                   /\ UNCHANGED << phase, scratch, entry, web, jobPhase, 
                                   hMutex, rowLock, hCalls, snapshot, closed, 
                                   cpc, cst, liveSeen, tpc, rpc, restarts, 
                                   reported, written, mergedLate, pickedUp, 
                                   cancelledSaved, cancelledScratch >>

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == pc = "Done" /\ UNCHANGED vars

Next == Step
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Next)

Termination == <>(pc = "Done")

\* END TRANSLATION

NeverOlder == [][\A k \in Keys : Stored(k)' = Stored(k) \/ Rank(Stored(k)') > Rank(Stored(k))]_vars

Ends == <>(pc = "Done")

=============================================================================
