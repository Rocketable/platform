------------------------------ MODULE MessageTree ------------------------------
EXTENDS Integers, Sequences, FiniteSets

(* One native chat's Message Tree as planned (Mutation = "none"); each other Mutation turns one rule off.
   Entries are small integers in creation order; entry 0 is the root. Each label is one step of the code
   with the lock or transaction it holds; README.md maps every label to its function. A crash drops what a
   process holds in memory (the running turn, the history-mutation flag) and keeps the database. *)

CONSTANTS MaxEntries, Viewers, MaxMoves, Inputs, OutsideArrivals, MaxSummaries, MaxDeletes, MaxCrashes, Mutation

ASSUME Mutation \in {"none", "commit-deletes", "no-floor-check", "delete-outside", "park-drops-queue",
                     "delete-without-interrupt", "shadow-id-cut"}

Root == 0
Empty == -1

(* --algorithm MessageTree
variables
    created = {}, parent = [e \in 1..MaxEntries |-> Root], next = 1,
    outside = {}, floor = Root,
    deleted = {}, branchDeleted = {},
    latest = Root, head = Empty,
    queue = <<>>, enqueued = 0, ran = 0,
    running = FALSE, anchor = Root,
    mutating = FALSE, boot = 0, crashes = 0,
    shadowView = {Root},
    pending = {}, badSummary = FALSE,
    moves = [v \in Viewers |-> 0], deletes = 0, summaries = 0, arrivals = 0;

define
    Live == ({Root} \cup created) \ deleted
    Leaf == IF head = Empty THEN latest ELSE head
    RECURSIVE Ancestors(_)
    Ancestors(e) == IF e = Root THEN {Root} ELSE {e} \cup Ancestors(parent[e])
    Path == Ancestors(Leaf)
    Descends(t, a) == a \in Ancestors(t)
    Children(e) == {c \in created \ deleted : parent[c] = e}
    Subtree(b) == {e \in created \ deleted : b \in Ancestors(e)}
    BranchStarts == {b \in created \ deleted : Cardinality(Children(parent[b])) >= 2}
    ShadowFor(t) == IF Mutation = "shadow-id-cut" THEN {e \in Live : e <= t} ELSE Ancestors(t)

    NoLoss == deleted = branchDeleted /\ \A e \in created \ deleted : parent[e] \in Live
    HeadAfterFloor == Descends(Leaf, floor)
    OutsideNeverDeleted == outside \cap deleted = {}
    HeadIsNode == Leaf \in Live
    QueueKept == enqueued = ran + Len(queue)
    OnePath == running => anchor \in Path
    NoLiveTurnDeleted == running => anchor \notin deleted
    ShadowFollowsParent == shadowView \subseteq Path
    SummaryLandsOffBranch == ~badSummary
    NeverBranched == \A e \in Live : Cardinality(Children(e)) <= 1

    QueuedEventuallyRun == <>[](Len(queue) = 0)
end define;

macro Append() begin
    if Mutation = "commit-deletes" /\ head # Empty then
        deleted := deleted \cup (created \ Ancestors(head));
    end if;
    created := created \cup {next};
    parent[next] := Leaf;
    shadowView := shadowView \cup {next};
    latest := next;
    head := Empty;
    next := next + 1;
end macro;

procedure Move(target = Root, checked = TRUE)
variable b = 0;
begin
M1:
    await ~mutating;
    mutating := TRUE;
    b := boot;
M2:
    if boot = b then
        running := FALSE;
        if Mutation = "park-drops-queue" then
            queue := <<>>;
        end if;
    end if;
M3:
    if boot = b then
        if target \in Live /\ (Descends(target, floor) \/ ~checked \/ Mutation = "no-floor-check") then
            head := IF target = latest THEN Empty ELSE target;
            shadowView := ShadowFor(target);
        end if;
        mutating := FALSE;
    end if;
    return;
end procedure;

procedure DeleteBranch(first = 1)
begin
D1:
    if first \in Live /\ Mutation # "delete-without-interrupt"
       /\ (Leaf \in Subtree(first) \/ (running /\ anchor \in Subtree(first))) then
        call Move(parent[first], Mutation # "delete-outside");
    end if;
D2:
    if /\ first \in Live
       /\ (Mutation = "delete-without-interrupt" \/ (Leaf \notin Subtree(first) /\ ~(running /\ anchor \in Subtree(first)))) then
        if Leaf \in Subtree(first) then head := parent[first]; end if;
        branchDeleted := branchDeleted \cup Subtree(first);
        deleted := deleted \cup Subtree(first);
        shadowView := shadowView \ Subtree(first);
    end if;
    return;
end procedure;

fair process Viewer \in Viewers
variable pick = 0;
begin
V:
    either
        await enqueued < Inputs;
        queue := Append(queue, enqueued + 1);
        enqueued := enqueued + 1;
        goto V;
    or
        await moves[self] < MaxMoves;
        moves[self] := moves[self] + 1;
        with t \in Live do pick := t; end with;
        call Move(pick, TRUE);
        goto V;
    or
        await deletes < MaxDeletes /\ BranchStarts # {};
        deletes := deletes + 1;
        with f \in BranchStarts do pick := f; end with;
        call DeleteBranch(pick);
        goto V;
    or
        await summaries < MaxSummaries /\ (created \ deleted) \ Path # {};
        summaries := summaries + 1;
        with s \in (created \ deleted) \ Path do pending := pending \cup {s}; end with;
        goto V;
    or
        skip;
    end either;
end process;

fair process Agent = "agent"
begin
AStart:
    await ~mutating /\ ~running /\ queue # <<>>;
    running := TRUE;
    anchor := Leaf;
    queue := Tail(queue);
    ran := ran + 1;
AFinish:
    if running /\ next <= MaxEntries then
        Append();
    end if;
    running := FALSE;
    goto AStart;
end process;

fair process Summary = "summary"
begin
S1:
    await pending # {};
    with s \in pending do
        pending := pending \ {s};
        if s \notin Path /\ s \notin deleted /\ ~running /\ next <= MaxEntries then
            Append();
        end if;
    end with;
    goto S1;
end process;

process Outside = "outside"
begin
O1:
    await arrivals < OutsideArrivals /\ next <= MaxEntries;
    arrivals := arrivals + 1;
    outside := outside \cup {next};
    floor := next;
    Append();
end process;

process Crash = "crash"
begin
C1:
    await crashes < MaxCrashes;
    crashes := crashes + 1;
    boot := boot + 1;
    running := FALSE;
    mutating := FALSE;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, created, parent, next, outside, floor, deleted, branchDeleted, 
          latest, head, queue, enqueued, ran, running, anchor, mutating, boot, 
          crashes, shadowView, pending, badSummary, moves, deletes, summaries, 
          arrivals, stack

(* define statement *)
Live == ({Root} \cup created) \ deleted
Leaf == IF head = Empty THEN latest ELSE head
RECURSIVE Ancestors(_)
Ancestors(e) == IF e = Root THEN {Root} ELSE {e} \cup Ancestors(parent[e])
Path == Ancestors(Leaf)
Descends(t, a) == a \in Ancestors(t)
Children(e) == {c \in created \ deleted : parent[c] = e}
Subtree(b) == {e \in created \ deleted : b \in Ancestors(e)}
BranchStarts == {b \in created \ deleted : Cardinality(Children(parent[b])) >= 2}
ShadowFor(t) == IF Mutation = "shadow-id-cut" THEN {e \in Live : e <= t} ELSE Ancestors(t)

NoLoss == deleted = branchDeleted /\ \A e \in created \ deleted : parent[e] \in Live
HeadAfterFloor == Descends(Leaf, floor)
OutsideNeverDeleted == outside \cap deleted = {}
HeadIsNode == Leaf \in Live
QueueKept == enqueued = ran + Len(queue)
OnePath == running => anchor \in Path
NoLiveTurnDeleted == running => anchor \notin deleted
ShadowFollowsParent == shadowView \subseteq Path
SummaryLandsOffBranch == ~badSummary
NeverBranched == \A e \in Live : Cardinality(Children(e)) <= 1

QueuedEventuallyRun == <>[](Len(queue) = 0)

VARIABLES target, checked, b, first, pick

vars == << pc, created, parent, next, outside, floor, deleted, branchDeleted, 
           latest, head, queue, enqueued, ran, running, anchor, mutating, 
           boot, crashes, shadowView, pending, badSummary, moves, deletes, 
           summaries, arrivals, stack, target, checked, b, first, pick >>

ProcSet == (Viewers) \cup {"agent"} \cup {"summary"} \cup {"outside"} \cup {"crash"}

Init == (* Global variables *)
        /\ created = {}
        /\ parent = [e \in 1..MaxEntries |-> Root]
        /\ next = 1
        /\ outside = {}
        /\ floor = Root
        /\ deleted = {}
        /\ branchDeleted = {}
        /\ latest = Root
        /\ head = Empty
        /\ queue = <<>>
        /\ enqueued = 0
        /\ ran = 0
        /\ running = FALSE
        /\ anchor = Root
        /\ mutating = FALSE
        /\ boot = 0
        /\ crashes = 0
        /\ shadowView = {Root}
        /\ pending = {}
        /\ badSummary = FALSE
        /\ moves = [v \in Viewers |-> 0]
        /\ deletes = 0
        /\ summaries = 0
        /\ arrivals = 0
        (* Procedure Move *)
        /\ target = [ self \in ProcSet |-> Root]
        /\ checked = [ self \in ProcSet |-> TRUE]
        /\ b = [ self \in ProcSet |-> 0]
        (* Procedure DeleteBranch *)
        /\ first = [ self \in ProcSet |-> 1]
        (* Process Viewer *)
        /\ pick = [self \in Viewers |-> 0]
        /\ stack = [self \in ProcSet |-> << >>]
        /\ pc = [self \in ProcSet |-> CASE self \in Viewers -> "V"
                                        [] self = "agent" -> "AStart"
                                        [] self = "summary" -> "S1"
                                        [] self = "outside" -> "O1"
                                        [] self = "crash" -> "C1"]

M1(self) == /\ pc[self] = "M1"
            /\ ~mutating
            /\ mutating' = TRUE
            /\ b' = [b EXCEPT ![self] = boot]
            /\ pc' = [pc EXCEPT ![self] = "M2"]
            /\ UNCHANGED << created, parent, next, outside, floor, deleted, 
                            branchDeleted, latest, head, queue, enqueued, ran, 
                            running, anchor, boot, crashes, shadowView, 
                            pending, badSummary, moves, deletes, summaries, 
                            arrivals, stack, target, checked, first, pick >>

M2(self) == /\ pc[self] = "M2"
            /\ IF boot = b[self]
                  THEN /\ running' = FALSE
                       /\ IF Mutation = "park-drops-queue"
                             THEN /\ queue' = <<>>
                             ELSE /\ TRUE
                                  /\ queue' = queue
                  ELSE /\ TRUE
                       /\ UNCHANGED << queue, running >>
            /\ pc' = [pc EXCEPT ![self] = "M3"]
            /\ UNCHANGED << created, parent, next, outside, floor, deleted, 
                            branchDeleted, latest, head, enqueued, ran, anchor, 
                            mutating, boot, crashes, shadowView, pending, 
                            badSummary, moves, deletes, summaries, arrivals, 
                            stack, target, checked, b, first, pick >>

M3(self) == /\ pc[self] = "M3"
            /\ IF boot = b[self]
                  THEN /\ IF target[self] \in Live /\ (Descends(target[self], floor) \/ ~checked[self] \/ Mutation = "no-floor-check")
                             THEN /\ head' = (IF target[self] = latest THEN Empty ELSE target[self])
                                  /\ shadowView' = ShadowFor(target[self])
                             ELSE /\ TRUE
                                  /\ UNCHANGED << head, shadowView >>
                       /\ mutating' = FALSE
                  ELSE /\ TRUE
                       /\ UNCHANGED << head, mutating, shadowView >>
            /\ pc' = [pc EXCEPT ![self] = Head(stack[self]).pc]
            /\ b' = [b EXCEPT ![self] = Head(stack[self]).b]
            /\ target' = [target EXCEPT ![self] = Head(stack[self]).target]
            /\ checked' = [checked EXCEPT ![self] = Head(stack[self]).checked]
            /\ stack' = [stack EXCEPT ![self] = Tail(stack[self])]
            /\ UNCHANGED << created, parent, next, outside, floor, deleted, 
                            branchDeleted, latest, queue, enqueued, ran, 
                            running, anchor, boot, crashes, pending, 
                            badSummary, moves, deletes, summaries, arrivals, 
                            first, pick >>

Move(self) == M1(self) \/ M2(self) \/ M3(self)

D1(self) == /\ pc[self] = "D1"
            /\ IF first[self] \in Live /\ Mutation # "delete-without-interrupt"
                  /\ (Leaf \in Subtree(first[self]) \/ (running /\ anchor \in Subtree(first[self])))
                  THEN /\ /\ checked' = [checked EXCEPT ![self] = Mutation # "delete-outside"]
                          /\ stack' = [stack EXCEPT ![self] = << [ procedure |->  "Move",
                                                                   pc        |->  "D2",
                                                                   b         |->  b[self],
                                                                   target    |->  target[self],
                                                                   checked   |->  checked[self] ] >>
                                                               \o stack[self]]
                          /\ target' = [target EXCEPT ![self] = parent[first[self]]]
                       /\ b' = [b EXCEPT ![self] = 0]
                       /\ pc' = [pc EXCEPT ![self] = "M1"]
                  ELSE /\ pc' = [pc EXCEPT ![self] = "D2"]
                       /\ UNCHANGED << stack, target, checked, b >>
            /\ UNCHANGED << created, parent, next, outside, floor, deleted, 
                            branchDeleted, latest, head, queue, enqueued, ran, 
                            running, anchor, mutating, boot, crashes, 
                            shadowView, pending, badSummary, moves, deletes, 
                            summaries, arrivals, first, pick >>

