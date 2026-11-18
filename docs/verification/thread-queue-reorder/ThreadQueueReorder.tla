---- MODULE ThreadQueueReorder ----
EXTENDS Integers, Sequences, FiniteSets, TLC

CONSTANTS N, A, Hidden, Persons, Fixed

ASSUME N \in Nat /\ A \in Nat /\ Hidden \subseteq 1..N /\ Fixed \in BOOLEAN

Items == 1..(N + A)

(* --algorithm ThreadQueueReorder {
variables
  waiting = 1..N,
  pos = [x \in Items |-> x - 1],
  order = [k \in 1..N |-> k],
  created = N,
  ran = <<>>,
  removed = {},
  outOfOrder = FALSE,
  staleClaim = FALSE,
  changed = FALSE,
  pickedAfterChange = FALSE,
  staleList = FALSE,
  failed = FALSE;

define {
  Range(s) == {s[k] : k \in DOMAIN s}
  Remove(s, x) == SelectSeq(s, LAMBDA y : y # x)
  Before(x, y) == pos[x] < pos[y] \/ (pos[x] = pos[y] /\ x < y)
  DBOrder == [r \in 1..Cardinality(waiting) |-> CHOOSE x \in waiting : Cardinality({y \in waiting : Before(y, x)}) = r - 1]
  Perms(s) == {p \in [1..Len(s) -> Range(s)] : Range(p) = Range(s)}
  Apply(o, l) ==
    LET S == {k \in 1..Len(o) : o[k] \in Range(l)}
        P == SelectSeq(l, LAMBDA x : x \in Range(o))
    IN [k \in 1..Len(o) |-> IF k \in S THEN P[Cardinality({j \in S : j <= k})] ELSE o[k]]
  Renumber(o) == [x \in Items |-> IF x \in Range(o) THEN (CHOOSE k \in 1..Len(o) : o[k] = x) - 1 ELSE pos[x]]
  Max(S) == CHOOSE m \in S : \A s \in S : s <= m
  Min(S) == CHOOSE m \in S : \A s \in S : m <= s
  Positions == {pos[x] : x \in waiting}
}

fair process (worker = "worker")
variable picked = 0;
{
pick:
  while (TRUE) {
    await waiting # {};
    picked := Head(DBOrder);
    outOfOrder := outOfOrder \/ Head(DBOrder) # Head(order);
    pickedAfterChange := pickedAfterChange \/ changed;
claim:
    if (picked \in waiting) {
      staleClaim := staleClaim \/ picked # Head(order);
      waiting := waiting \ {picked};
      order := Remove(order, picked);
      ran := Append(ran, picked);
    }
  }
}

fair process (person \in Persons)
variables view = <<>>, list = <<>>, i = 1, ok = TRUE;
{
look:
  view := SelectSeq(DBOrder, LAMBDA x : x \notin Hidden);
submit:
  with (l \in Perms(view)) {
    list := l;
  };
  if (Fixed) {
    either {
      staleList := staleList \/ Range(list) # waiting \ Hidden;
      changed := changed \/ Apply(order, list) # order;
      pos := Renumber(Apply(DBOrder, list));
      order := Apply(order, list);
    } or {
      failed := TRUE;
    }
  } else {
statement:
    while (i <= Len(list)) {
      either {
        if (list[i] \in waiting) {
          pos[list[i]] := i - 1;
        };
        i := i + 1;
      } or {
        ok := FALSE;
        i := Len(list) + 1;
      }
    };
    if (ok) {
      staleList := staleList \/ Range(list) # waiting \ Hidden;
      changed := changed \/ Apply(order, list) # order;
      order := Apply(order, list);
    } else {
      failed := TRUE;
    }
  };
}

fair process (adder = "adder")
{
add:
  while (created < N + A) {
    with (n = created + 1) {
      either {
        pos[n] := IF waiting = {} THEN 0 ELSE Max(Positions) + 1;
        order := Append(order, n);
      } or {
        pos[n] := IF waiting = {} THEN 0 ELSE Min(Positions) - 1;
        order := <<n>> \o order;
      };
      waiting := waiting \cup {n};
      created := n;
    }
  }
}

fair process (remover = "remover")
{
remove:
  with (x \in waiting) {
    waiting := waiting \ {x};
    order := Remove(order, x);
    removed := removed \cup {x};
  }
}
} *)
\* BEGIN TRANSLATION
VARIABLES pc, waiting, pos, order, created, ran, removed, outOfOrder, 
          staleClaim, changed, pickedAfterChange, staleList, failed

(* define statement *)
Range(s) == {s[k] : k \in DOMAIN s}
Remove(s, x) == SelectSeq(s, LAMBDA y : y # x)
Before(x, y) == pos[x] < pos[y] \/ (pos[x] = pos[y] /\ x < y)
DBOrder == [r \in 1..Cardinality(waiting) |-> CHOOSE x \in waiting : Cardinality({y \in waiting : Before(y, x)}) = r - 1]
Perms(s) == {p \in [1..Len(s) -> Range(s)] : Range(p) = Range(s)}
Apply(o, l) ==
  LET S == {k \in 1..Len(o) : o[k] \in Range(l)}
      P == SelectSeq(l, LAMBDA x : x \in Range(o))
  IN [k \in 1..Len(o) |-> IF k \in S THEN P[Cardinality({j \in S : j <= k})] ELSE o[k]]
Renumber(o) == [x \in Items |-> IF x \in Range(o) THEN (CHOOSE k \in 1..Len(o) : o[k] = x) - 1 ELSE pos[x]]
Max(S) == CHOOSE m \in S : \A s \in S : s <= m
Min(S) == CHOOSE m \in S : \A s \in S : m <= s
Positions == {pos[x] : x \in waiting}

VARIABLES picked, view, list, i, ok

vars == << pc, waiting, pos, order, created, ran, removed, outOfOrder, 
           staleClaim, changed, pickedAfterChange, staleList, failed, picked, 
           view, list, i, ok >>

ProcSet == {"worker"} \cup (Persons) \cup {"adder"} \cup {"remover"}

Init == (* Global variables *)
        /\ waiting = 1..N
        /\ pos = [x \in Items |-> x - 1]
        /\ order = [k \in 1..N |-> k]
        /\ created = N
        /\ ran = <<>>
        /\ removed = {}
        /\ outOfOrder = FALSE
        /\ staleClaim = FALSE
        /\ changed = FALSE
        /\ pickedAfterChange = FALSE
        /\ staleList = FALSE
        /\ failed = FALSE
        (* Process worker *)
        /\ picked = 0
        (* Process person *)
        /\ view = [self \in Persons |-> <<>>]
        /\ list = [self \in Persons |-> <<>>]
        /\ i = [self \in Persons |-> 1]
        /\ ok = [self \in Persons |-> TRUE]
        /\ pc = [self \in ProcSet |-> CASE self = "worker" -> "pick"
                                        [] self \in Persons -> "look"
                                        [] self = "adder" -> "add"
                                        [] self = "remover" -> "remove"]

pick == /\ pc["worker"] = "pick"
        /\ waiting # {}
        /\ picked' = Head(DBOrder)
        /\ outOfOrder' = (outOfOrder \/ Head(DBOrder) # Head(order))
        /\ pickedAfterChange' = (pickedAfterChange \/ changed)
        /\ pc' = [pc EXCEPT !["worker"] = "claim"]
        /\ UNCHANGED << waiting, pos, order, created, ran, removed, staleClaim, 
                        changed, staleList, failed, view, list, i, ok >>

claim == /\ pc["worker"] = "claim"
         /\ IF picked \in waiting
               THEN /\ staleClaim' = (staleClaim \/ picked # Head(order))
                    /\ waiting' = waiting \ {picked}
                    /\ order' = Remove(order, picked)
                    /\ ran' = Append(ran, picked)
               ELSE /\ TRUE
                    /\ UNCHANGED << waiting, order, ran, staleClaim >>
         /\ pc' = [pc EXCEPT !["worker"] = "pick"]
         /\ UNCHANGED << pos, created, removed, outOfOrder, changed, 
                         pickedAfterChange, staleList, failed, picked, view, 
                         list, i, ok >>

worker == pick \/ claim

look(self) == /\ pc[self] = "look"
              /\ view' = [view EXCEPT ![self] = SelectSeq(DBOrder, LAMBDA x : x \notin Hidden)]
              /\ pc' = [pc EXCEPT ![self] = "submit"]
              /\ UNCHANGED << waiting, pos, order, created, ran, removed, 
                              outOfOrder, staleClaim, changed, 
                              pickedAfterChange, staleList, failed, picked, 
                              list, i, ok >>

submit(self) == /\ pc[self] = "submit"
                /\ \E l \in Perms(view[self]):
                     list' = [list EXCEPT ![self] = l]
                /\ IF Fixed
                      THEN /\ \/ /\ staleList' = (staleList \/ Range(list'[self]) # waiting \ Hidden)
                                 /\ changed' = (changed \/ Apply(order, list'[self]) # order)
                                 /\ pos' = Renumber(Apply(DBOrder, list'[self]))
                                 /\ order' = Apply(order, list'[self])
                                 /\ UNCHANGED failed
                              \/ /\ failed' = TRUE
                                 /\ UNCHANGED <<pos, order, changed, staleList>>
                           /\ pc' = [pc EXCEPT ![self] = "Done"]
                      ELSE /\ pc' = [pc EXCEPT ![self] = "statement"]
                           /\ UNCHANGED << pos, order, changed, staleList, 
                                           failed >>
                /\ UNCHANGED << waiting, created, ran, removed, outOfOrder, 
                                staleClaim, pickedAfterChange, picked, view, i, 
                                ok >>

