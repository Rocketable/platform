------------------------- MODULE StickyAttachmentRetention -------------------------
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Files, Msgs, Trim, Unlocked, MaxFailures

Live == {"fresh", "old"}

(* --algorithm StickyAttachmentRetention
variables
    row = [f \in Files |-> FALSE],
    blob = [f \in Files |-> FALSE],
    uploaded = {},
    queue = {},
    state = [m \in Msgs |-> "absent"],
    pinned = [m \in Msgs |-> FALSE],
    refs = [m \in Msgs |-> {}],
    cleared = {},
    inflight = {},
    failures = 0;

define
    Referenced(f, states) == \E m \in Msgs : state[m] \in states /\ f \in refs[m]
    Absent == {m \in Msgs : state[m] = "absent"}
    Rows == {f \in Files : row[f]}
    Unreferenced == {f \in Rows : ~Referenced(f, Live \cup {"removed"})}
    Orphaned(f) == Referenced(f, {"removed"}) /\ ~Referenced(f, Live)
    TypeOK ==
        /\ row \in [Files -> BOOLEAN]
        /\ blob \in [Files -> BOOLEAN]
        /\ uploaded \subseteq Files
        /\ queue \subseteq Files
        /\ cleared \subseteq Files
        /\ state \in [Msgs -> {"absent", "fresh", "old", "removed"}]
        /\ refs \in [Msgs -> SUBSET Files]
    KeptResolve == \A m \in Msgs : state[m] \in Live => \A f \in refs[m] : row[f] /\ blob[f]
    RowHasBlob == \A f \in Files : row[f] => blob[f]
    QueuedHasNoRow == \A f \in queue : ~row[f]
    OnlyRemovedReferences == \A f \in cleared : Referenced(f, {"removed"})
    NoLeak == <>[](\A f \in uploaded : Orphaned(f) => ~row[f] /\ ~blob[f])
end define;

process User = "user"
begin
Act:
    either
        with f \in Files \ uploaded do
            row[f] := TRUE;
            blob[f] := TRUE;
            uploaded := uploaded \cup {f};
        end with;
        goto Act;
    or
        with m \in Absent, s \in (SUBSET Rows) \ {{}}, p \in BOOLEAN do
            state[m] := "fresh";
            refs[m] := s;
            pinned[m] := p;
        end with;
        goto Act;
    or
        await Unlocked # "none" /\ Absent # {};
        with s \in (SUBSET (IF Unlocked = "fresh" THEN Unreferenced ELSE Rows)) \ {{}} do
            inflight := s;
        end with;
    or
        goto Done;
    end either;
Insert:
    with m \in Absent, p \in BOOLEAN do
        state[m] := "fresh";
        refs[m] := inflight;
        pinned[m] := p;
        inflight := {};
    end with;
    goto Act;
end process;

process Clock = "clock"
begin
Age:
    while TRUE do
        await inflight = {};
        with m \in {m \in Msgs : state[m] = "fresh" /\ ~pinned[m]} do
            state[m] := "old";
        end with;
    end while;
end process;

fair process Retention = "retention"
begin
Prune:
    while TRUE do
        await \E m \in Msgs : state[m] = "old";
        with removed = {m \in Msgs : state[m] = "old"},
             kept = {m \in Msgs : state[m] = "fresh"},
             candidates = UNION {refs[m] : m \in removed},
             orphans = IF Trim = "current" THEN {}
                       ELSE {f \in candidates : row[f] /\ (Trim = "unguarded" \/ \A k \in kept : f \notin refs[k])} do
            state := [m \in Msgs |-> IF m \in removed THEN "removed" ELSE state[m]];
            row := [f \in Files |-> row[f] /\ f \notin orphans];
            queue := queue \cup orphans;
            cleared := cleared \cup orphans;
        end with;
    end while;
end process;

fair process Drainer \in {"d1", "d2"}
variables file = "none";
begin
Pick:
    while TRUE do
        with f \in queue do
            file := f;
        end with;
Delete:
        either
            blob[file] := FALSE;
Dequeue:
            queue := queue \ {file};
        or
            await failures < MaxFailures;
            failures := failures + 1;
        end either;
    end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, row, blob, uploaded, queue, state, pinned, refs, cleared, 
          inflight, failures

(* define statement *)
Referenced(f, states) == \E m \in Msgs : state[m] \in states /\ f \in refs[m]
Absent == {m \in Msgs : state[m] = "absent"}
Rows == {f \in Files : row[f]}
Unreferenced == {f \in Rows : ~Referenced(f, Live \cup {"removed"})}
Orphaned(f) == Referenced(f, {"removed"}) /\ ~Referenced(f, Live)
TypeOK ==
    /\ row \in [Files -> BOOLEAN]
    /\ blob \in [Files -> BOOLEAN]
    /\ uploaded \subseteq Files
    /\ queue \subseteq Files
    /\ cleared \subseteq Files
    /\ state \in [Msgs -> {"absent", "fresh", "old", "removed"}]
    /\ refs \in [Msgs -> SUBSET Files]
KeptResolve == \A m \in Msgs : state[m] \in Live => \A f \in refs[m] : row[f] /\ blob[f]
RowHasBlob == \A f \in Files : row[f] => blob[f]
QueuedHasNoRow == \A f \in queue : ~row[f]
OnlyRemovedReferences == \A f \in cleared : Referenced(f, {"removed"})
NoLeak == <>[](\A f \in uploaded : Orphaned(f) => ~row[f] /\ ~blob[f])

VARIABLE file

vars == << pc, row, blob, uploaded, queue, state, pinned, refs, cleared, 
           inflight, failures, file >>

ProcSet == {"user"} \cup {"clock"} \cup {"retention"} \cup ({"d1", "d2"})

Init == (* Global variables *)
        /\ row = [f \in Files |-> FALSE]
        /\ blob = [f \in Files |-> FALSE]
        /\ uploaded = {}
        /\ queue = {}
        /\ state = [m \in Msgs |-> "absent"]
        /\ pinned = [m \in Msgs |-> FALSE]
        /\ refs = [m \in Msgs |-> {}]
        /\ cleared = {}
        /\ inflight = {}
        /\ failures = 0
        (* Process Drainer *)
        /\ file = [self \in {"d1", "d2"} |-> "none"]
        /\ pc = [self \in ProcSet |-> CASE self = "user" -> "Act"
                                        [] self = "clock" -> "Age"
                                        [] self = "retention" -> "Prune"
                                        [] self \in {"d1", "d2"} -> "Pick"]

Act == /\ pc["user"] = "Act"
       /\ \/ /\ \E f \in Files \ uploaded:
                  /\ row' = [row EXCEPT ![f] = TRUE]
                  /\ blob' = [blob EXCEPT ![f] = TRUE]
                  /\ uploaded' = (uploaded \cup {f})
             /\ pc' = [pc EXCEPT !["user"] = "Act"]
             /\ UNCHANGED <<state, pinned, refs, inflight>>
          \/ /\ \E m \in Absent:
                  \E s \in (SUBSET Rows) \ {{}}:
                    \E p \in BOOLEAN:
                      /\ state' = [state EXCEPT ![m] = "fresh"]
                      /\ refs' = [refs EXCEPT ![m] = s]
                      /\ pinned' = [pinned EXCEPT ![m] = p]
             /\ pc' = [pc EXCEPT !["user"] = "Act"]
             /\ UNCHANGED <<row, blob, uploaded, inflight>>
          \/ /\ Unlocked # "none" /\ Absent # {}
             /\ \E s \in (SUBSET (IF Unlocked = "fresh" THEN Unreferenced ELSE Rows)) \ {{}}:
                  inflight' = s
             /\ pc' = [pc EXCEPT !["user"] = "Insert"]
             /\ UNCHANGED <<row, blob, uploaded, state, pinned, refs>>
          \/ /\ pc' = [pc EXCEPT !["user"] = "Done"]
             /\ UNCHANGED <<row, blob, uploaded, state, pinned, refs, inflight>>
       /\ UNCHANGED << queue, cleared, failures, file >>

Insert == /\ pc["user"] = "Insert"
          /\ \E m \in Absent:
               \E p \in BOOLEAN:
                 /\ state' = [state EXCEPT ![m] = "fresh"]
                 /\ refs' = [refs EXCEPT ![m] = inflight]
                 /\ pinned' = [pinned EXCEPT ![m] = p]
                 /\ inflight' = {}
          /\ pc' = [pc EXCEPT !["user"] = "Act"]
          /\ UNCHANGED << row, blob, uploaded, queue, cleared, failures, file >>

User == Act \/ Insert

Age == /\ pc["clock"] = "Age"
       /\ inflight = {}
       /\ \E m \in {m \in Msgs : state[m] = "fresh" /\ ~pinned[m]}:
            state' = [state EXCEPT ![m] = "old"]
       /\ pc' = [pc EXCEPT !["clock"] = "Age"]
       /\ UNCHANGED << row, blob, uploaded, queue, pinned, refs, cleared, 
                       inflight, failures, file >>

Clock == Age

Prune == /\ pc["retention"] = "Prune"
         /\ \E m \in Msgs : state[m] = "old"
         /\ LET removed == {m \in Msgs : state[m] = "old"} IN
              LET kept == {m \in Msgs : state[m] = "fresh"} IN
                LET candidates == UNION {refs[m] : m \in removed} IN
                  LET orphans == IF Trim = "current" THEN {}
                                 ELSE {f \in candidates : row[f] /\ (Trim = "unguarded" \/ \A k \in kept : f \notin refs[k])} IN
                    /\ state' = [m \in Msgs |-> IF m \in removed THEN "removed" ELSE state[m]]
                    /\ row' = [f \in Files |-> row[f] /\ f \notin orphans]
                    /\ queue' = (queue \cup orphans)
                    /\ cleared' = (cleared \cup orphans)
         /\ pc' = [pc EXCEPT !["retention"] = "Prune"]
         /\ UNCHANGED << blob, uploaded, pinned, refs, inflight, failures, 
                         file >>

Retention == Prune

Pick(self) == /\ pc[self] = "Pick"
              /\ \E f \in queue:
                   file' = [file EXCEPT ![self] = f]
              /\ pc' = [pc EXCEPT ![self] = "Delete"]
              /\ UNCHANGED << row, blob, uploaded, queue, state, pinned, refs, 
                              cleared, inflight, failures >>

Delete(self) == /\ pc[self] = "Delete"
                /\ \/ /\ blob' = [blob EXCEPT ![file[self]] = FALSE]
                      /\ pc' = [pc EXCEPT ![self] = "Dequeue"]
                      /\ UNCHANGED failures
                   \/ /\ failures < MaxFailures
                      /\ failures' = failures + 1
                      /\ pc' = [pc EXCEPT ![self] = "Pick"]
                      /\ blob' = blob
                /\ UNCHANGED << row, uploaded, queue, state, pinned, refs, 
                                cleared, inflight, file >>

Dequeue(self) == /\ pc[self] = "Dequeue"
                 /\ queue' = queue \ {file[self]}
                 /\ pc' = [pc EXCEPT ![self] = "Pick"]
                 /\ UNCHANGED << row, blob, uploaded, state, pinned, refs, 
                                 cleared, inflight, failures, file >>

Drainer(self) == Pick(self) \/ Delete(self) \/ Dequeue(self)

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == User \/ Clock \/ Retention
           \/ (\E self \in {"d1", "d2"}: Drainer(self))
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Retention)
        /\ \A self \in {"d1", "d2"} : WF_vars(Drainer(self))

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION
=============================================================================