D2(self) == /\ pc[self] = "D2"
            /\ IF /\ first[self] \in Live
                  /\ (Mutation = "delete-without-interrupt" \/ (Leaf \notin Subtree(first[self]) /\ ~(running /\ anchor \in Subtree(first[self]))))
                  THEN /\ IF Leaf \in Subtree(first[self])
                             THEN /\ head' = parent[first[self]]
                             ELSE /\ TRUE
                                  /\ head' = head
                       /\ branchDeleted' = (branchDeleted \cup Subtree(first[self]))
                       /\ deleted' = (deleted \cup Subtree(first[self]))
                       /\ shadowView' = shadowView \ Subtree(first[self])
                  ELSE /\ TRUE
                       /\ UNCHANGED << deleted, branchDeleted, head, 
                                       shadowView >>
            /\ pc' = [pc EXCEPT ![self] = Head(stack[self]).pc]
            /\ first' = [first EXCEPT ![self] = Head(stack[self]).first]
            /\ stack' = [stack EXCEPT ![self] = Tail(stack[self])]
            /\ UNCHANGED << created, parent, next, outside, floor, latest, 
                            queue, enqueued, ran, running, anchor, mutating, 
                            boot, crashes, pending, badSummary, moves, deletes, 
                            summaries, arrivals, target, checked, b, pick >>