statement(self) == /\ pc[self] = "statement"
                   /\ IF i[self] <= Len(list[self])
                         THEN /\ \/ /\ IF list[self][i[self]] \in waiting
                                          THEN /\ pos' = [pos EXCEPT ![list[self][i[self]]] = i[self] - 1]
                                          ELSE /\ TRUE
                                               /\ pos' = pos
                                    /\ i' = [i EXCEPT ![self] = i[self] + 1]
                                    /\ ok' = ok
                                 \/ /\ ok' = [ok EXCEPT ![self] = FALSE]
                                    /\ i' = [i EXCEPT ![self] = Len(list[self]) + 1]
                                    /\ pos' = pos
                              /\ pc' = [pc EXCEPT ![self] = "statement"]
                              /\ UNCHANGED << order, changed, staleList, 
                                              failed >>
                         ELSE /\ IF ok[self]
                                    THEN /\ staleList' = (staleList \/ Range(list[self]) # waiting \ Hidden)
                                         /\ changed' = (changed \/ Apply(order, list[self]) # order)
                                         /\ order' = Apply(order, list[self])
                                         /\ UNCHANGED failed
                                    ELSE /\ failed' = TRUE
                                         /\ UNCHANGED << order, changed, 
                                                         staleList >>
                              /\ pc' = [pc EXCEPT ![self] = "Done"]
                              /\ UNCHANGED << pos, i, ok >>
                   /\ UNCHANGED << waiting, created, ran, removed, outOfOrder, 
                                   staleClaim, pickedAfterChange, picked, view, 
                                   list >>

