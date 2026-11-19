---- MODULE OpenCodePrune ----
EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS Sessions, PageSize, Budget, Order, ForgetOnFailure, MaxChanges, MaxFailures

NoAnchor == <<0, 0>>
Key(s, time) == <<time[s], s>>
Below(a, b) == a[1] < b[1] \/ (a[1] = b[1] /\ a[2] < b[2])
Ahead(a, b) == IF Order = "asc" THEN Below(a, b) ELSE Below(b, a)
Listable(live, time, anchor) ==
    IF anchor = NoAnchor THEN live ELSE {s \in live : Ahead(anchor, Key(s, time))}
RECURSIVE Take(_, _, _)
Take(set, n, time) ==
    IF n = 0 \/ set = {} THEN <<>>
    ELSE LET first == CHOOSE s \in set : \A other \in set \ {s} : Ahead(Key(s, time), Key(other, time))
         IN <<first>> \o Take(set \ {first}, n - 1, time)
Listed(page) == {page[i] : i \in 1..Len(page)}

(* --algorithm OpenCodePrune
variables live = Sessions,
          time = [s \in Sessions |-> 1],
          clock = 2,
          changes = 0,
          local = Sessions,
          forgotten = {},
          failures = 0,
          pages = 0,
          done = FALSE;

process Server = "server"
begin
Change:
    while changes < MaxChanges do
        with s \in live do
            either
                time[s] := clock;
                clock := clock + 1;
            or
                live := live \ {s};
            end either;
        end with;
        changes := changes + 1;
    end while;
end process;

fair process Prune = "prune"
variables seen = {}, anchor = NoAnchor;
begin
Pass:
    seen := {};
    anchor := NoAnchor;
    pages := 0;
Fetch:
    with page = Take(Listable(live, time, anchor), PageSize, time) do
        either
            await Len(page) <= Budget;
            pages := pages + 1;
            if page = <<>> then
                forgotten := forgotten \cup (local \ seen);
                local := local \cap seen;
                done := TRUE;
                goto Done;
            else
                seen := seen \cup Listed(page);
                anchor := Key(page[Len(page)], time);
                goto Fetch;
            end if;
        or
            await Len(page) > Budget \/ failures < MaxFailures;
            if Len(page) <= Budget then
                failures := failures + 1;
            end if;
            if ForgetOnFailure then
                forgotten := forgotten \cup (local \ seen);
                local := local \cap seen;
            end if;
            goto Pass;
        end either;
    end with;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, live, time, clock, changes, local, forgotten, failures, pages, 
          done, seen, anchor

vars == << pc, live, time, clock, changes, local, forgotten, failures, pages, 
           done, seen, anchor >>

ProcSet == {"server"} \cup {"prune"}

Init == (* Global variables *)
        /\ live = Sessions
        /\ time = [s \in Sessions |-> 1]
        /\ clock = 2
        /\ changes = 0
        /\ local = Sessions
        /\ forgotten = {}
        /\ failures = 0
        /\ pages = 0
        /\ done = FALSE
        (* Process Prune *)
        /\ seen = {}
        /\ anchor = NoAnchor
        /\ pc = [self \in ProcSet |-> CASE self = "server" -> "Change"
                                        [] self = "prune" -> "Pass"]

Change == /\ pc["server"] = "Change"
          /\ IF changes < MaxChanges
                THEN /\ \E s \in live:
                          \/ /\ time' = [time EXCEPT ![s] = clock]
                             /\ clock' = clock + 1
                             /\ live' = live
                          \/ /\ live' = live \ {s}
                             /\ UNCHANGED <<time, clock>>
                     /\ changes' = changes + 1
                     /\ pc' = [pc EXCEPT !["server"] = "Change"]
                ELSE /\ pc' = [pc EXCEPT !["server"] = "Done"]
                     /\ UNCHANGED << live, time, clock, changes >>
          /\ UNCHANGED << local, forgotten, failures, pages, done, seen, 
                          anchor >>

Server == Change

Pass == /\ pc["prune"] = "Pass"
        /\ seen' = {}
        /\ anchor' = NoAnchor
        /\ pages' = 0
        /\ pc' = [pc EXCEPT !["prune"] = "Fetch"]
        /\ UNCHANGED << live, time, clock, changes, local, forgotten, failures, 
                        done >>

Fetch == /\ pc["prune"] = "Fetch"
         /\ LET page == Take(Listable(live, time, anchor), PageSize, time) IN
              \/ /\ Len(page) <= Budget
                 /\ pages' = pages + 1
                 /\ IF page = <<>>
                       THEN /\ forgotten' = (forgotten \cup (local \ seen))
                            /\ local' = (local \cap seen)
                            /\ done' = TRUE
                            /\ pc' = [pc EXCEPT !["prune"] = "Done"]
                            /\ UNCHANGED << seen, anchor >>
                       ELSE /\ seen' = (seen \cup Listed(page))
                            /\ anchor' = Key(page[Len(page)], time)
                            /\ pc' = [pc EXCEPT !["prune"] = "Fetch"]
                            /\ UNCHANGED << local, forgotten, done >>
                 /\ UNCHANGED failures
              \/ /\ Len(page) > Budget \/ failures < MaxFailures
                 /\ IF Len(page) <= Budget
                       THEN /\ failures' = failures + 1
                       ELSE /\ TRUE
                            /\ UNCHANGED failures
                 /\ IF ForgetOnFailure
                       THEN /\ forgotten' = (forgotten \cup (local \ seen))
                            /\ local' = (local \cap seen)
                       ELSE /\ TRUE
                            /\ UNCHANGED << local, forgotten >>
                 /\ pc' = [pc EXCEPT !["prune"] = "Pass"]
                 /\ UNCHANGED <<pages, done, seen, anchor>>
         /\ UNCHANGED << live, time, clock, changes >>

Prune == Pass \/ Fetch

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == Server \/ Prune
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Prune)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION

NoLiveForgotten == forgotten \cap live = {}

PruneCompletes == <>done

NothingForgotten == forgotten = {}

NeverMultiPage == done => pages <= 2
====