DeleteBranch(self) == D1(self) \/ D2(self)

V(self) == /\ pc[self] = "V"
           /\ \/ /\ enqueued < Inputs
                 /\ queue' = Append(queue, enqueued + 1)
                 /\ enqueued' = enqueued + 1
                 /\ pc' = [pc EXCEPT ![self] = "V"]
                 /\ UNCHANGED <<pending, moves, deletes, summaries, stack, target, checked, b, first, pick>>
              \/ /\ moves[self] < MaxMoves
                 /\ moves' = [moves EXCEPT ![self] = moves[self] + 1]
                 /\ \E t \in Live:
                      pick' = [pick EXCEPT ![self] = t]
                 /\ /\ checked' = [checked EXCEPT ![self] = TRUE]
                    /\ stack' = [stack EXCEPT ![self] = << [ procedure |->  "Move",
                                                             pc        |->  "V",
                                                             b         |->  b[self],
                                                             target    |->  target[self],
                                                             checked   |->  checked[self] ] >>
                                                         \o stack[self]]
                    /\ target' = [target EXCEPT ![self] = pick'[self]]
                 /\ b' = [b EXCEPT ![self] = 0]
                 /\ pc' = [pc EXCEPT ![self] = "M1"]
                 /\ UNCHANGED <<queue, enqueued, pending, deletes, summaries, first>>
              \/ /\ deletes < MaxDeletes /\ BranchStarts # {}
                 /\ deletes' = deletes + 1
                 /\ \E f \in BranchStarts:
                      pick' = [pick EXCEPT ![self] = f]
                 /\ /\ first' = [first EXCEPT ![self] = pick'[self]]
                    /\ stack' = [stack EXCEPT ![self] = << [ procedure |->  "DeleteBranch",
                                                             pc        |->  "V",
                                                             first     |->  first[self] ] >>
                                                         \o stack[self]]
                 /\ pc' = [pc EXCEPT ![self] = "D1"]
                 /\ UNCHANGED <<queue, enqueued, pending, moves, summaries, target, checked, b>>
              \/ /\ summaries < MaxSummaries /\ (created \ deleted) \ Path # {}
                 /\ summaries' = summaries + 1
                 /\ \E s \in (created \ deleted) \ Path:
                      pending' = (pending \cup {s})
                 /\ pc' = [pc EXCEPT ![self] = "V"]
                 /\ UNCHANGED <<queue, enqueued, moves, deletes, stack, target, checked, b, first, pick>>
              \/ /\ TRUE
                 /\ pc' = [pc EXCEPT ![self] = "Done"]
                 /\ UNCHANGED <<queue, enqueued, pending, moves, deletes, summaries, stack, target, checked, b, first, pick>>
           /\ UNCHANGED << created, parent, next, outside, floor, deleted, 
                           branchDeleted, latest, head, ran, running, anchor, 
                           mutating, boot, crashes, shadowView, badSummary, 
                           arrivals >>