person(self) == look(self) \/ submit(self) \/ statement(self)

add == /\ pc["adder"] = "add"
       /\ IF created < N + A
             THEN /\ LET n == created + 1 IN
                       /\ \/ /\ pos' = [pos EXCEPT ![n] = IF waiting = {} THEN 0 ELSE Max(Positions) + 1]
                             /\ order' = Append(order, n)
                          \/ /\ pos' = [pos EXCEPT ![n] = IF waiting = {} THEN 0 ELSE Min(Positions) - 1]
                             /\ order' = <<n>> \o order
                       /\ waiting' = (waiting \cup {n})
                       /\ created' = n
                  /\ pc' = [pc EXCEPT !["adder"] = "add"]
             ELSE /\ pc' = [pc EXCEPT !["adder"] = "Done"]
                  /\ UNCHANGED << waiting, pos, order, created >>
       /\ UNCHANGED << ran, removed, outOfOrder, staleClaim, changed, 
                       pickedAfterChange, staleList, failed, picked, view, 
                       list, i, ok >>

adder == add

remove == /\ pc["remover"] = "remove"
          /\ \E x \in waiting:
               /\ waiting' = waiting \ {x}
               /\ order' = Remove(order, x)
               /\ removed' = (removed \cup {x})
          /\ pc' = [pc EXCEPT !["remover"] = "Done"]
          /\ UNCHANGED << pos, created, ran, outOfOrder, staleClaim, changed, 
                          pickedAfterChange, staleList, failed, picked, view, 
                          list, i, ok >>

remover == remove

Next == worker \/ adder \/ remover
           \/ (\E self \in Persons: person(self))

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(worker)
        /\ \A self \in Persons : WF_vars(person(self))
        /\ WF_vars(adder)
        /\ WF_vars(remover)

\* END TRANSLATION

TypeOK ==
  /\ waiting \subseteq Items
  /\ pos \in [Items -> Int]
  /\ order \in Seq(Items)
  /\ removed \subseteq Items
  /\ created \in N..(N + A)

DistinctPositions == \A x, y \in waiting : x # y => pos[x] # pos[y]

OrderMatches == (\A p \in Persons : pc[p] # "statement") => DBOrder = order

WorkerFollowsOrder == ~outOfOrder

NoLossNoDup ==
  /\ Len(ran) = Cardinality(Range(ran))
  /\ Len(order) = Cardinality(waiting)
  /\ Range(order) = waiting
  /\ waiting \cap Range(ran) = {}
  /\ waiting \cap removed = {}
  /\ Range(ran) \cap removed = {}
  /\ waiting \cup Range(ran) \cup removed = 1..created

Drained == <>[](waiting = {})

NeverChanged == ~changed
NeverPickedAfterChange == ~pickedAfterChange
NeverStaleList == ~staleList
NeverFailed == ~failed
NoStaleClaim == ~staleClaim
====
