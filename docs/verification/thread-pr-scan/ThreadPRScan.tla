---------------------------- MODULE ThreadPRScan ----------------------------
(* The incremental pull request scan of a remote OpenCode thread
   (internal/rocketclaw/web/src/thread-pr.ts loadThreadPRs). A look walks the
   thread's session tree. Each session keeps what its finished messages showed
   up to a boundary message; a later look reads only messages newer than the
   boundary, and reads a session at all only when it is the thread or its
   updated time moved. A session's children are listed again when it changed
   or already had children.

   Messages carry increasing IDs. A message may show a pull request before it
   finishes (a completed tool call in a running reply), and may still show
   more until it finishes. Reverting a session deletes its newest messages.
   Starting a subagent adds a message to its parent.

   A session's updated time is known from its parent's latest children
   listing. Each session read and each children listing is one atomic step:
   the model abstracts paging. The requirement is checked for looks that no server change
   interleaved with.

   The CONSTANTS:
   - Fault: "none" is the design; "no-boundary-check" keeps the boundary even
     when its message is gone; "past-unfinished" moves the boundary past an
     unfinished message; "changed-parents-only" lists children only of changed
     sessions; "silent-spawn" starts a subagent without changing its parent.
   - MaxChanges: how many server changes may happen. *)
EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS Fault, MaxChanges

Sessions == {"root", "child", "grand"}
Parent(s) == IF s = "child" THEN "root" ELSE IF s = "grand" THEN "child" ELSE "none"
MaxLog == 2
Ids(log) == {log[i].id : i \in 1..Len(log)}
Shown(s, log) == {<<s, log[i].id>> : i \in {j \in 1..Len(log) : log[j].pr}}
Truth(logs, live) == UNION {Shown(s, logs[s]) : s \in {t \in Sessions : live[t]}}
After(log, boundary) == SelectSeq(log, LAMBDA m : m.id > boundary)
Open(new) == {i \in 1..Len(new) : ~new[i].done}
Stable(new) ==
    IF Fault = "past-unfinished" THEN SelectSeq(new, LAMBDA m : m.done)
    ELSE IF Open(new) = {} THEN new
    ELSE SubSeq(new, 1, (CHOOSE i \in Open(new) : \A j \in Open(new) : i <= j) - 1)
EmptyCache == [seen |-> FALSE, updated |-> 0, boundary |-> 0, stable |-> {}, result |-> {}, children |-> {}]

