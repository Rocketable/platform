----------------------------- MODULE ReadFdRetry -----------------------------
EXTENDS Naturals, FiniteSets

CONSTANTS N, ProcCap, SysCap, TwoStep, Backoff, Timer, ReleaseOnWait,
          CloseAfterRead, SerializeRetries, SlotCancel, CancelCheck, Cancelable, Recycle

ASSUME N \in Nat \ {0} /\ ProcCap \in Nat \ {0} /\ SysCap \in Nat /\ SysCap >= ProcCap
ASSUME {TwoStep, Backoff, Timer, ReleaseOnWait, CloseAfterRead,
        SerializeRetries, SlotCancel, CancelCheck, Cancelable, Recycle} \subseteq BOOLEAN

Readers == 1..N
InProcId == N + 1
OtherSysId == N + 2
CancelId == N + 3

RECURSIVE Sum(_, _)
Sum(f, S) == IF S = {} THEN 0 ELSE LET x == CHOOSE y \in S : TRUE IN f[x] + Sum(f, S \ {x})

(* --algorithm ReadFdRetry
variables
    dirHeld = [r \in Readers |-> 0],
    fileHeld = [r \in Readers |-> 0],
    inProc = 0,
    otherSys = 0,
    lock = 0,
    woken = [r \in Readers |-> FALSE],
    cancelled = [r \in Readers |-> FALSE],
    outcome = [r \in Readers |-> "none"],
    retrying = [r \in Readers |-> FALSE],
    failedSinceWait = [r \in Readers |-> FALSE],
    retriedCancelled = [r \in Readers |-> FALSE],
    sawEnfile = FALSE;

define
    Held(r) == dirHeld[r] + fileHeld[r]
    ProcUse == inProc + Sum(dirHeld, Readers) + Sum(fileHeld, Readers)
    SysUse == ProcUse + otherSys
    CanOpen == ProcUse < ProcCap /\ SysUse < SysCap
    Locked(r) == SerializeRetries /\ retrying[r]

    TypeOK ==
        /\ dirHeld \in [Readers -> 0..2]
        /\ fileHeld \in [Readers -> 0..2]
        /\ inProc \in 0..ProcCap
        /\ otherSys \in 0..SysCap
        /\ lock \in {0} \cup Readers
        /\ woken \in [Readers -> BOOLEAN]
        /\ cancelled \in [Readers -> BOOLEAN]
        /\ outcome \in [Readers -> {"none", "ok", "cancelled"}]
        /\ retrying \in [Readers -> BOOLEAN]
        /\ failedSinceWait \in [Readers -> BOOLEAN]
        /\ retriedCancelled \in [Readers -> BOOLEAN]
        /\ sawEnfile \in BOOLEAN

    CapRespected == ProcUse <= ProcCap /\ SysUse <= SysCap

    HeldBound ==
        \A r \in Readers :
            /\ dirHeld[r] <= 1
            /\ fileHeld[r] <= 1
            /\ Held(r) <= IF TwoStep THEN 2 ELSE 1

    HoldsOnlyWhileOpeningOrReading ==
        \A r \in Readers :
            /\ dirHeld[r] > 0 => pc[r] \in {"OpenFile", "Read", "Close"}
            /\ fileHeld[r] > 0 => pc[r] \in {"Read", "Close"}

    NoLeak == \A r \in Readers : pc[r] = "Done" => Held(r) = 0

    NoSpin == \A r \in Readers : pc[r] = "Attempt" => ~failedSinceWait[r]

    LockOnlyWhileOpening == lock # 0 => pc[lock] \in {"OpenDir", "OpenFile"}

    NoRetryAfterCancel == \A r \in Readers : ~retriedCancelled[r]

    OthersRelease == <>[](inProc = 0 /\ otherSys = 0)

    AllReadsEnd == OthersRelease => \A r \in Readers : <>(pc[r] = "Done")

    CancelEnds == \A r \in Readers : cancelled[r] ~> (pc[r] = "Done")

    SomeReaderKeepsReading == OthersRelease => []<>(\E r \in Readers : pc[r] = "Close")

    EachReaderKeepsReading == OthersRelease => \A r \in Readers : []<>(pc[r] = "Close")

    NeverRetriedThenRead == ~\E r \in Readers : pc[r] = "Done" /\ outcome[r] = "ok" /\ retrying[r]
    NeverEnfile == ~sawEnfile
    NeverCancelled == ~\E r \in Readers : outcome[r] = "cancelled"
end define;

fair process Reader \in Readers
begin
Attempt:
    either
        await ~Locked(self) \/ lock = 0;
        if Locked(self) then
            lock := self;
        end if;
        woken[self] := FALSE;
        if TwoStep then
            goto OpenDir;
        else
            goto OpenFile;
        end if;
    or
        await SlotCancel /\ Locked(self) /\ cancelled[self];
        outcome[self] := "cancelled";
        goto Done;
    end either;
OpenDir:
    if CanOpen then
        dirHeld[self] := dirHeld[self] + 1;
        goto OpenFile;
    else
        failedSinceWait[self] := TRUE;
        retrying[self] := TRUE;
        sawEnfile := sawEnfile \/ ProcUse < ProcCap;
        if lock = self then
            lock := 0;
        end if;
        if Backoff then
            goto Wait;
        else
            goto Attempt;
        end if;
    end if;
OpenFile:
    if CanOpen then
        fileHeld[self] := fileHeld[self] + 1;
        if lock = self then
            lock := 0;
        end if;
        goto Read;
    else
        if ReleaseOnWait then
            dirHeld[self] := 0;
        end if;
        failedSinceWait[self] := TRUE;
        retrying[self] := TRUE;
        sawEnfile := sawEnfile \/ ProcUse < ProcCap;
        if lock = self then
            lock := 0;
        end if;
        if Backoff then
            goto Wait;
        else
            goto Attempt;
        end if;
    end if;
Wait:
    either
        await woken[self] \/ Timer;
        if CancelCheck /\ cancelled[self] then
            dirHeld[self] := 0;
            outcome[self] := "cancelled";
            goto Done;
        else
            retriedCancelled[self] := retriedCancelled[self] \/ cancelled[self];
            failedSinceWait[self] := FALSE;
            goto Attempt;
        end if;
    or
        await cancelled[self];
        dirHeld[self] := 0;
        outcome[self] := "cancelled";
        goto Done;
    end either;
Read:
    skip;
Close:
    if CloseAfterRead then
        dirHeld[self] := 0;
        fileHeld[self] := 0;
        woken := [r \in Readers |-> TRUE];
    end if;
    outcome[self] := "ok";
    if Recycle then
        retrying[self] := FALSE;
        goto Attempt;
    end if;
end process;

process InProcess = InProcId
begin
InLoop:
    while TRUE do
        either
            await CanOpen;
            inProc := inProc + 1;
        or
            await inProc > 0;
            inProc := inProc - 1;
        end either;
    end while;
end process;

process OtherProcesses = OtherSysId
begin
SysLoop:
    while TRUE do
        either
            await SysUse < SysCap;
            otherSys := otherSys + 1;
        or
            await otherSys > 0;
            otherSys := otherSys - 1;
        end either;
    end while;
end process;

process Canceller = CancelId
begin
CancelLoop:
    while Cancelable do
        with r \in {x \in Readers : ~cancelled[x]} do
            cancelled[r] := TRUE;
        end with;
    end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "77cd5428" /\ chksum(tla) = "f92c93f1")
