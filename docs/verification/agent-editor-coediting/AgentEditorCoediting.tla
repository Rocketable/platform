------------------------- MODULE AgentEditorCoediting -------------------------
(* Live co-editing of an Agent Editor draft: server-ordered OT on a variant's   *)
(* instructions plus last-write-wins settings, the File tab, and the            *)
(* assistant's accepted suggestions. Characters are unique ids so intent can be *)
(* stated: <<"s", i>> seed text, <<client, n>> typed by a client, <<"x", 1>>    *)
(* written by the assistant. Mechanism constants select the code as built       *)
(* (backend/agent_drafts.go, backend/agent_draft_assistant.go,                  *)
(* web/src/agent-editor/coedit.ts, file.tsx), the earlier code, or the plan as  *)
(* first written; README.md says which check uses which.                        *)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS
  Clients,        \* browser sessions editing the draft
  FileClients,    \* clients that may edit in the File tab (subset of Clients)
  MaxOps,         \* local edits (form or File tab) per client in FileClients
  OtherOps,       \* local edits per other client
  SeedLen,        \* length of the initial body
  Keys,           \* setting keys
  Mode,           \* delete-range vs concurrent insert strictly inside it:
                  \*   "split"      (as built) op = list of splices; the delete splits around the insert
                  \*   "plan"       single splice; the delete range widens over the insert,
                  \*                while an insert inside a deleted range survives (plan's rule)
                  \*   "clip"       single splice; the delete keeps only the part before the insert
                  \*   "deletewins" single splice, made consistent: the insert is dropped
                  \* Single-splice modes are checked with FileClients = {}.
  FileBody,       \* "splices" (as built): File-tab instruction edits are splice ops like the Form's; a
                  \*   frontmatter edit is sent as the whole file with the server's body at its base, so
                  \*   only settings change; it waits in the client's op queue (CoEdit.pending).
                  \* "whole" (plan): the File tab sends the whole text and the server diffs the body.
  FileDiff,       \* whole only: "prefixsuffix" one splice; "minimal" exact diff
  FileEdits,      \* whole only: "contiguous" one range changes; "any" several places
  FileNeedsSync,  \* whole only: TRUE a File submit waits until no form op is in flight or queued
  FileRebase,     \* splices only: what a File submit's settings are diffed against
                  \*   "fromtext" (as built) one submit at a time; diffed against the text the typist
                  \*              started from (fromFile), then against the last acknowledged submit
                  \*   "stale"    (earlier code) the server's settings at the revision of the first
                  \*              keystroke, kept across in-flight submits; several submits may queue
                  \*   "ackrev"   (rejected alternative) one submit at a time; on its ack the local File
                  \*              state is rebased on the client's revision
  SetMode,        \* "pendingwins" (as built): a remote write to a key with a pending local write is
                  \* hidden from the view; "naive": remote writes are always applied
  FormEdits,      \* form edits people make: "body" and/or "set" (a subset only shrinks a run)
  Assistant,      \* TRUE: one assistant suggestion is made on some revision and accepted later
  DiffCap,        \* 0 (as built): the server's body diff is exact with no size limit (linear-space
                  \* Myers); n > 0 (earlier code, n = 2,000 there): past n one-character edits the
                  \* whole changed middle is replaced by one splice
  RejectFile,     \* TRUE: the server may refuse one File-tab submit (as built only on a server error;
                  \* in the earlier code also when a teammate renamed the variant while it was queued)
  OnReject        \* "drophead" (as built, CoEdit.flush): a refused send drops only itself;
                  \* "dropall" (earlier code): every pending edit is dropped

ASSUME Mode \in {"plan", "clip", "deletewins", "split"}
ASSUME FileBody \in {"whole", "splices"}
ASSUME FileDiff \in {"prefixsuffix", "minimal"}
ASSUME FileEdits \in {"contiguous", "any"}
ASSUME FileRebase \in {"stale", "ackrev", "fromtext"}
ASSUME SetMode \in {"pendingwins", "naive"}
ASSUME OnReject \in {"dropall", "drophead"}
ASSUME FileClients \subseteq Clients
ASSUME Keys # {} /\ DiffCap \in Nat /\ FormEdits \subseteq {"body", "set"}

(* --algorithm AgentEditorCoediting {
variables
  stext = [i \in 1..SeedLen |-> <<"s", i>>],   \* server body
  sset = [k \in Keys |-> <<"s", 0>>],           \* server settings
  slog = <<>>,                                  \* op log: committed ops in server order
  shist = <<[t |-> [i \in 1..SeedLen |-> <<"s", i>>], s |-> [k \in Keys |-> <<"s", 0>>]]>>, \* draft at each revision
  setLog = <<>>,                                \* every committed setting write [k, v], in order
  sIns = {}, sDel = {},                         \* ghost: committed inserted ids / intended deletes
  issuedSets = {},                              \* ghost: setting values sent by the form (and whole File)
  invalidHandled = 0,                           \* ghost: invalid File submits processed
  c2s = [c \in Clients |-> <<>>],               \* client -> server FIFO
  s2c = [c \in Clients |-> <<>>],               \* server -> client FIFO
  text = [c \in Clients |-> [i \in 1..SeedLen |-> <<"s", i>>]],
  cset = [c \in Clients |-> [k \in Keys |-> <<"s", 0>>]],
  rev = [c \in Clients |-> 0],
  q = [c \in Clients |-> <<>>],                 \* CoEdit.pending: the head is in flight when waiting
  waiting = [c \in Clients |-> FALSE],
  used = [c \in Clients |-> 0],
  draft = [c \in Clients |-> NoDraft],          \* whole-text File submit awaiting the server (plan)
  loc = [c \in Clients |-> NoLoc],              \* File-tab frontmatter edit not yet in the draft (as built)
  typedIns = {}, typedDel = {},                 \* ghost: every id typed / every id a client deleted
  lastMine = [c \in Clients |-> [k \in Keys |-> <<"none", 0>>]], \* ghost: c's last intended value per key
  othersAfter = [c \in Clients |-> [k \in Keys |-> FALSE]],     \* ghost: someone else wrote k since
  clobbered = {},                               \* ghost: File writes to keys the typist never touched
  rejected = FALSE,                             \* ghost: the one refusal is chosen
  prop = [on |-> FALSE, done |-> FALSE, late |-> FALSE, base |-> 0, t |-> <<>>, p |-> <<>>]; \* late: ghost

define {
  Budget(c) == IF c \in FileClients THEN MaxOps ELSE OtherOps
  RxIds == {"rx_" \o c : c \in Clients}
  ClientOf(r) == CHOOSE c \in Clients : "rx_" \o c = r
  Seed == [i \in 1..SeedLen |-> <<"s", i>>]
  SeedSet == <<"s", 0>>
  NoVal == <<"none", 0>>
  NoSet == [k \in Keys |-> NoVal]
  NoDraft == [on |-> FALSE, base |-> 0, txt |-> <<>>, set |-> NoSet, sv |-> NoVal, valid |-> TRUE,
              replied |-> FALSE, intent |-> {}, ins |-> {}, fid |-> NoVal]
  \* file.tsx Local: base revision, settings shown at the first keystroke, current settings, last
  \* sent settings (NoSet: none yet), from (the text started from, then the last acknowledged
  \* submit's), keys edited, "not yet valid", YAML fixed since; ghost vsent: the last sent settings
  \* the server will take (valid, not refused), the typist's reference for "changed back"
  NoLoc == [on |-> FALSE, base |-> 0, start |-> NoSet, txt |-> NoSet, sent |-> NoSet, from |-> NoSet,
            touched |-> {}, invalid |-> FALSE, fixed |-> FALSE, vsent |-> NoSet]
  Min(x, y) == IF x < y THEN x ELSE y
  Max(x, y) == IF x > y THEN x ELSE y
  Ids(s) == {s[i] : i \in 1..Len(s)}

  \* Primitive edits. An op is a sequence of primitives applied in order;
  \* a splice {at, remove, insert} is <<Del(at, remove), Ins(at, insert)>>.
  Ins(p, I) == [t |-> "ins", at |-> p, n |-> 0, ids |-> I, k |-> "", v |-> NoVal]
  Del(p, r) == [t |-> "del", at |-> p, n |-> r, ids |-> <<>>, k |-> "", v |-> NoVal]
  SetP(key, x) == [t |-> "set", at |-> 0, n |-> 0, ids |-> <<>>, k |-> key, v |-> x]

  ApplyP(s, x) ==
    CASE x.t = "ins" -> SubSeq(s, 1, x.at) \o x.ids \o SubSeq(s, x.at + 1, Len(s))
      [] x.t = "del" -> SubSeq(s, 1, x.at) \o SubSeq(s, x.at + x.n + 1, Len(s))
      [] OTHER -> s
  RECURSIVE ApplyOp(_, _)
  ApplyOp(s, op) == IF op = <<>> THEN s ELSE ApplyOp(ApplyP(s, Head(op)), Tail(op))
  RECURSIVE SetOf(_, _)
  SetOf(m, op) == IF op = <<>> THEN m
                  ELSE SetOf(IF Head(op).t = "set" THEN [m EXCEPT ![Head(op).k] = Head(op).v] ELSE m, Tail(op))
  SetsIn(op) == {op[i].v : i \in {j \in 1..Len(op) : op[j].t = "set"}}

  \* a after b (both on the same text). aFirst: a is earlier in server order.
  \* Same rules as transformAgentDraftSplice (agent_drafts.go) and past (coedit.ts).
  XOne(a, b, aFirst) ==
    CASE a.t = "set" ->
           IF b.t = "set" /\ b.k = a.k /\ aFirst /\ SetMode = "pendingwins" THEN <<>> ELSE <<a>>
      [] b.t = "set" -> <<a>>
      [] a.t = "ins" /\ b.t = "ins" ->
           IF a.at < b.at \/ (a.at = b.at /\ aFirst) THEN <<a>>
           ELSE <<Ins(a.at + Len(b.ids), a.ids)>>
      [] a.t = "ins" /\ b.t = "del" ->
           IF a.at <= b.at THEN <<a>>
           ELSE IF a.at >= b.at + b.n THEN <<Ins(a.at - b.n, a.ids)>>
           ELSE IF Mode = "deletewins" THEN <<>>
           ELSE <<Ins(b.at, a.ids)>>                 \* typed inside a deleted passage: survives there
      [] a.t = "del" /\ b.t = "ins" ->
           IF b.at <= a.at THEN <<Del(a.at + Len(b.ids), a.n)>>
           ELSE IF b.at >= a.at + a.n THEN <<a>>
           ELSE (CASE Mode = "split" ->
                        <<Del(a.at, b.at - a.at), Del(a.at + Len(b.ids), a.n - (b.at - a.at))>>
                   [] Mode = "clip" -> <<Del(a.at, b.at - a.at)>>
                   [] OTHER -> <<Del(a.at, a.n + Len(b.ids))>>)  \* one splice: range widens over the insert
      [] a.t = "del" /\ b.t = "del" ->
           LET ov == Max(0, Min(a.at + a.n, b.at + b.n) - Max(a.at, b.at))
               st == IF a.at <= b.at THEN a.at
                     ELSE IF a.at <= b.at + b.n THEN b.at ELSE a.at - b.n
           IN IF a.n = ov THEN <<>> ELSE <<Del(st, a.n - ov)>>

  \* <<A after B, B after A>> for ops (sequences of primitives): transformAgentDraftSplices.
  RECURSIVE XS(_, _, _)
  XS(A, B, f) ==
    IF A = <<>> THEN <<<<>>, B>>
    ELSE IF B = <<>> THEN <<A, <<>>>>
    ELSE IF Len(A) = 1 /\ Len(B) = 1 THEN <<XOne(A[1], B[1], f), XOne(B[1], A[1], ~f)>>
    ELSE IF Len(A) > 1
      THEN LET r1 == XS(<<A[1]>>, B, f)
               r2 == XS(Tail(A), r1[2], f)
           IN <<r1[1] \o r2[1], r2[2]>>
      ELSE LET r1 == XS(A, <<B[1]>>, f)
               r2 == XS(r1[1], Tail(B), f)
           IN <<r2[1], r1[2] \o r2[2]>>

  \* Server: move an incoming op past every op committed after its base. The step is atomic: the
  \* server only uses ops its loaded draft holds and commits with a revision compare-and-swap.
  RECURSIVE XL(_, _)
  XL(op, i) == IF i > Len(slog) THEN op ELSE XL(XS(op, slog[i], FALSE)[1], i + 1)

  \* Client: move a remote op past the pending queue, in order (CoEdit.rebase). File entries change
  \* no body text and are not in the view, so the op passes them unchanged.
  RECURSIVE Rebase(_, _)
  Rebase(Q, op) ==
    IF Q = <<>> THEN <<<<>>, op>>
    ELSE IF Head(Q).k # "op"
      THEN LET r == Rebase(Tail(Q), op) IN <<<<Head(Q)>> \o r[1], r[2]>>
      ELSE LET x == XS(Head(Q).op, op, FALSE)
               r == Rebase(Tail(Q), x[2])
           IN <<<<[Head(Q) EXCEPT !.op = x[1]]>> \o r[1], r[2]>>

  \* Queue entries and the message that sends one (an op's base is the revision at send time).
  OpE(o, d, i) == [k |-> "op", op |-> o, intent |-> d, ins |-> i, base |-> 0, set |-> NoSet,
                   valid |-> TRUE, rej |-> FALSE, touched |-> {}, from |-> NoSet]
  Msg(e, r) == IF e.k = "op" THEN [e EXCEPT !.base = r] ELSE e

  \* Diffs: common prefix/suffix as one splice, and the exact diff (shortestAgentDraftEdits).
  RECURSIVE Pre(_, _)
  Pre(s, u) == IF s = <<>> \/ u = <<>> \/ Head(s) # Head(u) THEN 0 ELSE 1 + Pre(Tail(s), Tail(u))
  RECURSIVE Suf(_, _, _)
  Suf(s, u, lim) ==
    IF lim = 0 \/ s[Len(s)] # u[Len(u)] THEN 0
    ELSE 1 + Suf(SubSeq(s, 1, Len(s) - 1), SubSeq(u, 1, Len(u) - 1), lim - 1)
  PSDiff(s, u) ==
    LET p == Pre(s, u)
        q0 == Suf(s, u, Min(Len(s), Len(u)) - p)
        r == Len(s) - p - q0
        I == SubSeq(u, p + 1, Len(u) - q0)
    IN (IF r > 0 THEN <<Del(p, r)>> ELSE <<>>) \o (IF I # <<>> THEN <<Ins(p, I)>> ELSE <<>>)
  RECURSIVE Dels(_, _, _)
  Dels(s, u, k) == IF k = 0 THEN <<>>
                   ELSE (IF s[k] \in Ids(u) THEN <<>> ELSE <<Del(k - 1, 1)>>) \o Dels(s, u, k - 1)
  RECURSIVE Inss(_, _, _)
  Inss(s, u, k) == IF k > Len(u) THEN <<>>
                   ELSE (IF u[k] \in Ids(s) THEN <<>> ELSE <<Ins(k - 1, <<u[k]>>)>>) \o Inss(s, u, k + 1)
  Exact(s, u) == Dels(s, u, Len(s)) \o Inss(s, u, 1)
  \* diffAgentDraftBody: exact (as built); with a cap, past it the changed middle whole (earlier code).
  CappedDiff(s, u) == IF DiffCap > 0 /\ Len(Exact(s, u)) > DiffCap THEN PSDiff(s, u) ELSE Exact(s, u)
  \* diffAgentDraft's settings part: a write for every key whose value differs.
  RECURSIVE SetDiff(_, _, _)
  SetDiff(from, to, K) ==
    IF K = {} THEN <<>>
    ELSE LET k == CHOOSE x \in K : TRUE
         IN (IF from[k] # to[k] THEN <<SetP(k, to[k])>> ELSE <<>>) \o SetDiff(from, to, K \ {k})
  FileOp(b, m) ==
    (IF FileDiff = "prefixsuffix" THEN PSDiff(b.t, m.txt) ELSE Exact(b.t, m.txt)) \o SetDiff(b.s, m.set, Keys)

  \* Texts reachable by keeping some characters and adding at most one new one (fid).
  WithFid(s, fid) == {s} \cup {SubSeq(s, 1, i) \o <<fid>> \o SubSeq(s, i + 1, Len(s)) : i \in 0..Len(s)}
  AnyEdit(s, fid) == (UNION {WithFid(SelectSeq(s, LAMBDA x : x \in K), fid) : K \in SUBSET Ids(s)}) \ {s}
  FileTexts(s, fid) ==
    IF FileEdits = "contiguous"
    THEN {SubSeq(s, 1, p) \o I \o SubSeq(s, p + r + 1, Len(s)) :
            p \in 0..Len(s), r \in 0..Len(s), I \in {<<>>, <<fid>>}} \ {s}
    ELSE AnyEdit(s, fid)

  Views == {stext} \cup {text[c] : c \in Clients}
  Settled == \A c \in Clients : c2s[c] = <<>> /\ s2c[c] = <<>> /\ q[c] = <<>>
  Quiescent == Settled /\ \A c \in Clients : ~draft[c].on /\ ~loc[c].on

  \* 1. Convergence: when nothing is pending, every view equals the server's draft.
  Convergence == Quiescent => \A c \in Clients : text[c] = stext /\ cset[c] = sset
  \* 2. No edit is lost: server text = everything inserted minus what an author saw and deleted.
  NoLostEdit == Ids(stext) = (Ids(Seed) \cup sIns) \ sDel
  NoDuplicate == \A v \in Views : Cardinality(Ids(v)) = Len(v)
  \* 3. Surviving text keeps its relative order across every pair of views.
  Idx(s, x) == CHOOSE k \in 1..Len(s) : s[k] = x
  OrderPreserved ==
    \A v, w \in Views : \A i, j \in 1..Len(v) :
      (i < j /\ v[i] \in Ids(w) /\ v[j] \in Ids(w)) => Idx(w, v[i]) < Idx(w, v[j])
  \* 4. An invalid File-tab version stays private to the typist, and commits nothing.
  InvalidPrivate ==
    \A c \in Clients : (draft[c].on /\ ~draft[c].valid) =>
      draft[c].fid \notin Ids(stext) \cup UNION {Ids(text[d]) : d \in Clients \ {c}}
  InvalidNoRevision ==
    [][invalidHandled' # invalidHandled => UNCHANGED <<slog, stext, sset>>]_<<invalidHandled, slog, stext, sset>>
  \* 5. Every committed setting write is in the op log; each key's value is its last write.
  PendingSets ==
    UNION {(UNION {SetsIn(q[c][i].op) : i \in 1..Len(q[c])}) \cup
           (UNION {IF c2s[c][i].k = "op" THEN SetsIn(c2s[c][i].op)
                   ELSE IF c2s[c][i].k = "file" THEN {c2s[c][i].sv} ELSE {} : i \in 1..Len(c2s[c])})
           : c \in Clients}
  HistoryComplete == \A v \in issuedSets : v \in {setLog[i].v : i \in 1..Len(setLog)} \/ v \in PendingSets
  RECURSIVE LastSet(_, _)
  LastSet(k, i) == IF i = 0 THEN SeedSet ELSE IF setLog[i].k = k THEN setLog[i].v ELSE LastSet(k, i - 1)
  SettingsLWW == \A k \in Keys : sset[k] = LastSet(k, Len(setLog))
  \* 6. Nothing a client typed disappears unsent: each typed insert or delete is committed or still
  \*    pending in its client's queue.
  PendIns == UNION {UNION {q[c][i].ins : i \in 1..Len(q[c])} : c \in Clients}
  PendDel == UNION {UNION {q[c][i].intent : i \in 1..Len(q[c])} : c \in Clients}
  NoDroppedEdit == typedIns \subseteq sIns \cup PendIns /\ typedDel \subseteq sDel \cup PendDel
  \* 7. When all is quiet, each key holds the value its typist last chose (a form write, a File edit,
  \*    or sending a File text that shows a value they edited there), unless someone else wrote that
  \*    key after that choice (then last write wins).
  OwnLastWrite ==
    Quiescent => \A c \in Clients, k \in Keys :
      (lastMine[c][k] # NoVal /\ ~othersAfter[c][k]) => sset[k] = lastMine[c][k]
  \* 8. A File submit never changes a key the typist did not edit in the File tab.
  FileKeepsOthers == clobbered = {}
  \* Witness: every committed body op fits the plan's single splice {at, remove, insert}.
  TextPart(op) == SelectSeq(op, LAMBDA x : x.t # "set")
  OneSplice(op) == LET tp == TextPart(op) IN
    Len(tp) <= 1 \/ (Len(tp) = 2 /\ tp[1].t = "del" /\ tp[2].t = "ins" /\ tp[1].at = tp[2].at)
  ShapeSingleSplice == \A i \in 1..Len(slog) : OneSplice(slog[i])
  \* Cheap stand-in for liveness at wide bounds: the queue head has exactly one request or reply in
  \* transit, and so does a pending whole-text submit; delivery is always enabled while a channel is
  \* non-empty, so nothing can get stuck.
  Cnt(s, kinds) == Len(SelectSeq(s, LAMBDA m : m.k \in kinds))
  NoStuck == \A c \in Clients :
    /\ Cnt(c2s[c], {"op", "qfile"}) + Cnt(s2c[c], {"ack", "qfileop", "qfileack", "qinvalid", "qreject"})
         = IF waiting[c] THEN 1 ELSE 0
    /\ Cnt(c2s[c], {"file"}) + Cnt(s2c[c], {"fileop", "invalid"}) = IF draft[c].on /\ ~draft[c].replied THEN 1 ELSE 0
    /\ waiting[c] = (q[c] # <<>>)
  \* Liveness: once people stop typing, every op is acknowledged and every message delivered.
  EventuallySettled == <>[]Settled
  \* Reachability witnesses: each must be violated by the code as built, showing that the passing
  \* checks reach the case a fix handles.
  \* A refused File submit has typed text queued behind it.
  NoRefusalWithTextBehind == \A c \in Clients :
    ~(Cnt(s2c[c], {"qreject"}) > 0 /\ \E j \in 2..Len(q[c]) : q[c][j].ins # {})
  \* A File-tab change back to the value it started from commits a write.
  NoStartValueRewritten == \A i \in 1..Len(setLog) : setLog[i].v # SeedSet
  \* A File submit starts from a view holding the typist's own unacknowledged form write.
  NoFileFromPendingWrite == \A c \in Clients : \A i \in 1..Len(q[c]) :
    q[c][i].k = "qfile" => \A k \in Keys :
      ~(q[c][i].from[k] \in issuedSets /\ q[c][i].from[k] # shist[q[c][i].base + 1].s[k])
  \* A suggestion is accepted after other ops committed on top of its revision.
  NoLateAccept == ~(prop.done /\ prop.late)
}

\* A form edit (CoEdit.editBody / setField): joins the queue's tail unless that is in flight or a
\* File entry; sent at once when nothing is in flight. intent is read from the pre-edit text.
macro LocalOp(o, intent, ins) {
  if (q[self] # <<>> /\ q[self][Len(q[self])].k = "op" /\ ~(waiting[self] /\ Len(q[self]) = 1)) {
    q[self][Len(q[self])] := [k |-> "op", op |-> q[self][Len(q[self])].op \o o,
                              intent |-> q[self][Len(q[self])].intent \cup intent,
                              ins |-> q[self][Len(q[self])].ins \cup ins, base |-> 0, set |-> NoSet,
                              valid |-> TRUE, rej |-> FALSE, touched |-> {}, from |-> NoSet]
  } else {
    if (~waiting[self]) {
      c2s[self] := Append(c2s[self], Msg(OpE(o, intent, ins), rev[self]));
      waiting[self] := TRUE
    };
    q[self] := Append(q[self], OpE(o, intent, ins))
  };
  typedIns := typedIns \cup ins;
  typedDel := typedDel \cup intent;
  used[self] := used[self] + 1;
  cset[self] := SetOf(cset[self], o);
  text[self] := ApplyOp(text[self], o)
}

fair process (srv = "server") {
srvLoop:
  while (TRUE) {
    with (c \in {d \in Clients : c2s[d] # <<>>}; m = Head(c2s[c])) {
      c2s[c] := Tail(c2s[c]);
      if (m.k \in {"file", "qfile"} /\ ~m.valid) {
        \* "not yet valid": only the typist hears; nothing commits.
        s2c[c] := Append(s2c[c], [k |-> IF m.k = "file" THEN "invalid" ELSE "qinvalid"]);
        invalidHandled := invalidHandled + 1
      } else if (m.k = "qfile" /\ m.rej) {
        \* the server refuses the submit (an error reply)
        s2c[c] := Append(s2c[c], [k |-> "qreject"])
      } else {
        with (raw = CASE m.k = "op" -> m.op
                      [] m.k = "file" -> FileOp(shist[m.base + 1], m)
                      [] OTHER -> SetDiff(IF FileRebase = "fromtext" THEN m.from ELSE shist[m.base + 1].s,
                                          m.set, Keys);
              op = XL(raw, m.base + 1);
              nt = ApplyOp(stext, op);
              ns = SetOf(sset, op);
              sp = SelectSeq(op, LAMBDA x : x.t = "set")) {
          if (m.k = "qfile" /\ raw = <<>>) {
            \* ApplyAgentDraftOp commits nothing for a change that changes nothing.
            s2c[c] := Append(s2c[c], [k |-> "qfileack"])
          } else {
            if (m.k = "qfile") {
              clobbered := clobbered \cup
                {[k |-> sp[i].k, v |-> sp[i].v] : i \in {j \in 1..Len(sp) : sp[j].k \notin m.touched /\ sp[j].v # sset[sp[j].k]}}
            };
            othersAfter := [d \in Clients |-> [k \in Keys |->
                             othersAfter[d][k] \/ (d # c /\ \E i \in 1..Len(sp) : sp[i].k = k)]];
            stext := nt;
            sset := ns;
            slog := Append(slog, op);
            shist := Append(shist, [t |-> nt, s |-> ns]);
            setLog := setLog \o [i \in 1..Len(sp) |-> [k |-> sp[i].k, v |-> sp[i].v]];
            sIns := sIns \cup m.ins;
            sDel := sDel \cup m.intent;
            s2c := [d \in Clients |-> Append(s2c[d],
                      CASE d # c -> [k |-> "op", op |-> op]
                        [] m.k = "op" -> [k |-> "ack"]
                        [] m.k = "file" -> [k |-> "fileop", op |-> op]
                        [] OTHER -> [k |-> "qfileop", op |-> op])]
          }
        }
      }
    }
  }
}

\* The assistant suggests a whole new body on the revision it read; an editor accepts it later.
\* Accepting commits diffAgentDraftBody(body at that revision, suggestion), moved past later ops.
process (asst = "assistant") {
asLoop:
  while (TRUE) {
    either {
      await Assistant /\ ~prop.on /\ ~prop.done;
      with (p \in AnyEdit(stext, <<"x", 1>>)) {
        prop := [on |-> TRUE, done |-> FALSE, late |-> FALSE, base |-> Len(slog), t |-> stext, p |-> p]
      }
    } or {
      await prop.on;
      with (op = XL(CappedDiff(prop.t, prop.p), prop.base + 1); nt = ApplyOp(stext, op); late = Len(slog) > prop.base) {
        stext := nt;
        slog := Append(slog, op);
        shist := Append(shist, [t |-> nt, s |-> sset]);
        sIns := sIns \cup (Ids(prop.p) \ Ids(prop.t));
        sDel := sDel \cup (Ids(prop.t) \ Ids(prop.p));
        s2c := [d \in Clients |-> Append(s2c[d], [k |-> "op", op |-> op])];
        prop := [prop EXCEPT !.on = FALSE, !.done = TRUE, !.late = late]
      }
    }
  }
}

\* People may stop typing at any time, so editors are not fair.
process (ed \in Clients) {
edLoop:
  while (TRUE) {
    either {
      \* form: type one character
      await "body" \in FormEdits /\ used[self] < Budget(self) /\ ~draft[self].on;
      with (p \in 0..Len(text[self]); id = <<self, used[self] + 1>>) {
        LocalOp(<<Ins(p, <<id>>)>>, {}, {id})
      }
    } or {
      \* form: delete a selected range
      await "body" \in FormEdits /\ used[self] < Budget(self) /\ ~draft[self].on /\ text[self] # <<>>;
      with (p \in 0..(Len(text[self]) - 1); r \in 1..(Len(text[self]) - p)) {
        LocalOp(<<Del(p, r)>>, Ids(SubSeq(text[self], p + 1, p + r)), {})
      }
    } or {
      \* form: change a setting
      await "set" \in FormEdits /\ used[self] < Budget(self) /\ ~draft[self].on;
      with (k \in Keys; x = <<self, used[self] + 1>>) {
        issuedSets := issuedSets \cup {x};
        lastMine[self][k] := x;
        othersAfter[self][k] := FALSE;
        LocalOp(<<SetP(k, x)>>, {}, {})
      }
    } or {
      \* plan: File tab edits and submits the whole text with its base revision
      await FileBody = "whole" /\ self \in FileClients /\ used[self] < Budget(self) /\ ~draft[self].on /\
            (~FileNeedsSync \/ q[self] = <<>>);
      \* either the body changes (valid or not yet valid) or only one setting changes
      with (fid = <<self, used[self] + 1>>;
            ch \in {[t |-> x, s |-> cset[self], ok |-> b] : x \in FileTexts(text[self], fid), b \in BOOLEAN}
                  \cup {[t |-> text[self], s |-> [cset[self] EXCEPT ![k] = fid], ok |-> TRUE] : k \in Keys};
            sv = IF ch.s # cset[self] THEN fid ELSE NoVal) {
        draft[self] := [on |-> TRUE, base |-> rev[self], txt |-> ch.t, set |-> ch.s, sv |-> sv, valid |-> ch.ok,
                        replied |-> FALSE, intent |-> Ids(text[self]) \ Ids(ch.t),
                        ins |-> Ids(ch.t) \ Ids(text[self]), fid |-> fid];
        c2s[self] := Append(c2s[self], [k |-> "file", base |-> rev[self], txt |-> ch.t, set |-> ch.s, sv |-> sv,
                        valid |-> ch.ok, intent |-> Ids(text[self]) \ Ids(ch.t),
                        ins |-> Ids(ch.t) \ Ids(text[self])]);
        if (ch.ok /\ sv # NoVal) { issuedSets := issuedSets \cup {sv} };
        used[self] := used[self] + 1
      }
    } or {
      \* plan: typist fixes the YAML and resubmits the same text, same base
      await draft[self].on /\ ~draft[self].valid /\ draft[self].replied;
      draft[self].valid := TRUE || draft[self].replied := FALSE;
      c2s[self] := Append(c2s[self], [k |-> "file", base |-> draft[self].base, txt |-> draft[self].txt,
                      set |-> draft[self].set, sv |-> draft[self].sv, valid |-> TRUE,
                      intent |-> draft[self].intent, ins |-> draft[self].ins]);
      if (draft[self].sv # NoVal) { issuedSets := issuedSets \cup {draft[self].sv} }
    } or {
      \* as built: change one setting in the File tab's frontmatter, to a new value or back to the
      \* value shown at the first keystroke (file.tsx change)
      await FileBody = "splices" /\ self \in FileClients /\ used[self] < Budget(self);
      with (l = IF loc[self].on THEN loc[self]
                ELSE [NoLoc EXCEPT !.on = TRUE, !.base = rev[self], !.start = cset[self], !.txt = cset[self],
                                   !.from = cset[self]];
            k \in Keys;
            v \in {<<self, used[self] + 1>>, l.start[k]} \ {l.txt[k]};
            nt = [l.txt EXCEPT ![k] = v];
            ref = IF l.vsent = NoSet THEN l.start ELSE l.vsent) {
        \* back to what the view shows, with nothing sent yet: the edit is dropped
        loc[self] := IF nt = cset[self] /\ l.sent = NoSet THEN NoLoc
                     ELSE [l EXCEPT !.txt = nt, !.touched = l.touched \cup {k}, !.invalid = FALSE, !.fixed = FALSE];
        \* changing back to what was last validly sent (or started from) intends nothing
        lastMine[self][k] := IF v = ref[k] THEN NoVal ELSE v;
        othersAfter[self][k] := FALSE;
        used[self] := used[self] + 1
      }
    } or {
      \* as built: 200 ms after a change, the whole file goes into the queue (CoEdit.submitFile);
      \* with fromtext (as built) only when no earlier File submit is outstanding
      await loc[self].on /\ (loc[self].txt # loc[self].sent \/ loc[self].fixed) /\
            (FileRebase = "stale" \/ \A i \in 1..Len(q[self]) : q[self][i].k # "qfile");
      \* ok: the YAML parses; rj (prophecy): the server will refuse this submit
      with (ok \in IF loc[self].fixed THEN {TRUE} ELSE BOOLEAN;
            rj \in IF ok /\ RejectFile /\ ~rejected THEN BOOLEAN ELSE {FALSE};
            e = [k |-> "qfile", op |-> <<>>, intent |-> {}, ins |-> {}, base |-> loc[self].base,
                 set |-> loc[self].txt, valid |-> ok, rej |-> rj, touched |-> loc[self].touched,
                 from |-> loc[self].from];
            ref = IF loc[self].vsent = NoSet THEN loc[self].start ELSE loc[self].vsent;
            re = {k \in loc[self].touched : loc[self].txt[k] # ref[k]}) {
        \* sending re-chooses what the text shows for the keys edited there since the last send
        lastMine[self] := [k \in Keys |-> IF k \in re THEN loc[self].txt[k] ELSE lastMine[self][k]];
        othersAfter[self] := [k \in Keys |-> IF k \in re THEN FALSE ELSE othersAfter[self][k]];
        loc[self] := [loc[self] EXCEPT !.sent = loc[self].txt, !.fixed = FALSE,
                                       !.vsent = IF ok /\ ~rj THEN loc[self].txt ELSE loc[self].vsent];
        rejected := rejected \/ rj;
        if (~waiting[self]) {
          c2s[self] := Append(c2s[self], e);
          waiting[self] := TRUE
        };
        q[self] := Append(q[self], e)
      }
    } or {
      \* as built: after "not yet valid" (or a refusal) the typist fixes the YAML
      await loc[self].on /\ loc[self].invalid;
      loc[self] := [loc[self] EXCEPT !.invalid = FALSE, !.fixed = TRUE]
    }
  }
}

\* Delivery is fair: every message eventually reaches its client.
fair process (rx \in RxIds) {
rxLoop:
  while (TRUE) {
    with (me = ClientOf(self)) {
      await s2c[me] # <<>>;
      with (m = Head(s2c[me])) {
        s2c[me] := Tail(s2c[me]);
        if (m.k \in {"ack", "qfileop", "qfileack", "qinvalid", "qreject"}) {
          \* the queue head settles: send the next entry (CoEdit.settle / flush)
          with (e = Head(q[me]);
                drop = m.k = "qreject" /\ OnReject = "dropall";
                r = Rebase(IF drop THEN <<>> ELSE Tail(q[me]), IF m.k = "qfileop" THEN m.op ELSE <<>>);
                nr = IF m.k \in {"ack", "qfileop"} THEN rev[me] + 1 ELSE rev[me];
                same = loc[me].on /\ loc[me].txt = e.set /\ ~loc[me].fixed) {
            q[me] := r[1];
            rev[me] := nr;
            if (r[1] # <<>>) {
              c2s[me] := Append(c2s[me], Msg(Head(r[1]), nr))
            } else {
              waiting[me] := FALSE
            };
            \* a dropped queue leaves the view at the server's draft
            text[me] := IF drop THEN shist[nr + 1].t ELSE ApplyOp(text[me], r[2]);
            cset[me] := IF drop THEN shist[nr + 1].s ELSE SetOf(cset[me], r[2]);
            \* file.tsx: a settled submit clears the local File state unless the typist changed it since
            loc[me] := CASE e.k # "qfile" \/ ~loc[me].on -> loc[me]
                         [] m.k = "qinvalid" -> IF same THEN [loc[me] EXCEPT !.invalid = TRUE] ELSE loc[me]
                         [] m.k = "qreject" -> IF same THEN [loc[me] EXCEPT !.invalid = TRUE] ELSE loc[me]
                         [] same -> NoLoc
                         [] FileRebase = "ackrev" -> [loc[me] EXCEPT !.base = nr, !.from = e.set]
                         [] OTHER -> [loc[me] EXCEPT !.from = e.set]
          }
        } else if (m.k = "invalid") {
          draft[me].replied := TRUE
        } else {
          \* remote op (or my own whole-text File op): rebase the queue, apply the transformed op
          with (r = Rebase(q[me], m.op)) {
            rev[me] := rev[me] + 1;
            q[me] := r[1];
            text[me] := ApplyOp(text[me], r[2]);
            cset[me] := SetOf(cset[me], r[2]);
            if (m.k = "fileop") { draft[me] := NoDraft }
          }
        }
      }
    }
  }
}
} *)
\* BEGIN TRANSLATION
VARIABLES stext, sset, slog, shist, setLog, sIns, sDel, issuedSets, 
          invalidHandled, c2s, s2c, text, cset, rev, q, waiting, used, draft, 
          loc, typedIns, typedDel, lastMine, othersAfter, clobbered, rejected, 
          prop

(* define statement *)
Budget(c) == IF c \in FileClients THEN MaxOps ELSE OtherOps
RxIds == {"rx_" \o c : c \in Clients}
ClientOf(r) == CHOOSE c \in Clients : "rx_" \o c = r
Seed == [i \in 1..SeedLen |-> <<"s", i>>]
SeedSet == <<"s", 0>>
NoVal == <<"none", 0>>
NoSet == [k \in Keys |-> NoVal]
NoDraft == [on |-> FALSE, base |-> 0, txt |-> <<>>, set |-> NoSet, sv |-> NoVal, valid |-> TRUE,
            replied |-> FALSE, intent |-> {}, ins |-> {}, fid |-> NoVal]




NoLoc == [on |-> FALSE, base |-> 0, start |-> NoSet, txt |-> NoSet, sent |-> NoSet, from |-> NoSet,
          touched |-> {}, invalid |-> FALSE, fixed |-> FALSE, vsent |-> NoSet]
Min(x, y) == IF x < y THEN x ELSE y
Max(x, y) == IF x > y THEN x ELSE y
Ids(s) == {s[i] : i \in 1..Len(s)}



Ins(p, I) == [t |-> "ins", at |-> p, n |-> 0, ids |-> I, k |-> "", v |-> NoVal]
Del(p, r) == [t |-> "del", at |-> p, n |-> r, ids |-> <<>>, k |-> "", v |-> NoVal]
SetP(key, x) == [t |-> "set", at |-> 0, n |-> 0, ids |-> <<>>, k |-> key, v |-> x]

ApplyP(s, x) ==
  CASE x.t = "ins" -> SubSeq(s, 1, x.at) \o x.ids \o SubSeq(s, x.at + 1, Len(s))
    [] x.t = "del" -> SubSeq(s, 1, x.at) \o SubSeq(s, x.at + x.n + 1, Len(s))
    [] OTHER -> s
RECURSIVE ApplyOp(_, _)
ApplyOp(s, op) == IF op = <<>> THEN s ELSE ApplyOp(ApplyP(s, Head(op)), Tail(op))
RECURSIVE SetOf(_, _)
SetOf(m, op) == IF op = <<>> THEN m
                ELSE SetOf(IF Head(op).t = "set" THEN [m EXCEPT ![Head(op).k] = Head(op).v] ELSE m, Tail(op))
SetsIn(op) == {op[i].v : i \in {j \in 1..Len(op) : op[j].t = "set"}}



XOne(a, b, aFirst) ==
  CASE a.t = "set" ->
         IF b.t = "set" /\ b.k = a.k /\ aFirst /\ SetMode = "pendingwins" THEN <<>> ELSE <<a>>
    [] b.t = "set" -> <<a>>
    [] a.t = "ins" /\ b.t = "ins" ->
         IF a.at < b.at \/ (a.at = b.at /\ aFirst) THEN <<a>>
         ELSE <<Ins(a.at + Len(b.ids), a.ids)>>
    [] a.t = "ins" /\ b.t = "del" ->
         IF a.at <= b.at THEN <<a>>
         ELSE IF a.at >= b.at + b.n THEN <<Ins(a.at - b.n, a.ids)>>
         ELSE IF Mode = "deletewins" THEN <<>>
         ELSE <<Ins(b.at, a.ids)>>
    [] a.t = "del" /\ b.t = "ins" ->
         IF b.at <= a.at THEN <<Del(a.at + Len(b.ids), a.n)>>
         ELSE IF b.at >= a.at + a.n THEN <<a>>
         ELSE (CASE Mode = "split" ->
                      <<Del(a.at, b.at - a.at), Del(a.at + Len(b.ids), a.n - (b.at - a.at))>>
                 [] Mode = "clip" -> <<Del(a.at, b.at - a.at)>>
                 [] OTHER -> <<Del(a.at, a.n + Len(b.ids))>>)
    [] a.t = "del" /\ b.t = "del" ->
         LET ov == Max(0, Min(a.at + a.n, b.at + b.n) - Max(a.at, b.at))
             st == IF a.at <= b.at THEN a.at
                   ELSE IF a.at <= b.at + b.n THEN b.at ELSE a.at - b.n
         IN IF a.n = ov THEN <<>> ELSE <<Del(st, a.n - ov)>>


RECURSIVE XS(_, _, _)
XS(A, B, f) ==
  IF A = <<>> THEN <<<<>>, B>>
  ELSE IF B = <<>> THEN <<A, <<>>>>
  ELSE IF Len(A) = 1 /\ Len(B) = 1 THEN <<XOne(A[1], B[1], f), XOne(B[1], A[1], ~f)>>
  ELSE IF Len(A) > 1
    THEN LET r1 == XS(<<A[1]>>, B, f)
             r2 == XS(Tail(A), r1[2], f)
         IN <<r1[1] \o r2[1], r2[2]>>
    ELSE LET r1 == XS(A, <<B[1]>>, f)
             r2 == XS(r1[1], Tail(B), f)
         IN <<r2[1], r1[2] \o r2[2]>>



RECURSIVE XL(_, _)
XL(op, i) == IF i > Len(slog) THEN op ELSE XL(XS(op, slog[i], FALSE)[1], i + 1)



RECURSIVE Rebase(_, _)
Rebase(Q, op) ==
  IF Q = <<>> THEN <<<<>>, op>>
  ELSE IF Head(Q).k # "op"
    THEN LET r == Rebase(Tail(Q), op) IN <<<<Head(Q)>> \o r[1], r[2]>>
    ELSE LET x == XS(Head(Q).op, op, FALSE)
             r == Rebase(Tail(Q), x[2])
         IN <<<<[Head(Q) EXCEPT !.op = x[1]]>> \o r[1], r[2]>>


OpE(o, d, i) == [k |-> "op", op |-> o, intent |-> d, ins |-> i, base |-> 0, set |-> NoSet,
                 valid |-> TRUE, rej |-> FALSE, touched |-> {}, from |-> NoSet]
Msg(e, r) == IF e.k = "op" THEN [e EXCEPT !.base = r] ELSE e


RECURSIVE Pre(_, _)
Pre(s, u) == IF s = <<>> \/ u = <<>> \/ Head(s) # Head(u) THEN 0 ELSE 1 + Pre(Tail(s), Tail(u))
RECURSIVE Suf(_, _, _)
Suf(s, u, lim) ==
  IF lim = 0 \/ s[Len(s)] # u[Len(u)] THEN 0
  ELSE 1 + Suf(SubSeq(s, 1, Len(s) - 1), SubSeq(u, 1, Len(u) - 1), lim - 1)
PSDiff(s, u) ==
  LET p == Pre(s, u)
      q0 == Suf(s, u, Min(Len(s), Len(u)) - p)
      r == Len(s) - p - q0
      I == SubSeq(u, p + 1, Len(u) - q0)
  IN (IF r > 0 THEN <<Del(p, r)>> ELSE <<>>) \o (IF I # <<>> THEN <<Ins(p, I)>> ELSE <<>>)
RECURSIVE Dels(_, _, _)
Dels(s, u, k) == IF k = 0 THEN <<>>
                 ELSE (IF s[k] \in Ids(u) THEN <<>> ELSE <<Del(k - 1, 1)>>) \o Dels(s, u, k - 1)
RECURSIVE Inss(_, _, _)
Inss(s, u, k) == IF k > Len(u) THEN <<>>
                 ELSE (IF u[k] \in Ids(s) THEN <<>> ELSE <<Ins(k - 1, <<u[k]>>)>>) \o Inss(s, u, k + 1)
Exact(s, u) == Dels(s, u, Len(s)) \o Inss(s, u, 1)

CappedDiff(s, u) == IF DiffCap > 0 /\ Len(Exact(s, u)) > DiffCap THEN PSDiff(s, u) ELSE Exact(s, u)

RECURSIVE SetDiff(_, _, _)
SetDiff(from, to, K) ==
  IF K = {} THEN <<>>
  ELSE LET k == CHOOSE x \in K : TRUE
       IN (IF from[k] # to[k] THEN <<SetP(k, to[k])>> ELSE <<>>) \o SetDiff(from, to, K \ {k})
FileOp(b, m) ==
  (IF FileDiff = "prefixsuffix" THEN PSDiff(b.t, m.txt) ELSE Exact(b.t, m.txt)) \o SetDiff(b.s, m.set, Keys)


WithFid(s, fid) == {s} \cup {SubSeq(s, 1, i) \o <<fid>> \o SubSeq(s, i + 1, Len(s)) : i \in 0..Len(s)}
AnyEdit(s, fid) == (UNION {WithFid(SelectSeq(s, LAMBDA x : x \in K), fid) : K \in SUBSET Ids(s)}) \ {s}
FileTexts(s, fid) ==
  IF FileEdits = "contiguous"
  THEN {SubSeq(s, 1, p) \o I \o SubSeq(s, p + r + 1, Len(s)) :
          p \in 0..Len(s), r \in 0..Len(s), I \in {<<>>, <<fid>>}} \ {s}
  ELSE AnyEdit(s, fid)

Views == {stext} \cup {text[c] : c \in Clients}
Settled == \A c \in Clients : c2s[c] = <<>> /\ s2c[c] = <<>> /\ q[c] = <<>>
Quiescent == Settled /\ \A c \in Clients : ~draft[c].on /\ ~loc[c].on


Convergence == Quiescent => \A c \in Clients : text[c] = stext /\ cset[c] = sset

NoLostEdit == Ids(stext) = (Ids(Seed) \cup sIns) \ sDel
NoDuplicate == \A v \in Views : Cardinality(Ids(v)) = Len(v)

Idx(s, x) == CHOOSE k \in 1..Len(s) : s[k] = x
OrderPreserved ==
  \A v, w \in Views : \A i, j \in 1..Len(v) :
    (i < j /\ v[i] \in Ids(w) /\ v[j] \in Ids(w)) => Idx(w, v[i]) < Idx(w, v[j])

InvalidPrivate ==
  \A c \in Clients : (draft[c].on /\ ~draft[c].valid) =>
    draft[c].fid \notin Ids(stext) \cup UNION {Ids(text[d]) : d \in Clients \ {c}}
InvalidNoRevision ==
  [][invalidHandled' # invalidHandled => UNCHANGED <<slog, stext, sset>>]_<<invalidHandled, slog, stext, sset>>

PendingSets ==
  UNION {(UNION {SetsIn(q[c][i].op) : i \in 1..Len(q[c])}) \cup
         (UNION {IF c2s[c][i].k = "op" THEN SetsIn(c2s[c][i].op)
                 ELSE IF c2s[c][i].k = "file" THEN {c2s[c][i].sv} ELSE {} : i \in 1..Len(c2s[c])})
         : c \in Clients}
HistoryComplete == \A v \in issuedSets : v \in {setLog[i].v : i \in 1..Len(setLog)} \/ v \in PendingSets
RECURSIVE LastSet(_, _)
LastSet(k, i) == IF i = 0 THEN SeedSet ELSE IF setLog[i].k = k THEN setLog[i].v ELSE LastSet(k, i - 1)
SettingsLWW == \A k \in Keys : sset[k] = LastSet(k, Len(setLog))


PendIns == UNION {UNION {q[c][i].ins : i \in 1..Len(q[c])} : c \in Clients}
PendDel == UNION {UNION {q[c][i].intent : i \in 1..Len(q[c])} : c \in Clients}
NoDroppedEdit == typedIns \subseteq sIns \cup PendIns /\ typedDel \subseteq sDel \cup PendDel



OwnLastWrite ==
  Quiescent => \A c \in Clients, k \in Keys :
    (lastMine[c][k] # NoVal /\ ~othersAfter[c][k]) => sset[k] = lastMine[c][k]

FileKeepsOthers == clobbered = {}

TextPart(op) == SelectSeq(op, LAMBDA x : x.t # "set")
OneSplice(op) == LET tp == TextPart(op) IN
  Len(tp) <= 1 \/ (Len(tp) = 2 /\ tp[1].t = "del" /\ tp[2].t = "ins" /\ tp[1].at = tp[2].at)
ShapeSingleSplice == \A i \in 1..Len(slog) : OneSplice(slog[i])



Cnt(s, kinds) == Len(SelectSeq(s, LAMBDA m : m.k \in kinds))
NoStuck == \A c \in Clients :
  /\ Cnt(c2s[c], {"op", "qfile"}) + Cnt(s2c[c], {"ack", "qfileop", "qfileack", "qinvalid", "qreject"})
       = IF waiting[c] THEN 1 ELSE 0
  /\ Cnt(c2s[c], {"file"}) + Cnt(s2c[c], {"fileop", "invalid"}) = IF draft[c].on /\ ~draft[c].replied THEN 1 ELSE 0
  /\ waiting[c] = (q[c] # <<>>)

EventuallySettled == <>[]Settled



NoRefusalWithTextBehind == \A c \in Clients :
  ~(Cnt(s2c[c], {"qreject"}) > 0 /\ \E j \in 2..Len(q[c]) : q[c][j].ins # {})

NoStartValueRewritten == \A i \in 1..Len(setLog) : setLog[i].v # SeedSet

NoFileFromPendingWrite == \A c \in Clients : \A i \in 1..Len(q[c]) :
  q[c][i].k = "qfile" => \A k \in Keys :
    ~(q[c][i].from[k] \in issuedSets /\ q[c][i].from[k] # shist[q[c][i].base + 1].s[k])

NoLateAccept == ~(prop.done /\ prop.late)


vars == << stext, sset, slog, shist, setLog, sIns, sDel, issuedSets, 
           invalidHandled, c2s, s2c, text, cset, rev, q, waiting, used, draft, 
           loc, typedIns, typedDel, lastMine, othersAfter, clobbered, 
           rejected, prop >>

ProcSet == {"server"} \cup {"assistant"} \cup (Clients) \cup (RxIds)

Init == (* Global variables *)
        /\ stext = [i \in 1..SeedLen |-> <<"s", i>>]
        /\ sset = [k \in Keys |-> <<"s", 0>>]
        /\ slog = <<>>
        /\ shist = <<[t |-> [i \in 1..SeedLen |-> <<"s", i>>], s |-> [k \in Keys |-> <<"s", 0>>]]>>
        /\ setLog = <<>>
        /\ sIns = {}
        /\ sDel = {}
        /\ issuedSets = {}
        /\ invalidHandled = 0
        /\ c2s = [c \in Clients |-> <<>>]
        /\ s2c = [c \in Clients |-> <<>>]
        /\ text = [c \in Clients |-> [i \in 1..SeedLen |-> <<"s", i>>]]
        /\ cset = [c \in Clients |-> [k \in Keys |-> <<"s", 0>>]]
        /\ rev = [c \in Clients |-> 0]
        /\ q = [c \in Clients |-> <<>>]
        /\ waiting = [c \in Clients |-> FALSE]
        /\ used = [c \in Clients |-> 0]
        /\ draft = [c \in Clients |-> NoDraft]
        /\ loc = [c \in Clients |-> NoLoc]
        /\ typedIns = {}
        /\ typedDel = {}
        /\ lastMine = [c \in Clients |-> [k \in Keys |-> <<"none", 0>>]]
        /\ othersAfter = [c \in Clients |-> [k \in Keys |-> FALSE]]
        /\ clobbered = {}
        /\ rejected = FALSE
        /\ prop = [on |-> FALSE, done |-> FALSE, late |-> FALSE, base |-> 0, t |-> <<>>, p |-> <<>>]

srv == /\ \E c \in {d \in Clients : c2s[d] # <<>>}:
            LET m == Head(c2s[c]) IN
              /\ c2s' = [c2s EXCEPT ![c] = Tail(c2s[c])]
              /\ IF m.k \in {"file", "qfile"} /\ ~m.valid
                    THEN /\ s2c' = [s2c EXCEPT ![c] = Append(s2c[c], [k |-> IF m.k = "file" THEN "invalid" ELSE "qinvalid"])]
                         /\ invalidHandled' = invalidHandled + 1
                         /\ UNCHANGED << stext, sset, slog, shist, setLog, 
                                         sIns, sDel, othersAfter, clobbered >>
                    ELSE /\ IF m.k = "qfile" /\ m.rej
                               THEN /\ s2c' = [s2c EXCEPT ![c] = Append(s2c[c], [k |-> "qreject"])]
                                    /\ UNCHANGED << stext, sset, slog, shist, 
                                                    setLog, sIns, sDel, 
                                                    othersAfter, clobbered >>
                               ELSE /\ LET raw == CASE m.k = "op" -> m.op
                                                    [] m.k = "file" -> FileOp(shist[m.base + 1], m)
                                                    [] OTHER -> SetDiff(IF FileRebase = "fromtext" THEN m.from ELSE shist[m.base + 1].s,
                                                                        m.set, Keys) IN
                                         LET op == XL(raw, m.base + 1) IN
                                           LET nt == ApplyOp(stext, op) IN
                                             LET ns == SetOf(sset, op) IN
                                               LET sp == SelectSeq(op, LAMBDA x : x.t = "set") IN
                                                 IF m.k = "qfile" /\ raw = <<>>
                                                    THEN /\ s2c' = [s2c EXCEPT ![c] = Append(s2c[c], [k |-> "qfileack"])]
                                                         /\ UNCHANGED << stext, 
                                                                         sset, 
                                                                         slog, 
                                                                         shist, 
                                                                         setLog, 
                                                                         sIns, 
                                                                         sDel, 
                                                                         othersAfter, 
                                                                         clobbered >>
                                                    ELSE /\ IF m.k = "qfile"
                                                               THEN /\ clobbered' = (           clobbered \cup
                                                                                     {[k |-> sp[i].k, v |-> sp[i].v] : i \in {j \in 1..Len(sp) : sp[j].k \notin m.touched /\ sp[j].v # sset[sp[j].k]}})
                                                               ELSE /\ TRUE
                                                                    /\ UNCHANGED clobbered
                                                         /\ othersAfter' = [d \in Clients |-> [k \in Keys |->
                                                                             othersAfter[d][k] \/ (d # c /\ \E i \in 1..Len(sp) : sp[i].k = k)]]
                                                         /\ stext' = nt
                                                         /\ sset' = ns
                                                         /\ slog' = Append(slog, op)
                                                         /\ shist' = Append(shist, [t |-> nt, s |-> ns])
                                                         /\ setLog' = setLog \o [i \in 1..Len(sp) |-> [k |-> sp[i].k, v |-> sp[i].v]]
                                                         /\ sIns' = (sIns \cup m.ins)
                                                         /\ sDel' = (sDel \cup m.intent)
                                                         /\ s2c' = [d \in Clients |-> Append(s2c[d],
                                                                      CASE d # c -> [k |-> "op", op |-> op]
                                                                        [] m.k = "op" -> [k |-> "ack"]
                                                                        [] m.k = "file" -> [k |-> "fileop", op |-> op]
                                                                        [] OTHER -> [k |-> "qfileop", op |-> op])]
                         /\ UNCHANGED invalidHandled
       /\ UNCHANGED << issuedSets, text, cset, rev, q, waiting, used, draft, 
                       loc, typedIns, typedDel, lastMine, rejected, prop >>

asst == /\ \/ /\ Assistant /\ ~prop.on /\ ~prop.done
              /\ \E p \in AnyEdit(stext, <<"x", 1>>):
                   prop' = [on |-> TRUE, done |-> FALSE, late |-> FALSE, base |-> Len(slog), t |-> stext, p |-> p]
              /\ UNCHANGED <<stext, slog, shist, sIns, sDel, s2c>>
           \/ /\ prop.on
              /\ LET op == XL(CappedDiff(prop.t, prop.p), prop.base + 1) IN
                   LET nt == ApplyOp(stext, op) IN
                     LET late == Len(slog) > prop.base IN
                       /\ stext' = nt
                       /\ slog' = Append(slog, op)
                       /\ shist' = Append(shist, [t |-> nt, s |-> sset])
                       /\ sIns' = (sIns \cup (Ids(prop.p) \ Ids(prop.t)))
                       /\ sDel' = (sDel \cup (Ids(prop.t) \ Ids(prop.p)))
                       /\ s2c' = [d \in Clients |-> Append(s2c[d], [k |-> "op", op |-> op])]
                       /\ prop' = [prop EXCEPT !.on = FALSE, !.done = TRUE, !.late = late]
        /\ UNCHANGED << sset, setLog, issuedSets, invalidHandled, c2s, text, 
                        cset, rev, q, waiting, used, draft, loc, typedIns, 
                        typedDel, lastMine, othersAfter, clobbered, rejected >>

ed(self) == /\ \/ /\ "body" \in FormEdits /\ used[self] < Budget(self) /\ ~draft[self].on
                  /\ \E p \in 0..Len(text[self]):
                       LET id == <<self, used[self] + 1>> IN
                         /\ IF q[self] # <<>> /\ q[self][Len(q[self])].k = "op" /\ ~(waiting[self] /\ Len(q[self]) = 1)
                               THEN /\ q' = [q EXCEPT ![self][Len(q[self])] = [k |-> "op", op |-> q[self][Len(q[self])].op \o (<<Ins(p, <<id>>)>>),
                                                                               intent |-> q[self][Len(q[self])].intent \cup ({}),
                                                                               ins |-> q[self][Len(q[self])].ins \cup ({id}), base |-> 0, set |-> NoSet,
                                                                               valid |-> TRUE, rej |-> FALSE, touched |-> {}, from |-> NoSet]]
                                    /\ UNCHANGED << c2s, waiting >>
                               ELSE /\ IF ~waiting[self]
                                          THEN /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], Msg(OpE((<<Ins(p, <<id>>)>>), ({}), ({id})), rev[self]))]
                                               /\ waiting' = [waiting EXCEPT ![self] = TRUE]
                                          ELSE /\ TRUE
                                               /\ UNCHANGED << c2s, waiting >>
                                    /\ q' = [q EXCEPT ![self] = Append(q[self], OpE((<<Ins(p, <<id>>)>>), ({}), ({id})))]
                         /\ typedIns' = (typedIns \cup ({id}))
                         /\ typedDel' = (typedDel \cup ({}))
                         /\ used' = [used EXCEPT ![self] = used[self] + 1]
                         /\ cset' = [cset EXCEPT ![self] = SetOf(cset[self], (<<Ins(p, <<id>>)>>))]
                         /\ text' = [text EXCEPT ![self] = ApplyOp(text[self], (<<Ins(p, <<id>>)>>))]
                  /\ UNCHANGED <<issuedSets, draft, loc, lastMine, othersAfter, rejected>>
               \/ /\ "body" \in FormEdits /\ used[self] < Budget(self) /\ ~draft[self].on /\ text[self] # <<>>
                  /\ \E p \in 0..(Len(text[self]) - 1):
                       \E r \in 1..(Len(text[self]) - p):
                         /\ IF q[self] # <<>> /\ q[self][Len(q[self])].k = "op" /\ ~(waiting[self] /\ Len(q[self]) = 1)
                               THEN /\ q' = [q EXCEPT ![self][Len(q[self])] = [k |-> "op", op |-> q[self][Len(q[self])].op \o (<<Del(p, r)>>),
                                                                               intent |-> q[self][Len(q[self])].intent \cup (Ids(SubSeq(text[self], p + 1, p + r))),
                                                                               ins |-> q[self][Len(q[self])].ins \cup ({}), base |-> 0, set |-> NoSet,
                                                                               valid |-> TRUE, rej |-> FALSE, touched |-> {}, from |-> NoSet]]
                                    /\ UNCHANGED << c2s, waiting >>
                               ELSE /\ IF ~waiting[self]
                                          THEN /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], Msg(OpE((<<Del(p, r)>>), (Ids(SubSeq(text[self], p + 1, p + r))), ({})), rev[self]))]
                                               /\ waiting' = [waiting EXCEPT ![self] = TRUE]
                                          ELSE /\ TRUE
                                               /\ UNCHANGED << c2s, waiting >>
                                    /\ q' = [q EXCEPT ![self] = Append(q[self], OpE((<<Del(p, r)>>), (Ids(SubSeq(text[self], p + 1, p + r))), ({})))]
                         /\ typedIns' = (typedIns \cup ({}))
                         /\ typedDel' = (typedDel \cup (Ids(SubSeq(text[self], p + 1, p + r))))
                         /\ used' = [used EXCEPT ![self] = used[self] + 1]
                         /\ cset' = [cset EXCEPT ![self] = SetOf(cset[self], (<<Del(p, r)>>))]
                         /\ text' = [text EXCEPT ![self] = ApplyOp(text[self], (<<Del(p, r)>>))]
                  /\ UNCHANGED <<issuedSets, draft, loc, lastMine, othersAfter, rejected>>
               \/ /\ "set" \in FormEdits /\ used[self] < Budget(self) /\ ~draft[self].on
                  /\ \E k \in Keys:
                       LET x == <<self, used[self] + 1>> IN
                         /\ issuedSets' = (issuedSets \cup {x})
                         /\ lastMine' = [lastMine EXCEPT ![self][k] = x]
                         /\ othersAfter' = [othersAfter EXCEPT ![self][k] = FALSE]
                         /\ IF q[self] # <<>> /\ q[self][Len(q[self])].k = "op" /\ ~(waiting[self] /\ Len(q[self]) = 1)
                               THEN /\ q' = [q EXCEPT ![self][Len(q[self])] = [k |-> "op", op |-> q[self][Len(q[self])].op \o (<<SetP(k, x)>>),
                                                                               intent |-> q[self][Len(q[self])].intent \cup ({}),
                                                                               ins |-> q[self][Len(q[self])].ins \cup ({}), base |-> 0, set |-> NoSet,
                                                                               valid |-> TRUE, rej |-> FALSE, touched |-> {}, from |-> NoSet]]
                                    /\ UNCHANGED << c2s, waiting >>
                               ELSE /\ IF ~waiting[self]
                                          THEN /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], Msg(OpE((<<SetP(k, x)>>), ({}), ({})), rev[self]))]
                                               /\ waiting' = [waiting EXCEPT ![self] = TRUE]
                                          ELSE /\ TRUE
                                               /\ UNCHANGED << c2s, waiting >>
                                    /\ q' = [q EXCEPT ![self] = Append(q[self], OpE((<<SetP(k, x)>>), ({}), ({})))]
                         /\ typedIns' = (typedIns \cup ({}))
                         /\ typedDel' = (typedDel \cup ({}))
                         /\ used' = [used EXCEPT ![self] = used[self] + 1]
                         /\ cset' = [cset EXCEPT ![self] = SetOf(cset[self], (<<SetP(k, x)>>))]
                         /\ text' = [text EXCEPT ![self] = ApplyOp(text[self], (<<SetP(k, x)>>))]
                  /\ UNCHANGED <<draft, loc, rejected>>
               \/ /\ FileBody = "whole" /\ self \in FileClients /\ used[self] < Budget(self) /\ ~draft[self].on /\
                     (~FileNeedsSync \/ q[self] = <<>>)
                  /\ LET fid == <<self, used[self] + 1>> IN
                       \E ch \in  {[t |-> x, s |-> cset[self], ok |-> b] : x \in FileTexts(text[self], fid), b \in BOOLEAN}
                                 \cup {[t |-> text[self], s |-> [cset[self] EXCEPT ![k] = fid], ok |-> TRUE] : k \in Keys}:
                         LET sv == IF ch.s # cset[self] THEN fid ELSE NoVal IN
                           /\ draft' = [draft EXCEPT ![self] = [on |-> TRUE, base |-> rev[self], txt |-> ch.t, set |-> ch.s, sv |-> sv, valid |-> ch.ok,
                                                                replied |-> FALSE, intent |-> Ids(text[self]) \ Ids(ch.t),
                                                                ins |-> Ids(ch.t) \ Ids(text[self]), fid |-> fid]]
                           /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], [k |-> "file", base |-> rev[self], txt |-> ch.t, set |-> ch.s, sv |-> sv,
                                                              valid |-> ch.ok, intent |-> Ids(text[self]) \ Ids(ch.t),
                                                              ins |-> Ids(ch.t) \ Ids(text[self])])]
                           /\ IF ch.ok /\ sv # NoVal
                                 THEN /\ issuedSets' = (issuedSets \cup {sv})
                                 ELSE /\ TRUE
                                      /\ UNCHANGED issuedSets
                           /\ used' = [used EXCEPT ![self] = used[self] + 1]
                  /\ UNCHANGED <<text, cset, q, waiting, loc, typedIns, typedDel, lastMine, othersAfter, rejected>>
               \/ /\ draft[self].on /\ ~draft[self].valid /\ draft[self].replied
                  /\ draft' = [draft EXCEPT ![self].valid = TRUE,
                                            ![self].replied = FALSE]
                  /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], [k |-> "file", base |-> draft'[self].base, txt |-> draft'[self].txt,
                                                     set |-> draft'[self].set, sv |-> draft'[self].sv, valid |-> TRUE,
                                                     intent |-> draft'[self].intent, ins |-> draft'[self].ins])]
                  /\ IF draft'[self].sv # NoVal
                        THEN /\ issuedSets' = (issuedSets \cup {draft'[self].sv})
                        ELSE /\ TRUE
                             /\ UNCHANGED issuedSets
                  /\ UNCHANGED <<text, cset, q, waiting, used, loc, typedIns, typedDel, lastMine, othersAfter, rejected>>
               \/ /\ FileBody = "splices" /\ self \in FileClients /\ used[self] < Budget(self)
                  /\ LET l == IF loc[self].on THEN loc[self]
                              ELSE [NoLoc EXCEPT !.on = TRUE, !.base = rev[self], !.start = cset[self], !.txt = cset[self],
                                                 !.from = cset[self]] IN
                       \E k \in Keys:
                         \E v \in {<<self, used[self] + 1>>, l.start[k]} \ {l.txt[k]}:
                           LET nt == [l.txt EXCEPT ![k] = v] IN
                             LET ref == IF l.vsent = NoSet THEN l.start ELSE l.vsent IN
                               /\ loc' = [loc EXCEPT ![self] = IF nt = cset[self] /\ l.sent = NoSet THEN NoLoc
                                                               ELSE [l EXCEPT !.txt = nt, !.touched = l.touched \cup {k}, !.invalid = FALSE, !.fixed = FALSE]]
                               /\ lastMine' = [lastMine EXCEPT ![self][k] = IF v = ref[k] THEN NoVal ELSE v]
                               /\ othersAfter' = [othersAfter EXCEPT ![self][k] = FALSE]
                               /\ used' = [used EXCEPT ![self] = used[self] + 1]
                  /\ UNCHANGED <<issuedSets, c2s, text, cset, q, waiting, draft, typedIns, typedDel, rejected>>
               \/ /\ loc[self].on /\ (loc[self].txt # loc[self].sent \/ loc[self].fixed) /\
                     (FileRebase = "stale" \/ \A i \in 1..Len(q[self]) : q[self][i].k # "qfile")
                  /\ \E ok \in IF loc[self].fixed THEN {TRUE} ELSE BOOLEAN:
                       \E rj \in IF ok /\ RejectFile /\ ~rejected THEN BOOLEAN ELSE {FALSE}:
                         LET e == [k |-> "qfile", op |-> <<>>, intent |-> {}, ins |-> {}, base |-> loc[self].base,
                                   set |-> loc[self].txt, valid |-> ok, rej |-> rj, touched |-> loc[self].touched,
                                   from |-> loc[self].from] IN
                           LET ref == IF loc[self].vsent = NoSet THEN loc[self].start ELSE loc[self].vsent IN
                             LET re == {k \in loc[self].touched : loc[self].txt[k] # ref[k]} IN
                               /\ lastMine' = [lastMine EXCEPT ![self] = [k \in Keys |-> IF k \in re THEN loc[self].txt[k] ELSE lastMine[self][k]]]
                               /\ othersAfter' = [othersAfter EXCEPT ![self] = [k \in Keys |-> IF k \in re THEN FALSE ELSE othersAfter[self][k]]]
                               /\ loc' = [loc EXCEPT ![self] = [loc[self] EXCEPT !.sent = loc[self].txt, !.fixed = FALSE,
                                                                                 !.vsent = IF ok /\ ~rj THEN loc[self].txt ELSE loc[self].vsent]]
                               /\ rejected' = (rejected \/ rj)
                               /\ IF ~waiting[self]
                                     THEN /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], e)]
                                          /\ waiting' = [waiting EXCEPT ![self] = TRUE]
                                     ELSE /\ TRUE
                                          /\ UNCHANGED << c2s, waiting >>
                               /\ q' = [q EXCEPT ![self] = Append(q[self], e)]
                  /\ UNCHANGED <<issuedSets, text, cset, used, draft, typedIns, typedDel>>
               \/ /\ loc[self].on /\ loc[self].invalid
                  /\ loc' = [loc EXCEPT ![self] = [loc[self] EXCEPT !.invalid = FALSE, !.fixed = TRUE]]
                  /\ UNCHANGED <<issuedSets, c2s, text, cset, q, waiting, used, draft, typedIns, typedDel, lastMine, othersAfter, rejected>>
            /\ UNCHANGED << stext, sset, slog, shist, setLog, sIns, sDel, 
                            invalidHandled, s2c, rev, clobbered, prop >>

rx(self) == /\ LET me == ClientOf(self) IN
                 /\ s2c[me] # <<>>
                 /\ LET m == Head(s2c[me]) IN
                      /\ s2c' = [s2c EXCEPT ![me] = Tail(s2c[me])]
                      /\ IF m.k \in {"ack", "qfileop", "qfileack", "qinvalid", "qreject"}
                            THEN /\ LET e == Head(q[me]) IN
                                      LET drop == m.k = "qreject" /\ OnReject = "dropall" IN
                                        LET r == Rebase(IF drop THEN <<>> ELSE Tail(q[me]), IF m.k = "qfileop" THEN m.op ELSE <<>>) IN
                                          LET nr == IF m.k \in {"ack", "qfileop"} THEN rev[me] + 1 ELSE rev[me] IN
                                            LET same == loc[me].on /\ loc[me].txt = e.set /\ ~loc[me].fixed IN
                                              /\ q' = [q EXCEPT ![me] = r[1]]
                                              /\ rev' = [rev EXCEPT ![me] = nr]
                                              /\ IF r[1] # <<>>
                                                    THEN /\ c2s' = [c2s EXCEPT ![me] = Append(c2s[me], Msg(Head(r[1]), nr))]
                                                         /\ UNCHANGED waiting
                                                    ELSE /\ waiting' = [waiting EXCEPT ![me] = FALSE]
                                                         /\ c2s' = c2s
                                              /\ text' = [text EXCEPT ![me] = IF drop THEN shist[nr + 1].t ELSE ApplyOp(text[me], r[2])]
                                              /\ cset' = [cset EXCEPT ![me] = IF drop THEN shist[nr + 1].s ELSE SetOf(cset[me], r[2])]
                                              /\ loc' = [loc EXCEPT ![me] = CASE e.k # "qfile" \/ ~loc[me].on -> loc[me]
                                                                              [] m.k = "qinvalid" -> IF same THEN [loc[me] EXCEPT !.invalid = TRUE] ELSE loc[me]
                                                                              [] m.k = "qreject" -> IF same THEN [loc[me] EXCEPT !.invalid = TRUE] ELSE loc[me]
                                                                              [] same -> NoLoc
                                                                              [] FileRebase = "ackrev" -> [loc[me] EXCEPT !.base = nr, !.from = e.set]
                                                                              [] OTHER -> [loc[me] EXCEPT !.from = e.set]]
                                 /\ draft' = draft
                            ELSE /\ IF m.k = "invalid"
                                       THEN /\ draft' = [draft EXCEPT ![me].replied = TRUE]
                                            /\ UNCHANGED << text, cset, rev, q >>
                                       ELSE /\ LET r == Rebase(q[me], m.op) IN
                                                 /\ rev' = [rev EXCEPT ![me] = rev[me] + 1]
                                                 /\ q' = [q EXCEPT ![me] = r[1]]
                                                 /\ text' = [text EXCEPT ![me] = ApplyOp(text[me], r[2])]
                                                 /\ cset' = [cset EXCEPT ![me] = SetOf(cset[me], r[2])]
                                                 /\ IF m.k = "fileop"
                                                       THEN /\ draft' = [draft EXCEPT ![me] = NoDraft]
                                                       ELSE /\ TRUE
                                                            /\ draft' = draft
                                 /\ UNCHANGED << c2s, waiting, loc >>
            /\ UNCHANGED << stext, sset, slog, shist, setLog, sIns, sDel, 
                            issuedSets, invalidHandled, used, typedIns, 
                            typedDel, lastMine, othersAfter, clobbered, 
                            rejected, prop >>

Next == srv \/ asst
           \/ (\E self \in Clients: ed(self))
           \/ (\E self \in RxIds: rx(self))

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(srv)
        /\ \A self \in RxIds : WF_vars(rx(self))

\* END TRANSLATION
=============================================================================
