---- MODULE Delegation ----
EXTENDS FiniteSets

Stickies == {"off", "user", "global", "slack"}
Receivers == {"absent", "true", "false", "listWith", "listWithout"}
TaskRules == {"none", "allow", "auto", "deny", "wildcard"}
Ops == {"start", "write", "read", "list", "clone"}
SessionOps == {"write", "read", "list"}
ReadOps == {"read", "list"}
ToolNames == {"a", "b"}

Plain ==
    [kind: {"advanced"}, sudo: BOOLEAN, sticky: Stickies, builtin: {"no"}, receivers: Receivers, copies: {FALSE}]
    \cup [kind: {"easy"}, sudo: {FALSE}, sticky: Stickies, builtin: {"no"}, receivers: Receivers, copies: BOOLEAN]
Main == [kind |-> "advanced", sudo |-> TRUE, sticky |-> "global", builtin |-> "main", receivers |-> "absent", copies |-> FALSE]
Agents == Plain \cup {Main}

Owners == {"target", "caller", "otherSudo", "otherPlain"}
TopLevel == [helper |-> FALSE, owner |-> "target", ownThread |-> FALSE]

Sessions(c, self, op) ==
    IF op \in {"start", "clone"} THEN {TopLevel}
    ELSE {TopLevel}
        \cup {[helper |-> TRUE, owner |-> o, ownThread |-> FALSE] : o \in IF self THEN Owners \ {"caller"} ELSE Owners}
        \cup IF self /\ c.sticky = "slack" THEN {[helper |-> FALSE, owner |-> "target", ownThread |-> TRUE]} ELSE {}

RulesFor(c, self) ==
    CASE c.builtin = "main" -> {IF self THEN "deny" ELSE "none"}
      [] c.kind = "advanced" -> TaskRules
      [] OTHER -> {"none"}
EntriesFor(c) == IF c.kind = "easy" /\ c.builtin = "no" THEN BOOLEAN ELSE {FALSE}
ToolsFor(c) ==
    IF c.builtin = "no" THEN BOOLEAN ELSE {TRUE}
OpsFor(self) == IF self THEN Ops ELSE Ops \ {"clone"}
ParentTools(op) == IF op = "clone" THEN SUBSET ToolNames ELSE {ToolNames}
AllTools == [all |-> TRUE, names |-> {}]
Requests(op) == IF op = "clone" THEN {AllTools} \cup {[all |-> FALSE, names |-> n] : n \in SUBSET ToolNames} ELSE {AllTools}

Pairs == UNION {{[c |-> c, t |-> t, self |-> self] : t \in IF self THEN {c} ELSE Agents} : c \in Agents, self \in BOOLEAN}

Cases(p) == UNION {
    {[c |-> p.c, t |-> p.t, self |-> p.self, rule |-> r, entry |-> e, tools |-> g, op |-> op, ses |-> s, parent |-> pt, request |-> q]
        : r \in RulesFor(p.c, p.self), e \in EntriesFor(p.c), g \in ToolsFor(p.c),
          s \in Sessions(p.c, p.self, op), pt \in ParentTools(op), q \in Requests(op)}
    : op \in OpsFor(p.self)}

VARIABLE w

Init == \E p \in Pairs : w \in Cases(p)
Next == UNCHANGED w
Spec == Init /\ [][Next]_w

OwnerSudo(x) ==
    CASE x.ses.owner = "target" -> x.t.sudo
      [] x.ses.owner = "caller" -> x.c.sudo
      [] x.ses.owner = "otherSudo" -> TRUE
      [] OTHER -> FALSE
OwnedByCaller(x) == x.ses.owner = "caller" \/ (x.ses.owner = "target" /\ x.self)
SudoOwned(x) == OwnerSudo(x) /\ ~OwnedByCaller(x)
Named(x) == IF x.c.kind = "easy" THEN x.entry ELSE x.rule \in {"allow", "auto", "wildcard"}
CloneFits(x) == x.request.all \/ x.request.names \subseteq x.parent
CloneTools(x) == IF x.request.all THEN x.parent ELSE x.request.names

TaskGate(x) ==
    CASE x.c.sudo /\ x.rule = "none" -> "allow"
      [] x.rule = "none" -> "deny"
      [] x.rule = "wildcard" -> "allow"
      [] OTHER -> x.rule

OldAccepts(x) ==
    CASE x.t.receivers \in {"true", "listWith"} -> TRUE
      [] x.t.receivers = "absent" -> x.t.kind = "easy" \/ ~x.t.sudo
      [] OTHER -> FALSE

OldAllowed(x) ==
    CASE x.op = "start" -> x.c.kind = "advanced" /\ x.t.kind = "advanced" /\ TaskGate(x) # "deny" /\ (x.t.sticky = "off" \/ x.self)
      [] x.op = "clone" -> x.c.kind = "easy" /\ x.c.copies /\ CloneFits(x)
      [] x.op = "write" -> x.tools /\ ~x.ses.helper /\ (x.ses.ownThread \/ OldAccepts(x))
      [] OTHER -> x.tools