VARIABLES pc, dirHeld, fileHeld, inProc, otherSys, lock, woken, cancelled, 
          outcome, retrying, failedSinceWait, retriedCancelled, sawEnfile

(* define statement *)
Held(r) == dirHeld[r] + fileHeld[r]
ProcUse == inProc + Sum(dirHeld, Readers) + Sum(fileHeld, Readers)
SysUse == ProcUse + otherSys
CanOpen == ProcUse < ProcCap /\ SysUse < SysCap
Locked(r) == SerializeRetries /\ retrying[r]

TypeOK ==
    /\ dirHeld \in [Readers -> 0..2]
    /\ fileHeld \in [Readers -> 0..2]
    /\ inProc \in 0..ProcCap
    /\ otherSys \in 0..SysCap
    /\ lock \in {0} \cup Readers
    /\ woken \in [Readers -> BOOLEAN]
    /\ cancelled \in [Readers -> BOOLEAN]
    /\ outcome \in [Readers -> {"none", "ok", "cancelled"}]
    /\ retrying \in [Readers -> BOOLEAN]
    /\ failedSinceWait \in [Readers -> BOOLEAN]
    /\ retriedCancelled \in [Readers -> BOOLEAN]
    /\ sawEnfile \in BOOLEAN

CapRespected == ProcUse <= ProcCap /\ SysUse <= SysCap

