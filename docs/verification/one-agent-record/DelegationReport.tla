---- MODULE DelegationReport ----
EXTENDS Delegation, Naturals, Sequences, TLC

Order == <<"sudo-hermetic", "sudo-denial-refused", "unnamed-refused", "self-needs-naming", "slack-own-thread-needs-naming",
           "sudo-acts-on-own-sessions", "receiver-lists-gone", "task-reaches-easy-agents",
           "easy-tasks-listed-agents">>

AgentClass(a) ==
    CASE a.builtin = "main" -> "main"
      [] a.sudo -> "sudo advanced"
      [] OTHER -> a.kind

TargetClass(x) ==
    LET class == IF x.self THEN "self" ELSE AgentClass(x.t)
    IN IF x.op = "start" /\ x.t.sticky # "off" THEN class \o " (sticky)" ELSE class

SessionClass(x) ==
    CASE x.op \in {"start", "clone"} -> "-"
      [] x.ses.ownThread -> "own Slack thread"
      [] ~x.ses.helper -> "top-level"
      [] x.ses.owner = "target" -> "helper in its own chat"
      [] x.ses.owner = "caller" -> "helper in caller's chat"
      [] x.ses.owner = "otherSudo" -> "helper in a sudo agent's chat"
      [] OTHER -> "helper in a non-sudo agent's chat"

Naming(x) ==
    CASE SudoDenies(x) -> "sudo, denies target"
      [] x.c.sudo -> "sudo"
      [] Named(x) -> "named"
      [] OTHER -> "unnamed"

Signature(x) ==
    x.op \o " | " \o AgentClass(x.c) \o " | " \o Naming(x) \o " | " \o TargetClass(x) \o " | " \o SessionClass(x) \o " | "
    \o IF NewAllowed(x) THEN "now allowed" ELSE "now refused"

DiffIn(p) == {x \in Cases(p) : OldAllowed(x) # NewAllowed(x)}
Labels(x) == LET ks == {k \in Categories : InCategory(k, x)} IN IF ks = {} THEN {"UNAGREED"} ELSE ks

Report == UNION {UNION {{<<k, Signature(x)>> : k \in Labels(x)} : x \in DiffIn(p)} : p \in Pairs}
SoleCategories == UNION {UNION {IF Cardinality(Labels(x)) = 1 THEN Labels(x) ELSE {} : x \in DiffIn(p)} : p \in Pairs}
PairCount == Cardinality(Pairs)

ASSUME PrintT(<<"caller-target pairs", PairCount, "difference cases", Cardinality(Report)>>)
ASSUME {Order[i] : i \in 1..Len(Order)} = Categories
ASSUME \A i \in 1..Len(Order) :
    /\ PrintT(<<Order[i], Cardinality({r \in Report : r[1] = Order[i]})>>)
    /\ \A r \in {r \in Report : r[1] = Order[i]} : PrintT(r)
ASSUME \A r \in {r \in Report : r[1] = "UNAGREED"} : PrintT(r)
ASSUME \A r \in Report : r[1] # "UNAGREED"
ASSUME SoleCategories = Categories
====