(* --algorithm ThreadPRScan
variables
    logs = [s \in Sessions |-> <<>>], live = [s \in Sessions |-> s = "root"],
    updated = [s \in Sessions |-> 0], nextId = 1, changes = 0,
    cache = [s \in Sessions |-> EmptyCache], listed = [s \in Sessions |-> 0],
    frontier = {}, visited = {}, startChanges = 0, wrong = FALSE, looks = 0;

define
    Agreement == ~wrong
end define;

macro Bump(s) begin
    updated[s] := updated[s] + 1;
    changes := changes + 1;
end macro;

process Server = "server"
begin
    Change:
        while changes < MaxChanges do
            either
                with s \in {t \in Sessions : live[t] /\ Len(logs[t]) < MaxLog}, done \in BOOLEAN, pr \in BOOLEAN do
                    logs[s] := Append(logs[s], [id |-> nextId, done |-> done, pr |-> pr]);
                    nextId := nextId + 1;
                    Bump(s);
                end with;
            or
                with s \in {t \in Sessions : live[t]}, i \in 1..Len(logs[s]), done \in BOOLEAN do
                    await ~logs[s][i].done;
                    logs[s][i] := [logs[s][i] EXCEPT !.done = done, !.pr = TRUE];
                    Bump(s);
                end with;
            or
                with s \in {t \in Sessions : live[t] /\ logs[t] # <<>>}, keep \in 0..(Len(logs[s]) - 1) do
                    logs[s] := SubSeq(logs[s], 1, keep);
                    Bump(s);
                end with;
            or
                with c \in {t \in Sessions : ~live[t] /\ Parent(t) # "none" /\ live[Parent(t)]} do
                    live[c] := TRUE;
                    changes := changes + 1;
                    if Fault # "silent-spawn" /\ Len(logs[Parent(c)]) < MaxLog then
                        logs[Parent(c)] := Append(logs[Parent(c)], [id |-> nextId, done |-> TRUE, pr |-> FALSE]);
                        nextId := nextId + 1;
                        updated[Parent(c)] := updated[Parent(c)] + 1;
                    end if;
                end with;
            end either;
        end while;
end process;

fair process Scanner = "scanner"
begin
    Look:
        while looks < MaxChanges + 2 do
            frontier := {"root"};
            visited := {};
            startChanges := changes;
            looks := looks + 1;
    Read:
            while frontier # {} do
                with s \in frontier, c = cache[s], kids = {t \in Sessions : live[t] /\ Parent(t) = s} do
                    if s = "root" \/ ~c.seen \/ c.updated # listed[s] then
                        with known = c.seen /\ (c.boundary = 0 \/ c.boundary \in Ids(logs[s]) \/ Fault = "no-boundary-check"),
                             base = IF known THEN c.boundary ELSE 0,
                             kept = IF known THEN c.stable ELSE {},
                             new = After(logs[s], base),
                             stable = Stable(new) do
                            cache[s] := [seen |-> TRUE, updated |-> updated[s],
                                boundary |-> IF stable = <<>> THEN base ELSE stable[Len(stable)].id,
                                stable |-> kept \cup Shown(s, stable),
                                result |-> kept \cup Shown(s, new),
                                children |-> kids];
                            frontier := (frontier \ {s}) \cup (kids \ (visited \cup {s}));
                            listed := [t \in Sessions |-> IF t \in kids THEN updated[t] ELSE listed[t]];
                        end with;
                    elsif c.children # {} /\ Fault # "changed-parents-only" then
                        cache[s].children := kids;
                        frontier := (frontier \ {s}) \cup (kids \ (visited \cup {s}));
                        listed := [t \in Sessions |-> IF t \in kids THEN updated[t] ELSE listed[t]];
                    else
                        frontier := (frontier \ {s}) \cup (c.children \ (visited \cup {s}));
                    end if;
                    visited := visited \cup {s};
                end with;
            end while;
    Commit:
            if changes = startChanges /\ UNION {cache[s].result : s \in visited} # Truth(logs, live) then
                wrong := TRUE;
            end if;
        end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, logs, live, updated, nextId, changes, cache, listed, frontier, 
          visited, startChanges, wrong, looks

(* define statement *)
Agreement == ~wrong


vars == << pc, logs, live, updated, nextId, changes, cache, listed, frontier, 
           visited, startChanges, wrong, looks >>

ProcSet == {"server"} \cup {"scanner"}

Init == (* Global variables *)
        /\ logs = [s \in Sessions |-> <<>>]
        /\ live = [s \in Sessions |-> s = "root"]
        /\ updated = [s \in Sessions |-> 0]
        /\ nextId = 1
        /\ changes = 0
        /\ cache = [s \in Sessions |-> EmptyCache]
        /\ listed = [s \in Sessions |-> 0]
        /\ frontier = {}
        /\ visited = {}
        /\ startChanges = 0
        /\ wrong = FALSE
        /\ looks = 0
        /\ pc = [self \in ProcSet |-> CASE self = "server" -> "Change"
                                        [] self = "scanner" -> "Look"]

Change == /\ pc["server"] = "Change"
          /\ IF changes < MaxChanges
                THEN /\ \/ /\ \E s \in {t \in Sessions : live[t] /\ Len(logs[t]) < MaxLog}:
                                \E done \in BOOLEAN:
                                  \E pr \in BOOLEAN:
                                    /\ logs' = [logs EXCEPT ![s] = Append(logs[s], [id |-> nextId, done |-> done, pr |-> pr])]
                                    /\ nextId' = nextId + 1
                                    /\ updated' = [updated EXCEPT ![s] = updated[s] + 1]
                                    /\ changes' = changes + 1
                           /\ live' = live
                        \/ /\ \E s \in {t \in Sessions : live[t]}:
                                \E i \in 1..Len(logs[s]):
                                  \E done \in BOOLEAN:
                                    /\ ~logs[s][i].done
                                    /\ logs' = [logs EXCEPT ![s][i] = [logs[s][i] EXCEPT !.done = done, !.pr = TRUE]]
                                    /\ updated' = [updated EXCEPT ![s] = updated[s] + 1]
                                    /\ changes' = changes + 1
                           /\ UNCHANGED <<live, nextId>>
                        \/ /\ \E s \in {t \in Sessions : live[t] /\ logs[t] # <<>>}:
                                \E keep \in 0..(Len(logs[s]) - 1):
                                  /\ logs' = [logs EXCEPT ![s] = SubSeq(logs[s], 1, keep)]
                                  /\ updated' = [updated EXCEPT ![s] = updated[s] + 1]
                                  /\ changes' = changes + 1
                           /\ UNCHANGED <<live, nextId>>
                        \/ /\ \E c \in {t \in Sessions : ~live[t] /\ Parent(t) # "none" /\ live[Parent(t)]}:
                                /\ live' = [live EXCEPT ![c] = TRUE]
                                /\ changes' = changes + 1
                                /\ IF Fault # "silent-spawn" /\ Len(logs[Parent(c)]) < MaxLog
                                      THEN /\ logs' = [logs EXCEPT ![Parent(c)] = Append(logs[Parent(c)], [id |-> nextId, done |-> TRUE, pr |-> FALSE])]
                                           /\ nextId' = nextId + 1
                                           /\ updated' = [updated EXCEPT ![Parent(c)] = updated[Parent(c)] + 1]
                                      ELSE /\ TRUE
                                           /\ UNCHANGED << logs, updated, 
                                                           nextId >>
                     /\ pc' = [pc EXCEPT !["server"] = "Change"]
                ELSE /\ pc' = [pc EXCEPT !["server"] = "Done"]
                     /\ UNCHANGED << logs, live, updated, nextId, changes >>
          /\ UNCHANGED << cache, listed, frontier, visited, startChanges, 
                          wrong, looks >>

Server == Change

Look == /\ pc["scanner"] = "Look"
        /\ IF looks < MaxChanges + 2
              THEN /\ frontier' = {"root"}
                   /\ visited' = {}
                   /\ startChanges' = changes
                   /\ looks' = looks + 1
                   /\ pc' = [pc EXCEPT !["scanner"] = "Read"]
              ELSE /\ pc' = [pc EXCEPT !["scanner"] = "Done"]
                   /\ UNCHANGED << frontier, visited, startChanges, looks >>
        /\ UNCHANGED << logs, live, updated, nextId, changes, cache, listed, 
                        wrong >>

Read == /\ pc["scanner"] = "Read"
        /\ IF frontier # {}
              THEN /\ \E s \in frontier:
                        LET c == cache[s] IN
                          LET kids == {t \in Sessions : live[t] /\ Parent(t) = s} IN
                            /\ IF s = "root" \/ ~c.seen \/ c.updated # listed[s]
                                  THEN /\ LET known == c.seen /\ (c.boundary = 0 \/ c.boundary \in Ids(logs[s]) \/ Fault = "no-boundary-check") IN
                                            LET base == IF known THEN c.boundary ELSE 0 IN
                                              LET kept == IF known THEN c.stable ELSE {} IN
                                                LET new == After(logs[s], base) IN
                                                  LET stable == Stable(new) IN
                                                    /\ cache' = [cache EXCEPT ![s] =         [seen |-> TRUE, updated |-> updated[s],
                                                                                     boundary |-> IF stable = <<>> THEN base ELSE stable[Len(stable)].id,
                                                                                     stable |-> kept \cup Shown(s, stable),
                                                                                     result |-> kept \cup Shown(s, new),
                                                                                     children |-> kids]]
                                                    /\ frontier' = ((frontier \ {s}) \cup (kids \ (visited \cup {s})))
                                                    /\ listed' = [t \in Sessions |-> IF t \in kids THEN updated[t] ELSE listed[t]]
                                  ELSE /\ IF c.children # {} /\ Fault # "changed-parents-only"
                                             THEN /\ cache' = [cache EXCEPT ![s].children = kids]
                                                  /\ frontier' = ((frontier \ {s}) \cup (kids \ (visited \cup {s})))
                                                  /\ listed' = [t \in Sessions |-> IF t \in kids THEN updated[t] ELSE listed[t]]
                                             ELSE /\ frontier' = ((frontier \ {s}) \cup (c.children \ (visited \cup {s})))
                                                  /\ UNCHANGED << cache, 
                                                                  listed >>
                            /\ visited' = (visited \cup {s})
                   /\ pc' = [pc EXCEPT !["scanner"] = "Read"]
              ELSE /\ pc' = [pc EXCEPT !["scanner"] = "Commit"]
                   /\ UNCHANGED << cache, listed, frontier, visited >>
        /\ UNCHANGED << logs, live, updated, nextId, changes, startChanges, 
                        wrong, looks >>

Commit == /\ pc["scanner"] = "Commit"
          /\ IF changes = startChanges /\ UNION {cache[s].result : s \in visited} # Truth(logs, live)
                THEN /\ wrong' = TRUE
                ELSE /\ TRUE
                     /\ wrong' = wrong
          /\ pc' = [pc EXCEPT !["scanner"] = "Look"]
          /\ UNCHANGED << logs, live, updated, nextId, changes, cache, listed, 
                          frontier, visited, startChanges, looks >>

Scanner == Look \/ Read \/ Commit

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == Server \/ Scanner
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Scanner)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION
=============================================================================