Viewer(self) == V(self)

AStart == /\ pc["agent"] = "AStart"
          /\ ~mutating /\ ~running /\ queue # <<>>
          /\ running' = TRUE
          /\ anchor' = Leaf
          /\ queue' = Tail(queue)
          /\ ran' = ran + 1
          /\ pc' = [pc EXCEPT !["agent"] = "AFinish"]
          /\ UNCHANGED << created, parent, next, outside, floor, deleted, 
                          branchDeleted, latest, head, enqueued, mutating, 
                          boot, crashes, shadowView, pending, badSummary, 
                          moves, deletes, summaries, arrivals, stack, target, 
                          checked, b, first, pick >>

AFinish == /\ pc["agent"] = "AFinish"
           /\ IF running /\ next <= MaxEntries
                 THEN /\ IF Mutation = "commit-deletes" /\ head # Empty
                            THEN /\ deleted' = (deleted \cup (created \ Ancestors(head)))
                            ELSE /\ TRUE
                                 /\ UNCHANGED deleted
                      /\ created' = (created \cup {next})
                      /\ parent' = [parent EXCEPT ![next] = Leaf]
                      /\ shadowView' = (shadowView \cup {next})
                      /\ latest' = next
                      /\ head' = Empty
                      /\ next' = next + 1
                 ELSE /\ TRUE
                      /\ UNCHANGED << created, parent, next, deleted, latest, 
                                      head, shadowView >>
           /\ running' = FALSE
           /\ pc' = [pc EXCEPT !["agent"] = "AStart"]
           /\ UNCHANGED << outside, floor, branchDeleted, queue, enqueued, ran, 
                           anchor, mutating, boot, crashes, pending, 
                           badSummary, moves, deletes, summaries, arrivals, 
                           stack, target, checked, b, first, pick >>