HeldBound ==
    \A r \in Readers :
        /\ dirHeld[r] <= 1
        /\ fileHeld[r] <= 1
        /\ Held(r) <= IF TwoStep THEN 2 ELSE 1

HoldsOnlyWhileOpeningOrReading ==
    \A r \in Readers :
        /\ dirHeld[r] > 0 => pc[r] \in {"OpenFile", "Read", "Close"}
        /\ fileHeld[r] > 0 => pc[r] \in {"Read", "Close"}

NoLeak == \A r \in Readers : pc[r] = "Done" => Held(r) = 0

NoSpin == \A r \in Readers : pc[r] = "Attempt" => ~failedSinceWait[r]

LockOnlyWhileOpening == lock # 0 => pc[lock] \in {"OpenDir", "OpenFile"}

NoRetryAfterCancel == \A r \in Readers : ~retriedCancelled[r]

OthersRelease == <>[](inProc = 0 /\ otherSys = 0)

AllReadsEnd == OthersRelease => \A r \in Readers : <>(pc[r] = "Done")

CancelEnds == \A r \in Readers : cancelled[r] ~> (pc[r] = "Done")

SomeReaderKeepsReading == OthersRelease => []<>(\E r \in Readers : pc[r] = "Close")

EachReaderKeepsReading == OthersRelease => \A r \in Readers : []<>(pc[r] = "Close")

NeverRetriedThenRead == ~\E r \in Readers : pc[r] = "Done" /\ outcome[r] = "ok" /\ retrying[r]
NeverEnfile == ~sawEnfile
NeverCancelled == ~\E r \in Readers : outcome[r] = "cancelled"


vars == << pc, dirHeld, fileHeld, inProc, otherSys, lock, woken, cancelled, 
           outcome, retrying, failedSinceWait, retriedCancelled, sawEnfile >>

ProcSet == (Readers) \cup {InProcId} \cup {OtherSysId} \cup {CancelId}

Init == (* Global variables *)
        /\ dirHeld = [r \in Readers |-> 0]
        /\ fileHeld = [r \in Readers |-> 0]
        /\ inProc = 0
        /\ otherSys = 0
        /\ lock = 0
        /\ woken = [r \in Readers |-> FALSE]
        /\ cancelled = [r \in Readers |-> FALSE]
        /\ outcome = [r \in Readers |-> "none"]
        /\ retrying = [r \in Readers |-> FALSE]
        /\ failedSinceWait = [r \in Readers |-> FALSE]
        /\ retriedCancelled = [r \in Readers |-> FALSE]
        /\ sawEnfile = FALSE
        /\ pc = [self \in ProcSet |-> CASE self \in Readers -> "Attempt"
                                        [] self = InProcId -> "InLoop"
                                        [] self = OtherSysId -> "SysLoop"
                                        [] self = CancelId -> "CancelLoop"]

Attempt(self) == /\ pc[self] = "Attempt"
                 /\ \/ /\ ~Locked(self) \/ lock = 0
                       /\ IF Locked(self)
                             THEN /\ lock' = self
                             ELSE /\ TRUE
                                  /\ lock' = lock
                       /\ woken' = [woken EXCEPT ![self] = FALSE]
                       /\ IF TwoStep
                             THEN /\ pc' = [pc EXCEPT ![self] = "OpenDir"]
                             ELSE /\ pc' = [pc EXCEPT ![self] = "OpenFile"]
                       /\ UNCHANGED outcome
                    \/ /\ SlotCancel /\ Locked(self) /\ cancelled[self]
                       /\ outcome' = [outcome EXCEPT ![self] = "cancelled"]
                       /\ pc' = [pc EXCEPT ![self] = "Done"]
                       /\ UNCHANGED <<lock, woken>>
                 /\ UNCHANGED << dirHeld, fileHeld, inProc, otherSys, 
                                 cancelled, retrying, failedSinceWait, 
                                 retriedCancelled, sawEnfile >>