NewGate(x) == IF x.c.kind = "easy" THEN IF x.entry THEN "auto" ELSE "deny" ELSE TaskGate(x)
Hermetic(x) == SudoOwned(x) /\ ~(x.c.sudo /\ x.op \in ReadOps)
SudoDenies(x) == x.c.sudo /\ ~x.self /\ x.rule = "deny"
Reaches(x) == (IF x.c.sudo THEN ~SudoDenies(x) ELSE NewGate(x) # "deny") /\ ~Hermetic(x)

NewRule(x) ==
    CASE x.op = "start" -> NewGate(x) # "deny" /\ (x.t.sticky = "off" \/ x.self) /\ ~Hermetic(x)
      [] x.op = "clone" -> x.c.copies /\ CloneFits(x)
      [] x.op = "write" -> x.tools /\ ~x.ses.helper /\ Reaches(x)
      [] OTHER -> x.tools /\ Reaches(x)

NewAllowed(x) == NewRule(x)

Gained(x) == ~OldAllowed(x) /\ NewAllowed(x)
Lost(x) == OldAllowed(x) /\ ~NewAllowed(x)

Categories == {"task-reaches-easy-agents", "easy-tasks-listed-agents", "sudo-hermetic", "sudo-acts-on-own-sessions",
               "sudo-denial-refused", "unnamed-refused", "self-needs-naming", "slack-own-thread-needs-naming",
               "receiver-lists-gone"}

InCategory(k, x) ==
    CASE k = "task-reaches-easy-agents" -> Gained(x) /\ x.op = "start" /\ x.c.kind = "advanced" /\ x.t.kind = "easy"
      [] k = "easy-tasks-listed-agents" -> Gained(x) /\ x.op = "start" /\ x.c.kind = "easy" /\ x.entry
      [] k = "sudo-hermetic" -> Lost(x) /\ SudoOwned(x) /\ ~(x.c.sudo /\ x.op \in ReadOps)
      [] k = "sudo-acts-on-own-sessions" -> Gained(x) /\ x.c.sudo /\ OwnedByCaller(x)
      [] k = "sudo-denial-refused" -> Lost(x) /\ x.op \in SessionOps /\ SudoDenies(x)
      [] k = "unnamed-refused" -> Lost(x) /\ x.op \in SessionOps /\ ~x.c.sudo /\ ~x.self /\ ~Named(x)
      [] k = "self-needs-naming" -> Lost(x) /\ x.op \in SessionOps /\ ~x.c.sudo /\ x.self /\ ~Named(x) /\ ~(x.op = "write" /\ x.ses.ownThread)
      [] k = "slack-own-thread-needs-naming" -> Lost(x) /\ x.op = "write" /\ x.ses.ownThread /\ ~Named(x)
      [] k = "receiver-lists-gone" -> Gained(x) /\ x.op = "write" /\ ~OldAccepts(x) /\ x.t.builtin = "no"

AgreedChange(x) == \E k \in Categories : InCategory(k, x)
NoAgreedChange(x) == FALSE

SudoHermetic == NewAllowed(w) /\ SudoOwned(w) => w.c.sudo /\ w.op \in ReadOps
NamedOnly == NewAllowed(w) /\ ~w.c.sudo /\ w.op # "clone" => Named(w)
SelfOnlyWhenNamed == NewAllowed(w) /\ w.self /\ ~w.c.sudo /\ w.op # "clone" => Named(w)
StickyNotTasked == NewAllowed(w) /\ w.op = "start" => w.t.sticky = "off" \/ w.self
CloneNeverWidens == NewAllowed(w) /\ w.op = "clone" => CloneTools(w) \subseteq w.parent
SessionToolsNeedGrant == NewAllowed(w) /\ w.op \in SessionOps => w.tools
SudoReadsEverything == w.c.sudo /\ ~SudoDenies(w) /\ w.tools /\ w.op \in ReadOps => NewAllowed(w)
SudoActsOnRest == w.c.sudo /\ ~SudoDenies(w) /\ w.tools /\ w.op \in SessionOps /\ ~SudoOwned(w) /\ ~(w.op = "write" /\ w.ses.helper) => NewAllowed(w)
SudoDenialHolds == NewAllowed(w) => ~SudoDenies(w)
NamedReaches ==
    ~w.c.sudo /\ Named(w) /\ ~SudoOwned(w)
    /\ ((w.op \in SessionOps /\ w.tools /\ ~(w.op = "write" /\ w.ses.helper)) \/ (w.op = "start" /\ (w.t.sticky = "off" \/ w.self)))
    => NewAllowed(w)
ReceiverListsIgnored == \A r \in Receivers : NewAllowed([w EXCEPT !.t.receivers = r]) = NewAllowed(w)
BuiltinOrdinary == NewAllowed([w EXCEPT !.c.builtin = "no", !.t.builtin = "no"]) = NewAllowed(w)
OnlyAgreedDifferences == OldAllowed(w) # NewAllowed(w) => AgreedChange(w)

NewSelfImplicit(x) == NewRule(x) \/ (x.self /\ x.op \in SessionOps /\ x.tools /\ ~(x.op = "write" /\ x.ses.helper))
NewTaskIgnoresSticky(x) == IF x.op = "start" THEN NewGate(x) # "deny" /\ ~Hermetic(x) ELSE NewRule(x)
NewCloneUnchecked(x) == IF x.op = "clone" THEN x.c.copies ELSE NewRule(x)
NewReadsUnnamed(x) == NewRule(x) \/ (x.op \in ReadOps /\ x.tools /\ ~Hermetic(x))
NewSudoReadsClosed(x) == NewRule(x) /\ ~(x.op \in ReadOps /\ SudoOwned(x))
NewKeepsReceivers(x) == NewRule(x) /\ (x.op = "write" => x.ses.ownThread \/ OldAccepts(x))
NewSudoIgnoresDenial(x) == NewRule(x) \/ (SudoDenies(x) /\ x.op \in SessionOps /\ x.tools /\ ~Hermetic(x) /\ ~(x.op = "write" /\ x.ses.helper))
====