Agent == AStart \/ AFinish

S1 == /\ pc["summary"] = "S1"
      /\ pending # {}
      /\ \E s \in pending:
           /\ pending' = pending \ {s}
           /\ IF s \notin Path /\ s \notin deleted /\ ~running /\ next <= MaxEntries
                 THEN /\ IF Mutation = "commit-deletes" /\ head # Empty
                            THEN /\ deleted' = (deleted \cup (created \ Ancestors(head)))
                            ELSE /\ TRUE
                                 /\ UNCHANGED deleted
                      /\ created' = (created \cup {next})
                      /\ parent' = [parent EXCEPT ![next] = Leaf]
                      /\ shadowView' = (shadowView \cup {next})
                      /\ latest' = next
                      /\ head' = Empty
                      /\ next' = next + 1
                 ELSE /\ TRUE
                      /\ UNCHANGED << created, parent, next, deleted, latest, 
                                      head, shadowView >>
      /\ pc' = [pc EXCEPT !["summary"] = "S1"]
      /\ UNCHANGED << outside, floor, branchDeleted, queue, enqueued, ran, 
                      running, anchor, mutating, boot, crashes, badSummary, 
                      moves, deletes, summaries, arrivals, stack, target, 
                      checked, b, first, pick >>

Summary == S1