OpenDir(self) == /\ pc[self] = "OpenDir"
                 /\ IF CanOpen
                       THEN /\ dirHeld' = [dirHeld EXCEPT ![self] = dirHeld[self] + 1]
                            /\ pc' = [pc EXCEPT ![self] = "OpenFile"]
                            /\ UNCHANGED << lock, retrying, failedSinceWait, 
                                            sawEnfile >>
                       ELSE /\ failedSinceWait' = [failedSinceWait EXCEPT ![self] = TRUE]
                            /\ retrying' = [retrying EXCEPT ![self] = TRUE]
                            /\ sawEnfile' = (sawEnfile \/ ProcUse < ProcCap)
                            /\ IF lock = self
                                  THEN /\ lock' = 0
                                  ELSE /\ TRUE
                                       /\ lock' = lock
                            /\ IF Backoff
                                  THEN /\ pc' = [pc EXCEPT ![self] = "Wait"]
                                  ELSE /\ pc' = [pc EXCEPT ![self] = "Attempt"]
                            /\ UNCHANGED dirHeld
                 /\ UNCHANGED << fileHeld, inProc, otherSys, woken, cancelled, 
                                 outcome, retriedCancelled >>

OpenFile(self) == /\ pc[self] = "OpenFile"
                  /\ IF CanOpen
                        THEN /\ fileHeld' = [fileHeld EXCEPT ![self] = fileHeld[self] + 1]
                             /\ IF lock = self
                                   THEN /\ lock' = 0
                                   ELSE /\ TRUE
                                        /\ lock' = lock
                             /\ pc' = [pc EXCEPT ![self] = "Read"]
                             /\ UNCHANGED << dirHeld, retrying, 
                                             failedSinceWait, sawEnfile >>
                        ELSE /\ IF ReleaseOnWait
                                   THEN /\ dirHeld' = [dirHeld EXCEPT ![self] = 0]
                                   ELSE /\ TRUE
                                        /\ UNCHANGED dirHeld
                             /\ failedSinceWait' = [failedSinceWait EXCEPT ![self] = TRUE]
                             /\ retrying' = [retrying EXCEPT ![self] = TRUE]
                             /\ sawEnfile' = (sawEnfile \/ ProcUse < ProcCap)
                             /\ IF lock = self
                                   THEN /\ lock' = 0
                                   ELSE /\ TRUE
                                        /\ lock' = lock
                             /\ IF Backoff
                                   THEN /\ pc' = [pc EXCEPT ![self] = "Wait"]
                                   ELSE /\ pc' = [pc EXCEPT ![self] = "Attempt"]
                             /\ UNCHANGED fileHeld
                  /\ UNCHANGED << inProc, otherSys, woken, cancelled, outcome, 
                                  retriedCancelled >>

Wait(self) == /\ pc[self] = "Wait"
              /\ \/ /\ woken[self] \/ Timer
                    /\ IF CancelCheck /\ cancelled[self]
                          THEN /\ dirHeld' = [dirHeld EXCEPT ![self] = 0]
                               /\ outcome' = [outcome EXCEPT ![self] = "cancelled"]
                               /\ pc' = [pc EXCEPT ![self] = "Done"]
                               /\ UNCHANGED << failedSinceWait, 
                                               retriedCancelled >>
                          ELSE /\ retriedCancelled' = [retriedCancelled EXCEPT ![self] = retriedCancelled[self] \/ cancelled[self]]
                               /\ failedSinceWait' = [failedSinceWait EXCEPT ![self] = FALSE]
                               /\ pc' = [pc EXCEPT ![self] = "Attempt"]
                               /\ UNCHANGED << dirHeld, outcome >>
                 \/ /\ cancelled[self]
                    /\ dirHeld' = [dirHeld EXCEPT ![self] = 0]
                    /\ outcome' = [outcome EXCEPT ![self] = "cancelled"]
                    /\ pc' = [pc EXCEPT ![self] = "Done"]
                    /\ UNCHANGED <<failedSinceWait, retriedCancelled>>
              /\ UNCHANGED << fileHeld, inProc, otherSys, lock, woken, 
                              cancelled, retrying, sawEnfile >>

