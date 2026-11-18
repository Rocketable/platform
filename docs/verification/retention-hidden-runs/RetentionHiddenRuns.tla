----------------------------- MODULE RetentionHiddenRuns -----------------------------
EXTENDS Naturals, Sequences

CONSTANTS Shape

ASSUME Shape \in {"current", "fixed", "unguarded"}

Convs == {"chat", "sticky", "run", "sub", "draft"}
Order == <<"sticky", "chat", "run", "sub", "draft">>
Owners == {"chat", "run"}

(* --algorithm RetentionHiddenRuns
variables
    alive = [c \in Convs |-> TRUE],
    msgs = [c \in Convs |-> "none"],
    age = [c \in Convs |-> "fresh"],
    pending = [c \in Owners |-> FALSE],
    reportsTo = "none",
    continued = FALSE,
    next = 1,
    violations = {},
    seen = {};

define
    ShowsRun == alive["run"] /\ (reportsTo = "chat" \/ continued)
    Shown(c) == IF c = "chat" /\ ShowsRun THEN {"chat", "run"} ELSE {c}
    Pending(c) == CASE c = "chat" -> pending["chat"] \/ (reportsTo = "chat" /\ alive["run"] /\ pending["run"])
                    [] c = "run" -> pending["run"]
                    [] OTHER -> FALSE
    LatestBefore(c) == IF \A d \in Shown(c) : msgs[d] = "none" THEN age[c] = "old" ELSE \A d \in Shown(c) : msgs[d] # "fresh"
    EmptyManaged(c) == age[c] = "old" /\ \A d \in Shown(c) : msgs[d] = "none"
    Standalone == reportsTo = "none" /\ ~continued
    DatedByMessages(c) == \/ c = "chat"
                          \/ c = "run" /\ (Shape = "unguarded" \/ (Shape = "fixed" /\ Standalone))
    Stale(c) == ~Pending(c) /\ IF DatedByMessages(c) THEN LatestBefore(c) ELSE EmptyManaged(c)
    Doomed(c) == CASE c = "chat" -> {d \in {"chat", "sub"} : alive[d]} \cup (IF ShowsRun THEN {"run"} ELSE {})
                   [] OTHER -> {c}
    Harm(D) == (IF \E d \in D : msgs[d] = "fresh" THEN {"fresh"} ELSE {})
        \cup (IF \E d \in D \cap Owners : pending[d] THEN {"pending"} ELSE {})
        \cup (IF "run" \in D /\ "chat" \notin D /\ ShowsRun /\ msgs["run"] # "none" /\ ~Stale("chat") THEN {"shown"} ELSE {})
        \cup (IF "sub" \in D /\ "chat" \notin D /\ msgs["sub"] # "none" THEN {"owned"} ELSE {})
        \cup (IF "draft" \in D /\ msgs["draft"] # "none" THEN {"owned"} ELSE {})
    Witness(D) == (IF D = {"run"} /\ Standalone /\ msgs["run"] = "old" THEN {"standalone run deleted"} ELSE {})
        \cup (IF "run" \in D /\ reportsTo = "sticky" THEN {"sticky run deleted"} ELSE {})
        \cup (IF "chat" \in D /\ "run" \in D THEN {"chat deleted with its run"} ELSE {})

    TypeOK == /\ alive \in [Convs -> BOOLEAN] /\ alive["sticky"]
              /\ msgs \in [Convs -> {"none", "old", "fresh"}]
              /\ age \in [Convs -> {"fresh", "old"}]
              /\ pending \in [Owners -> BOOLEAN]
              /\ reportsTo \in {"none", "chat", "sticky"}
              /\ continued \in BOOLEAN
              /\ next \in 1..Len(Order)
    NoFreshDeleted == "fresh" \notin violations
    NoPendingDeleted == "pending" \notin violations
    NoShownRunDeleted == "shown" \notin violations
    OwnedKeepTheirRule == "owned" \notin violations

    Expired == /\ alive["run"] /\ ~pending["run"] /\ ~continued
               /\ reportsTo \in {"none", "sticky"}
               /\ age["run"] = "old" /\ msgs["run"] # "fresh"
    StickyExpired == Expired /\ reportsTo = "sticky"
    NoExpiredRunKeptForever == Expired ~> (~alive["run"] \/ ~Expired)
    StickyRunGone == StickyExpired ~> (~alive["run"] \/ ~StickyExpired)

    NeverStandaloneRunDeleted == "standalone run deleted" \notin seen
    NeverStickyRunDeleted == "sticky run deleted" \notin seen
    NeverChatDeletedWithRun == "chat deleted with its run" \notin seen
    NeverPendingRunKept == "expired run kept for its pending work" \notin seen
end define;

fair process retention = "retention"
begin
Visit:
    while TRUE do
        with c = Order[next] do
            if alive[c] then
                if c = "sticky" then
                    msgs := [d \in Convs |-> IF msgs[d] = "old" /\ (d = "sticky" \/ (d = "run" /\ reportsTo = "sticky")) THEN "none" ELSE msgs[d]];
                elsif Stale(c) then
                    violations := violations \cup Harm(Doomed(c));
                    seen := seen \cup Witness(Doomed(c));
                    alive := [d \in Convs |-> alive[d] /\ d \notin Doomed(c)];
                elsif c = "run" /\ Standalone /\ pending["run"] /\ msgs["run"] = "old" then
                    seen := seen \cup {"expired run kept for its pending work"};
                end if;
            end if;
        end with;
        next := (next % Len(Order)) + 1;
    end while;
end process;

process world = "world"
begin
Act:
    while TRUE do
        either
            with c \in {d \in Convs \ {"sub"} : alive[d]} do
                msgs[c] := "fresh";
            end with;
        or
            await alive["sub"];
            msgs := [msgs EXCEPT !["sub"] = "fresh", !["chat"] = "fresh"];
        or
            age := [d \in Convs |-> "old"];
            msgs := [d \in Convs |-> IF msgs[d] = "fresh" THEN "old" ELSE msgs[d]];
        or
            await alive["run"] /\ reportsTo = "none";
            with t \in {d \in {"chat", "sticky"} : alive[d]} do
                reportsTo := t;
            end with;
        or
            await alive["run"] /\ alive["chat"] /\ ~continued /\ reportsTo \in {"none", "chat"};
            continued := TRUE;
            reportsTo := "chat";
        or
            with c \in {d \in Owners : alive[d]} do
                pending[c] := ~pending[c];
            end with;
        end either;
    end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "252b146e" /\ chksum(tla) = "d5d101d9")
VARIABLES alive, msgs, age, pending, reportsTo, continued, next, violations, 
          seen

(* define statement *)
ShowsRun == alive["run"] /\ (reportsTo = "chat" \/ continued)
Shown(c) == IF c = "chat" /\ ShowsRun THEN {"chat", "run"} ELSE {c}
Pending(c) == CASE c = "chat" -> pending["chat"] \/ (reportsTo = "chat" /\ alive["run"] /\ pending["run"])
                [] c = "run" -> pending["run"]
                [] OTHER -> FALSE
LatestBefore(c) == IF \A d \in Shown(c) : msgs[d] = "none" THEN age[c] = "old" ELSE \A d \in Shown(c) : msgs[d] # "fresh"
EmptyManaged(c) == age[c] = "old" /\ \A d \in Shown(c) : msgs[d] = "none"
Standalone == reportsTo = "none" /\ ~continued
DatedByMessages(c) == \/ c = "chat"
                      \/ c = "run" /\ (Shape = "unguarded" \/ (Shape = "fixed" /\ Standalone))
Stale(c) == ~Pending(c) /\ IF DatedByMessages(c) THEN LatestBefore(c) ELSE EmptyManaged(c)
Doomed(c) == CASE c = "chat" -> {d \in {"chat", "sub"} : alive[d]} \cup (IF ShowsRun THEN {"run"} ELSE {})
               [] OTHER -> {c}
Harm(D) == (IF \E d \in D : msgs[d] = "fresh" THEN {"fresh"} ELSE {})
    \cup (IF \E d \in D \cap Owners : pending[d] THEN {"pending"} ELSE {})
    \cup (IF "run" \in D /\ "chat" \notin D /\ ShowsRun /\ msgs["run"] # "none" /\ ~Stale("chat") THEN {"shown"} ELSE {})
    \cup (IF "sub" \in D /\ "chat" \notin D /\ msgs["sub"] # "none" THEN {"owned"} ELSE {})
    \cup (IF "draft" \in D /\ msgs["draft"] # "none" THEN {"owned"} ELSE {})
Witness(D) == (IF D = {"run"} /\ Standalone /\ msgs["run"] = "old" THEN {"standalone run deleted"} ELSE {})
    \cup (IF "run" \in D /\ reportsTo = "sticky" THEN {"sticky run deleted"} ELSE {})
    \cup (IF "chat" \in D /\ "run" \in D THEN {"chat deleted with its run"} ELSE {})

TypeOK == /\ alive \in [Convs -> BOOLEAN] /\ alive["sticky"]
          /\ msgs \in [Convs -> {"none", "old", "fresh"}]
          /\ age \in [Convs -> {"fresh", "old"}]
          /\ pending \in [Owners -> BOOLEAN]
          /\ reportsTo \in {"none", "chat", "sticky"}
          /\ continued \in BOOLEAN
          /\ next \in 1..Len(Order)
NoFreshDeleted == "fresh" \notin violations
NoPendingDeleted == "pending" \notin violations
NoShownRunDeleted == "shown" \notin violations
OwnedKeepTheirRule == "owned" \notin violations

Expired == /\ alive["run"] /\ ~pending["run"] /\ ~continued
           /\ reportsTo \in {"none", "sticky"}
           /\ age["run"] = "old" /\ msgs["run"] # "fresh"
StickyExpired == Expired /\ reportsTo = "sticky"
NoExpiredRunKeptForever == Expired ~> (~alive["run"] \/ ~Expired)
StickyRunGone == StickyExpired ~> (~alive["run"] \/ ~StickyExpired)

NeverStandaloneRunDeleted == "standalone run deleted" \notin seen
NeverStickyRunDeleted == "sticky run deleted" \notin seen
NeverChatDeletedWithRun == "chat deleted with its run" \notin seen
NeverPendingRunKept == "expired run kept for its pending work" \notin seen


vars == << alive, msgs, age, pending, reportsTo, continued, next, violations, 
           seen >>

ProcSet == {"retention"} \cup {"world"}

Init == (* Global variables *)
        /\ alive = [c \in Convs |-> TRUE]
        /\ msgs = [c \in Convs |-> "none"]
        /\ age = [c \in Convs |-> "fresh"]
        /\ pending = [c \in Owners |-> FALSE]
        /\ reportsTo = "none"
        /\ continued = FALSE
        /\ next = 1
        /\ violations = {}
        /\ seen = {}

retention == /\ LET c == Order[next] IN
                  IF alive[c]
                     THEN /\ IF c = "sticky"
                                THEN /\ msgs' = [d \in Convs |-> IF msgs[d] = "old" /\ (d = "sticky" \/ (d = "run" /\ reportsTo = "sticky")) THEN "none" ELSE msgs[d]]
                                     /\ UNCHANGED << alive, violations, seen >>
                                ELSE /\ IF Stale(c)
                                           THEN /\ violations' = (violations \cup Harm(Doomed(c)))
                                                /\ seen' = (seen \cup Witness(Doomed(c)))
                                                /\ alive' = [d \in Convs |-> alive[d] /\ d \notin Doomed(c)]
                                           ELSE /\ IF c = "run" /\ Standalone /\ pending["run"] /\ msgs["run"] = "old"
                                                      THEN /\ seen' = (seen \cup {"expired run kept for its pending work"})
                                                      ELSE /\ TRUE
                                                           /\ seen' = seen
                                                /\ UNCHANGED << alive, 
                                                                violations >>
                                     /\ msgs' = msgs
                     ELSE /\ TRUE
                          /\ UNCHANGED << alive, msgs, violations, seen >>
             /\ next' = (next % Len(Order)) + 1
             /\ UNCHANGED << age, pending, reportsTo, continued >>

world == /\ \/ /\ \E c \in {d \in Convs \ {"sub"} : alive[d]}:
                    msgs' = [msgs EXCEPT ![c] = "fresh"]
               /\ UNCHANGED <<age, pending, reportsTo, continued>>
            \/ /\ alive["sub"]
               /\ msgs' = [msgs EXCEPT !["sub"] = "fresh", !["chat"] = "fresh"]
               /\ UNCHANGED <<age, pending, reportsTo, continued>>
            \/ /\ age' = [d \in Convs |-> "old"]
               /\ msgs' = [d \in Convs |-> IF msgs[d] = "fresh" THEN "old" ELSE msgs[d]]
               /\ UNCHANGED <<pending, reportsTo, continued>>
            \/ /\ alive["run"] /\ reportsTo = "none"
               /\ \E t \in {d \in {"chat", "sticky"} : alive[d]}:
                    reportsTo' = t
               /\ UNCHANGED <<msgs, age, pending, continued>>
            \/ /\ alive["run"] /\ alive["chat"] /\ ~continued /\ reportsTo \in {"none", "chat"}
               /\ continued' = TRUE
               /\ reportsTo' = "chat"
               /\ UNCHANGED <<msgs, age, pending>>
            \/ /\ \E c \in {d \in Owners : alive[d]}:
                    pending' = [pending EXCEPT ![c] = ~pending[c]]
               /\ UNCHANGED <<msgs, age, reportsTo, continued>>
         /\ UNCHANGED << alive, next, violations, seen >>

Next == retention \/ world

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(retention)

\* END TRANSLATION 
==========================================================================================