O1 == /\ pc["outside"] = "O1"
      /\ arrivals < OutsideArrivals /\ next <= MaxEntries
      /\ arrivals' = arrivals + 1
      /\ outside' = (outside \cup {next})
      /\ floor' = next
      /\ IF Mutation = "commit-deletes" /\ head # Empty
            THEN /\ deleted' = (deleted \cup (created \ Ancestors(head)))
            ELSE /\ TRUE
                 /\ UNCHANGED deleted
      /\ created' = (created \cup {next})
      /\ parent' = [parent EXCEPT ![next] = Leaf]
      /\ shadowView' = (shadowView \cup {next})
      /\ latest' = next
      /\ head' = Empty
      /\ next' = next + 1
      /\ pc' = [pc EXCEPT !["outside"] = "Done"]
      /\ UNCHANGED << branchDeleted, queue, enqueued, ran, running, anchor, 
                      mutating, boot, crashes, pending, badSummary, moves, 
                      deletes, summaries, stack, target, checked, b, first, 
                      pick >>

Outside == O1

C1 == /\ pc["crash"] = "C1"
      /\ crashes < MaxCrashes
      /\ crashes' = crashes + 1
      /\ boot' = boot + 1
      /\ running' = FALSE
      /\ mutating' = FALSE
      /\ pc' = [pc EXCEPT !["crash"] = "Done"]
      /\ UNCHANGED << created, parent, next, outside, floor, deleted, 
                      branchDeleted, latest, head, queue, enqueued, ran, 
                      anchor, shadowView, pending, badSummary, moves, deletes, 
                      summaries, arrivals, stack, target, checked, b, first, 
                      pick >>

Crash == C1

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == Agent \/ Summary \/ Outside \/ Crash
           \/ (\E self \in ProcSet: Move(self) \/ DeleteBranch(self))
           \/ (\E self \in Viewers: Viewer(self))
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in Viewers : WF_vars(Viewer(self)) /\ WF_vars(Move(self)) /\ WF_vars(DeleteBranch(self))
        /\ WF_vars(Agent)
        /\ WF_vars(Summary)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION
=============================================================================