Read(self) == /\ pc[self] = "Read"
              /\ TRUE
              /\ pc' = [pc EXCEPT ![self] = "Close"]
              /\ UNCHANGED << dirHeld, fileHeld, inProc, otherSys, lock, woken, 
                              cancelled, outcome, retrying, failedSinceWait, 
                              retriedCancelled, sawEnfile >>

Close(self) == /\ pc[self] = "Close"
               /\ IF CloseAfterRead
                     THEN /\ dirHeld' = [dirHeld EXCEPT ![self] = 0]
                          /\ fileHeld' = [fileHeld EXCEPT ![self] = 0]
                          /\ woken' = [r \in Readers |-> TRUE]
                     ELSE /\ TRUE
                          /\ UNCHANGED << dirHeld, fileHeld, woken >>
               /\ outcome' = [outcome EXCEPT ![self] = "ok"]
               /\ IF Recycle
                     THEN /\ retrying' = [retrying EXCEPT ![self] = FALSE]
                          /\ pc' = [pc EXCEPT ![self] = "Attempt"]
                     ELSE /\ pc' = [pc EXCEPT ![self] = "Done"]
                          /\ UNCHANGED retrying
               /\ UNCHANGED << inProc, otherSys, lock, cancelled, 
                               failedSinceWait, retriedCancelled, sawEnfile >>

Reader(self) == Attempt(self) \/ OpenDir(self) \/ OpenFile(self)
                   \/ Wait(self) \/ Read(self) \/ Close(self)

InLoop == /\ pc[InProcId] = "InLoop"
          /\ \/ /\ CanOpen
                /\ inProc' = inProc + 1
             \/ /\ inProc > 0
                /\ inProc' = inProc - 1
          /\ pc' = [pc EXCEPT ![InProcId] = "InLoop"]
          /\ UNCHANGED << dirHeld, fileHeld, otherSys, lock, woken, cancelled, 
                          outcome, retrying, failedSinceWait, retriedCancelled, 
                          sawEnfile >>

InProcess == InLoop

SysLoop == /\ pc[OtherSysId] = "SysLoop"
           /\ \/ /\ SysUse < SysCap
                 /\ otherSys' = otherSys + 1
              \/ /\ otherSys > 0
                 /\ otherSys' = otherSys - 1
           /\ pc' = [pc EXCEPT ![OtherSysId] = "SysLoop"]
           /\ UNCHANGED << dirHeld, fileHeld, inProc, lock, woken, cancelled, 
                           outcome, retrying, failedSinceWait, 
                           retriedCancelled, sawEnfile >>

OtherProcesses == SysLoop

CancelLoop == /\ pc[CancelId] = "CancelLoop"
              /\ IF Cancelable
                    THEN /\ \E r \in {x \in Readers : ~cancelled[x]}:
                              cancelled' = [cancelled EXCEPT ![r] = TRUE]
                         /\ pc' = [pc EXCEPT ![CancelId] = "CancelLoop"]
                    ELSE /\ pc' = [pc EXCEPT ![CancelId] = "Done"]
                         /\ UNCHANGED cancelled
              /\ UNCHANGED << dirHeld, fileHeld, inProc, otherSys, lock, woken, 
                              outcome, retrying, failedSinceWait, 
                              retriedCancelled, sawEnfile >>

Canceller == CancelLoop

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == InProcess \/ OtherProcesses \/ Canceller
           \/ (\E self \in Readers: Reader(self))
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in Readers : WF_vars(Reader(self))

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION 
==============================================================================
