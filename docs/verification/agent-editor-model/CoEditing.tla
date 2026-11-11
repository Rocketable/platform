----------------------------- MODULE CoEditing -----------------------------
(* Live co-editing of an Agent Editor draft (R17): server-ordered OT on the *)
(* instructions body plus one last-write-wins setting, with a File tab that *)
(* submits the whole text. Characters are unique ids so intent can be      *)
(* stated: <<"s", i>> seed text, <<client, n>> typed by a client.          *)
(* Mechanism switches (constants) select the planned design or a fix.      *)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS
  Clients,        \* browser sessions editing the draft
  FileClients,    \* clients that may edit in the File tab (subset of Clients)
  MaxOps,         \* local edits (form or File tab) per client in FileClients
  OtherOps,       \* local edits per other client
  SeedLen,        \* length of the initial body
  Mode,           \* delete-range vs concurrent insert strictly inside it:
                  \*   "plan"       single splice; the delete range widens over the insert,
                  \*                while an insert inside a deleted range survives (plan's rule)
                  \*   "clip"       single splice; the delete keeps only the part before the insert
                  \*   "deletewins" single splice, made consistent: the insert is dropped
                  \*   "split"      op = list of splices; the delete splits around the insert
                  \* Single-splice modes are checked with FileClients = {}: a File-tab splice
                  \* {at, remove, insert} is transformed here as its two parts, which is only
                  \* faithful when the result may be a list ("split").
  FileDiff,       \* "prefixsuffix": one splice from common prefix/suffix; "minimal": exact diff
  FileEdits,      \* "contiguous": a File-tab edit changes one range; "any": several places
  FileNeedsSync,  \* TRUE: a File-tab submit waits until no form op is in flight or buffered
  SetMode         \* "pendingwins": remote write to a key with a pending local write is ignored
                  \* "naive": remote writes are always applied

ASSUME Mode \in {"plan", "clip", "deletewins", "split"}
ASSUME FileDiff \in {"prefixsuffix", "minimal"}
ASSUME FileEdits \in {"contiguous", "any"}
ASSUME SetMode \in {"pendingwins", "naive"}
ASSUME FileClients \subseteq Clients

(* --algorithm CoEditing {
variables
  stext = [i \in 1..SeedLen |-> <<"s", i>>],   \* server body
  sset = <<"s", 0>>,                            \* server setting (one key)
  slog = <<>>,                                  \* op log: committed ops in server order
  shist = <<[t |-> [i \in 1..SeedLen |-> <<"s", i>>], s |-> <<"s", 0>>]>>, \* draft at each revision
  setLog = <<>>,                                \* every committed setting write, in order
  sIns = {}, sDel = {},                         \* ghost: committed inserted ids / intended deletes
  issuedSets = {},                              \* ghost: setting values sent by clients
  invalidHandled = 0,                           \* ghost: invalid File submits processed
  c2s = [c \in Clients |-> <<>>],               \* client -> server FIFO
  s2c = [c \in Clients |-> <<>>],               \* server -> client FIFO
  text = [c \in Clients |-> [i \in 1..SeedLen |-> <<"s", i>>]],
  cset = [c \in Clients |-> <<"s", 0>>],
  rev = [c \in Clients |-> 0],
  waiting = [c \in Clients |-> FALSE],          \* one op in flight
  inflight = [c \in Clients |-> <<>>],
  hasBuf = [c \in Clients |-> FALSE],           \* local edits not yet sent
  buf = [c \in Clients |-> <<>>],
  bufIntent = [c \in Clients |-> {}],
  bufIns = [c \in Clients |-> {}],
  used = [c \in Clients |-> 0],
  draft = [c \in Clients |-> NoDraft];          \* File-tab whole text awaiting the server

define {
  Budget(c) == IF c \in FileClients THEN MaxOps ELSE OtherOps
  RxIds == {"rx_" \o c : c \in Clients}
  ClientOf(r) == CHOOSE c \in Clients : "rx_" \o c = r
  Seed == [i \in 1..SeedLen |-> <<"s", i>>]
  SeedSet == <<"s", 0>>
  NoVal == <<"none", 0>>
  NoDraft == [on |-> FALSE, base |-> 0, txt |-> <<>>, set |-> NoVal, valid |-> TRUE,
              replied |-> FALSE, intent |-> {}, ins |-> {}, fid |-> NoVal]
  Min(x, y) == IF x < y THEN x ELSE y
  Max(x, y) == IF x > y THEN x ELSE y
  Ids(s) == {s[i] : i \in 1..Len(s)}

  \* Primitive edits. An op is a sequence of primitives applied in order;
  \* the plan's splice {at, remove, insert} is <<Del(at, remove), Ins(at, insert)>>.
  Ins(p, I) == [t |-> "ins", at |-> p, n |-> 0, ids |-> I, v |-> NoVal]
  Del(p, r) == [t |-> "del", at |-> p, n |-> r, ids |-> <<>>, v |-> NoVal]
  SetP(x) == [t |-> "set", at |-> 0, n |-> 0, ids |-> <<>>, v |-> x]

  ApplyP(s, x) ==
    CASE x.t = "ins" -> SubSeq(s, 1, x.at) \o x.ids \o SubSeq(s, x.at + 1, Len(s))
      [] x.t = "del" -> SubSeq(s, 1, x.at) \o SubSeq(s, x.at + x.n + 1, Len(s))
      [] OTHER -> s
  RECURSIVE ApplyOp(_, _)
  ApplyOp(s, op) == IF op = <<>> THEN s ELSE ApplyOp(ApplyP(s, Head(op)), Tail(op))
  RECURSIVE SetOf(_, _)
  SetOf(v, op) == IF op = <<>> THEN v
                  ELSE SetOf(IF Head(op).t = "set" THEN Head(op).v ELSE v, Tail(op))
  SetsIn(op) == {op[i].v : i \in {j \in 1..Len(op) : op[j].t = "set"}}

  \* a after b (both on the same text). aFirst: a is earlier in server order.
  XOne(a, b, aFirst) ==
    CASE a.t = "set" ->
           IF b.t = "set" /\ aFirst /\ SetMode = "pendingwins" THEN <<>> ELSE <<a>>
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

  \* <<A after B, B after A>> for ops (sequences of primitives).
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

  \* Server: transform an incoming op against every op committed after its base.
  RECURSIVE XL(_, _)
  XL(op, i) == IF i > Len(slog) THEN op ELSE XL(XS(op, slog[i], FALSE)[1], i + 1)

  \* File tab: whole text -> body op by diff against the base text.
  RECURSIVE Pre(_, _)
  Pre(s, u) == IF s = <<>> \/ u = <<>> \/ Head(s) # Head(u) THEN 0 ELSE 1 + Pre(Tail(s), Tail(u))
  RECURSIVE Suf(_, _, _)
  Suf(s, u, lim) ==
    IF lim = 0 \/ s[Len(s)] # u[Len(u)] THEN 0
    ELSE 1 + Suf(SubSeq(s, 1, Len(s) - 1), SubSeq(u, 1, Len(u) - 1), lim - 1)
  PSDiff(s, u) ==
    LET p == Pre(s, u)
        q == Suf(s, u, Min(Len(s), Len(u)) - p)
        r == Len(s) - p - q
        I == SubSeq(u, p + 1, Len(u) - q)
    IN (IF r > 0 THEN <<Del(p, r)>> ELSE <<>>) \o (IF I # <<>> THEN <<Ins(p, I)>> ELSE <<>>)
  RECURSIVE Dels(_, _, _)
  Dels(s, u, k) == IF k = 0 THEN <<>>
                   ELSE (IF s[k] \in Ids(u) THEN <<>> ELSE <<Del(k - 1, 1)>>) \o Dels(s, u, k - 1)
  RECURSIVE Inss(_, _, _)
  Inss(s, u, k) == IF k > Len(u) THEN <<>>
                   ELSE (IF u[k] \in Ids(s) THEN <<>> ELSE <<Ins(k - 1, <<u[k]>>)>>) \o Inss(s, u, k + 1)
  FileOp(b, m) ==
    (IF FileDiff = "prefixsuffix" THEN PSDiff(b.t, m.txt) ELSE Dels(b.t, m.txt, Len(b.t)) \o Inss(b.t, m.txt, 1))
    \o (IF m.set # b.s THEN <<SetP(m.set)>> ELSE <<>>)

  \* Whole texts a File-tab edit can produce from s (fid: the one new character).
  WithFid(s, fid) == {s} \cup {SubSeq(s, 1, i) \o <<fid>> \o SubSeq(s, i + 1, Len(s)) : i \in 0..Len(s)}
  FileTexts(s, fid) ==
    IF FileEdits = "contiguous"
    THEN {SubSeq(s, 1, p) \o I \o SubSeq(s, p + r + 1, Len(s)) :
            p \in 0..Len(s), r \in 0..Len(s), I \in {<<>>, <<fid>>}} \ {s}
    ELSE UNION {WithFid(SelectSeq(s, LAMBDA x : x \in K), fid) : K \in SUBSET Ids(s)} \ {s}

  Views == {stext} \cup {text[c] : c \in Clients}
  Settled == \A c \in Clients : c2s[c] = <<>> /\ s2c[c] = <<>> /\ ~waiting[c] /\ ~hasBuf[c]
  Quiescent == Settled /\ \A c \in Clients : ~draft[c].on

  \* 1. Convergence (R17).
  Convergence == Quiescent => \A c \in Clients : text[c] = stext /\ cset[c] = sset
  \* 2. No edit is lost (R17): server text = everything inserted minus what an author saw and deleted.
  NoLostEdit == Ids(stext) = (Ids(Seed) \cup sIns) \ sDel
  NoDuplicate == \A v \in Views : Cardinality(Ids(v)) = Len(v)
  \* 3. Surviving text keeps its relative order across every pair of views.
  Idx(s, x) == CHOOSE k \in 1..Len(s) : s[k] = x
  OrderPreserved ==
    \A v, w \in Views : \A i, j \in 1..Len(v) :
      (i < j /\ v[i] \in Ids(w) /\ v[j] \in Ids(w)) => Idx(w, v[i]) < Idx(w, v[j])
  \* 4. An invalid File-tab version stays private to the typist (R17), and commits nothing.
  InvalidPrivate ==
    \A c \in Clients : (draft[c].on /\ ~draft[c].valid) =>
      draft[c].fid \notin Ids(stext) \cup UNION {Ids(text[d]) : d \in Clients \ {c}}
  InvalidNoRevision ==
    [][invalidHandled' # invalidHandled => UNCHANGED <<slog, stext, sset>>]_<<invalidHandled, slog, stext, sset>>
  \* 5. Every committed setting write is in the op log; server value is the last one (LWW).
  PendingSets ==
    UNION {SetsIn(inflight[c]) \cup SetsIn(buf[c]) \cup
           UNION {IF c2s[c][i].k = "op" THEN SetsIn(c2s[c][i].op) ELSE {c2s[c][i].set} : i \in 1..Len(c2s[c])}
           : c \in Clients}
  HistoryComplete == \A v \in issuedSets : v \in Ids(setLog) \/ v \in PendingSets
  SettingsLWW == sset = IF setLog = <<>> THEN SeedSet ELSE setLog[Len(setLog)]
  \* Witness: every committed body op fits the plan's single splice {at, remove, insert}.
  TextPart(op) == SelectSeq(op, LAMBDA x : x.t # "set")
  OneSplice(op) == LET tp == TextPart(op) IN
    Len(tp) <= 1 \/ (Len(tp) = 2 /\ tp[1].t = "del" /\ tp[2].t = "ins" /\ tp[1].at = tp[2].at)
  ShapeSingleSplice == \A i \in 1..Len(slog) : OneSplice(slog[i])
  \* Cheap stand-in for liveness at wide bounds: each in-flight op has exactly one request or ack
  \* in transit, each pending File-tab submit exactly one request or reply; delivery steps are
  \* always enabled while a channel is non-empty, so nothing can get stuck.
  Cnt(s, kinds) == Len(SelectSeq(s, LAMBDA m : m.k \in kinds))
  NoStuck == \A c \in Clients :
    /\ Cnt(c2s[c], {"op"}) + Cnt(s2c[c], {"ack"}) = IF waiting[c] THEN 1 ELSE 0
    /\ Cnt(c2s[c], {"file"}) + Cnt(s2c[c], {"fileop", "invalid"}) = IF draft[c].on /\ ~draft[c].replied THEN 1 ELSE 0
    /\ hasBuf[c] => waiting[c]
  \* Liveness: once people stop typing, every op is acked and every message delivered.
  EventuallySettled == <>[]Settled
}

macro LocalOp(o, intent, ins) {
  \* intent is read from the pre-edit text, so text changes last
  if (~waiting[self]) {
    c2s[self] := Append(c2s[self], [k |-> "op", base |-> rev[self], op |-> o, intent |-> intent, ins |-> ins]);
    waiting[self] := TRUE;
    inflight[self] := o
  } else {
    buf[self] := buf[self] \o o;
    hasBuf[self] := TRUE;
    bufIntent[self] := bufIntent[self] \cup intent;
    bufIns[self] := bufIns[self] \cup ins
  };
  used[self] := used[self] + 1;
  cset[self] := SetOf(cset[self], o);
  text[self] := ApplyOp(text[self], o)
}

fair process (srv = "server") {
srvLoop:
  while (TRUE) {
    with (c \in {d \in Clients : c2s[d] # <<>>}; m = Head(c2s[c])) {
      c2s[c] := Tail(c2s[c]);
      if (m.k = "file" /\ ~m.valid) {
        \* "not yet valid": only the typist hears; nothing commits.
        s2c[c] := Append(s2c[c], [k |-> "invalid"]);
        invalidHandled := invalidHandled + 1
      } else {
        with (raw = IF m.k = "op" THEN m.op ELSE FileOp(shist[m.base + 1], m);
              op = XL(raw, m.base + 1);
              nt = ApplyOp(stext, op);
              ns = SetOf(sset, op)) {
          stext := nt;
          sset := ns;
          slog := Append(slog, op);
          shist := Append(shist, [t |-> nt, s |-> ns]);
          setLog := setLog \o SelectSeq([i \in 1..Len(op) |-> op[i].v], LAMBDA x : x # NoVal);
          sIns := sIns \cup m.ins;
          sDel := sDel \cup m.intent;
          s2c := [d \in Clients |-> Append(s2c[d],
                    IF d # c THEN [k |-> "op", op |-> op]
                    ELSE IF m.k = "op" THEN [k |-> "ack"] ELSE [k |-> "fileop", op |-> op])]
        }
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
      await used[self] < Budget(self) /\ ~draft[self].on;
      with (p \in 0..Len(text[self]); id = <<self, used[self] + 1>>) {
        LocalOp(<<Ins(p, <<id>>)>>, {}, {id})
      }
    } or {
      \* form: delete a selected range
      await used[self] < Budget(self) /\ ~draft[self].on /\ text[self] # <<>>;
      with (p \in 0..(Len(text[self]) - 1); r \in 1..(Len(text[self]) - p)) {
        LocalOp(<<Del(p, r)>>, Ids(SubSeq(text[self], p + 1, p + r)), {})
      }
    } or {
      \* form: change the setting
      await used[self] < Budget(self) /\ ~draft[self].on;
      with (x = <<self, used[self] + 1>>) {
        issuedSets := issuedSets \cup {x};
        LocalOp(<<SetP(x)>>, {}, {})
      }
    } or {
      \* File tab: edit and submit the whole text with its base revision
      await self \in FileClients /\ used[self] < Budget(self) /\ ~draft[self].on /\
            (~FileNeedsSync \/ (~waiting[self] /\ ~hasBuf[self]));
      \* either the body changes (valid or not yet valid) or only the setting changes
      with (fid = <<self, used[self] + 1>>;
            ch \in {[t |-> x, s |-> cset[self], ok |-> b] : x \in FileTexts(text[self], fid), b \in BOOLEAN}
                  \cup {[t |-> text[self], s |-> fid, ok |-> TRUE]};
            nt = ch.t; st = ch.s; ok = ch.ok) {
        draft[self] := [on |-> TRUE, base |-> rev[self], txt |-> nt, set |-> st, valid |-> ok,
                        replied |-> FALSE, intent |-> Ids(text[self]) \ Ids(nt),
                        ins |-> Ids(nt) \ Ids(text[self]), fid |-> fid];
        c2s[self] := Append(c2s[self], [k |-> "file", base |-> rev[self], txt |-> nt, set |-> st,
                        valid |-> ok, intent |-> Ids(text[self]) \ Ids(nt),
                        ins |-> Ids(nt) \ Ids(text[self])]);
        if (ok /\ st # cset[self]) { issuedSets := issuedSets \cup {st} };
        used[self] := used[self] + 1
      }
    } or {
      \* File tab: typist fixes the YAML and resubmits the same text, same base
      await draft[self].on /\ ~draft[self].valid /\ draft[self].replied;
      draft[self].valid := TRUE || draft[self].replied := FALSE;
      c2s[self] := Append(c2s[self], [k |-> "file", base |-> draft[self].base, txt |-> draft[self].txt,
                      set |-> draft[self].set, valid |-> TRUE, intent |-> draft[self].intent,
                      ins |-> draft[self].ins]);
      if (draft[self].set # shist[draft[self].base + 1].s) {
        issuedSets := issuedSets \cup {draft[self].set}
      }
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
        if (m.k = "ack") {
          if (hasBuf[me]) {
            c2s[me] := Append(c2s[me], [k |-> "op", base |-> rev[me] + 1, op |-> buf[me],
                            intent |-> bufIntent[me], ins |-> bufIns[me]]);
            inflight[me] := buf[me];
            buf[me] := <<>>;
            hasBuf[me] := FALSE;
            bufIntent[me] := {};
            bufIns[me] := {}
          } else {
            waiting[me] := FALSE;
            inflight[me] := <<>>
          };
          rev[me] := rev[me] + 1
        } else if (m.k = "invalid") {
          draft[me].replied := TRUE
        } else {
          \* remote op (or my own File-tab op): rebase pending ops, apply the transformed op
          with (x1 = XS(inflight[me], m.op, FALSE); x2 = XS(buf[me], x1[2], FALSE);
                ap = IF ~waiting[me] THEN m.op ELSE IF hasBuf[me] THEN x2[2] ELSE x1[2]) {
            rev[me] := rev[me] + 1;
            if (waiting[me]) { inflight[me] := x1[1] };
            if (hasBuf[me]) { buf[me] := x2[1] };
            text[me] := ApplyOp(text[me], ap);
            cset[me] := SetOf(cset[me], ap);
            if (m.k = "fileop") { draft[me] := NoDraft }
          }
        }
      }
    }
  }
}
} *)
\* BEGIN TRANSLATION (chksum(pcal) = "5b0ca217" /\ chksum(tla) = "89986966")
VARIABLES stext, sset, slog, shist, setLog, sIns, sDel, issuedSets, 
          invalidHandled, c2s, s2c, text, cset, rev, waiting, inflight, 
          hasBuf, buf, bufIntent, bufIns, used, draft

(* define statement *)
Budget(c) == IF c \in FileClients THEN MaxOps ELSE OtherOps
RxIds == {"rx_" \o c : c \in Clients}
ClientOf(r) == CHOOSE c \in Clients : "rx_" \o c = r
Seed == [i \in 1..SeedLen |-> <<"s", i>>]
SeedSet == <<"s", 0>>
NoVal == <<"none", 0>>
NoDraft == [on |-> FALSE, base |-> 0, txt |-> <<>>, set |-> NoVal, valid |-> TRUE,
            replied |-> FALSE, intent |-> {}, ins |-> {}, fid |-> NoVal]
Min(x, y) == IF x < y THEN x ELSE y
Max(x, y) == IF x > y THEN x ELSE y
Ids(s) == {s[i] : i \in 1..Len(s)}



Ins(p, I) == [t |-> "ins", at |-> p, n |-> 0, ids |-> I, v |-> NoVal]
Del(p, r) == [t |-> "del", at |-> p, n |-> r, ids |-> <<>>, v |-> NoVal]
SetP(x) == [t |-> "set", at |-> 0, n |-> 0, ids |-> <<>>, v |-> x]

ApplyP(s, x) ==
  CASE x.t = "ins" -> SubSeq(s, 1, x.at) \o x.ids \o SubSeq(s, x.at + 1, Len(s))
    [] x.t = "del" -> SubSeq(s, 1, x.at) \o SubSeq(s, x.at + x.n + 1, Len(s))
    [] OTHER -> s
RECURSIVE ApplyOp(_, _)
ApplyOp(s, op) == IF op = <<>> THEN s ELSE ApplyOp(ApplyP(s, Head(op)), Tail(op))
RECURSIVE SetOf(_, _)
SetOf(v, op) == IF op = <<>> THEN v
                ELSE SetOf(IF Head(op).t = "set" THEN Head(op).v ELSE v, Tail(op))
SetsIn(op) == {op[i].v : i \in {j \in 1..Len(op) : op[j].t = "set"}}


XOne(a, b, aFirst) ==
  CASE a.t = "set" ->
         IF b.t = "set" /\ aFirst /\ SetMode = "pendingwins" THEN <<>> ELSE <<a>>
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


RECURSIVE Pre(_, _)
Pre(s, u) == IF s = <<>> \/ u = <<>> \/ Head(s) # Head(u) THEN 0 ELSE 1 + Pre(Tail(s), Tail(u))
RECURSIVE Suf(_, _, _)
Suf(s, u, lim) ==
  IF lim = 0 \/ s[Len(s)] # u[Len(u)] THEN 0
  ELSE 1 + Suf(SubSeq(s, 1, Len(s) - 1), SubSeq(u, 1, Len(u) - 1), lim - 1)
PSDiff(s, u) ==
  LET p == Pre(s, u)
      q == Suf(s, u, Min(Len(s), Len(u)) - p)
      r == Len(s) - p - q
      I == SubSeq(u, p + 1, Len(u) - q)
  IN (IF r > 0 THEN <<Del(p, r)>> ELSE <<>>) \o (IF I # <<>> THEN <<Ins(p, I)>> ELSE <<>>)
RECURSIVE Dels(_, _, _)
Dels(s, u, k) == IF k = 0 THEN <<>>
                 ELSE (IF s[k] \in Ids(u) THEN <<>> ELSE <<Del(k - 1, 1)>>) \o Dels(s, u, k - 1)
RECURSIVE Inss(_, _, _)
Inss(s, u, k) == IF k > Len(u) THEN <<>>
                 ELSE (IF u[k] \in Ids(s) THEN <<>> ELSE <<Ins(k - 1, <<u[k]>>)>>) \o Inss(s, u, k + 1)
FileOp(b, m) ==
  (IF FileDiff = "prefixsuffix" THEN PSDiff(b.t, m.txt) ELSE Dels(b.t, m.txt, Len(b.t)) \o Inss(b.t, m.txt, 1))
  \o (IF m.set # b.s THEN <<SetP(m.set)>> ELSE <<>>)


WithFid(s, fid) == {s} \cup {SubSeq(s, 1, i) \o <<fid>> \o SubSeq(s, i + 1, Len(s)) : i \in 0..Len(s)}
FileTexts(s, fid) ==
  IF FileEdits = "contiguous"
  THEN {SubSeq(s, 1, p) \o I \o SubSeq(s, p + r + 1, Len(s)) :
          p \in 0..Len(s), r \in 0..Len(s), I \in {<<>>, <<fid>>}} \ {s}
  ELSE UNION {WithFid(SelectSeq(s, LAMBDA x : x \in K), fid) : K \in SUBSET Ids(s)} \ {s}

Views == {stext} \cup {text[c] : c \in Clients}
Settled == \A c \in Clients : c2s[c] = <<>> /\ s2c[c] = <<>> /\ ~waiting[c] /\ ~hasBuf[c]
Quiescent == Settled /\ \A c \in Clients : ~draft[c].on


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
  UNION {SetsIn(inflight[c]) \cup SetsIn(buf[c]) \cup
         UNION {IF c2s[c][i].k = "op" THEN SetsIn(c2s[c][i].op) ELSE {c2s[c][i].set} : i \in 1..Len(c2s[c])}
         : c \in Clients}
HistoryComplete == \A v \in issuedSets : v \in Ids(setLog) \/ v \in PendingSets
SettingsLWW == sset = IF setLog = <<>> THEN SeedSet ELSE setLog[Len(setLog)]

TextPart(op) == SelectSeq(op, LAMBDA x : x.t # "set")
OneSplice(op) == LET tp == TextPart(op) IN
  Len(tp) <= 1 \/ (Len(tp) = 2 /\ tp[1].t = "del" /\ tp[2].t = "ins" /\ tp[1].at = tp[2].at)
ShapeSingleSplice == \A i \in 1..Len(slog) : OneSplice(slog[i])



Cnt(s, kinds) == Len(SelectSeq(s, LAMBDA m : m.k \in kinds))
NoStuck == \A c \in Clients :
  /\ Cnt(c2s[c], {"op"}) + Cnt(s2c[c], {"ack"}) = IF waiting[c] THEN 1 ELSE 0
  /\ Cnt(c2s[c], {"file"}) + Cnt(s2c[c], {"fileop", "invalid"}) = IF draft[c].on /\ ~draft[c].replied THEN 1 ELSE 0
  /\ hasBuf[c] => waiting[c]

EventuallySettled == <>[]Settled


vars == << stext, sset, slog, shist, setLog, sIns, sDel, issuedSets, 
           invalidHandled, c2s, s2c, text, cset, rev, waiting, inflight, 
           hasBuf, buf, bufIntent, bufIns, used, draft >>

ProcSet == {"server"} \cup (Clients) \cup (RxIds)

Init == (* Global variables *)
        /\ stext = [i \in 1..SeedLen |-> <<"s", i>>]
        /\ sset = <<"s", 0>>
        /\ slog = <<>>
        /\ shist = <<[t |-> [i \in 1..SeedLen |-> <<"s", i>>], s |-> <<"s", 0>>]>>
        /\ setLog = <<>>
        /\ sIns = {}
        /\ sDel = {}
        /\ issuedSets = {}
        /\ invalidHandled = 0
        /\ c2s = [c \in Clients |-> <<>>]
        /\ s2c = [c \in Clients |-> <<>>]
        /\ text = [c \in Clients |-> [i \in 1..SeedLen |-> <<"s", i>>]]
        /\ cset = [c \in Clients |-> <<"s", 0>>]
        /\ rev = [c \in Clients |-> 0]
        /\ waiting = [c \in Clients |-> FALSE]
        /\ inflight = [c \in Clients |-> <<>>]
        /\ hasBuf = [c \in Clients |-> FALSE]
        /\ buf = [c \in Clients |-> <<>>]
        /\ bufIntent = [c \in Clients |-> {}]
        /\ bufIns = [c \in Clients |-> {}]
        /\ used = [c \in Clients |-> 0]
        /\ draft = [c \in Clients |-> NoDraft]

srv == /\ \E c \in {d \in Clients : c2s[d] # <<>>}:
            LET m == Head(c2s[c]) IN
              /\ c2s' = [c2s EXCEPT ![c] = Tail(c2s[c])]
              /\ IF m.k = "file" /\ ~m.valid
                    THEN /\ s2c' = [s2c EXCEPT ![c] = Append(s2c[c], [k |-> "invalid"])]
                         /\ invalidHandled' = invalidHandled + 1
                         /\ UNCHANGED << stext, sset, slog, shist, setLog, 
                                         sIns, sDel >>
                    ELSE /\ LET raw == IF m.k = "op" THEN m.op ELSE FileOp(shist[m.base + 1], m) IN
                              LET op == XL(raw, m.base + 1) IN
                                LET nt == ApplyOp(stext, op) IN
                                  LET ns == SetOf(sset, op) IN
                                    /\ stext' = nt
                                    /\ sset' = ns
                                    /\ slog' = Append(slog, op)
                                    /\ shist' = Append(shist, [t |-> nt, s |-> ns])
                                    /\ setLog' = setLog \o SelectSeq([i \in 1..Len(op) |-> op[i].v], LAMBDA x : x # NoVal)
                                    /\ sIns' = (sIns \cup m.ins)
                                    /\ sDel' = (sDel \cup m.intent)
                                    /\ s2c' = [d \in Clients |-> Append(s2c[d],
                                                 IF d # c THEN [k |-> "op", op |-> op]
                                                 ELSE IF m.k = "op" THEN [k |-> "ack"] ELSE [k |-> "fileop", op |-> op])]
                         /\ UNCHANGED invalidHandled
       /\ UNCHANGED << issuedSets, text, cset, rev, waiting, inflight, hasBuf, 
                       buf, bufIntent, bufIns, used, draft >>

ed(self) == /\ \/ /\ used[self] < Budget(self) /\ ~draft[self].on
                  /\ \E p \in 0..Len(text[self]):
                       LET id == <<self, used[self] + 1>> IN
                         /\ IF ~waiting[self]
                               THEN /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], [k |-> "op", base |-> rev[self], op |-> (<<Ins(p, <<id>>)>>), intent |-> ({}), ins |-> ({id})])]
                                    /\ waiting' = [waiting EXCEPT ![self] = TRUE]
                                    /\ inflight' = [inflight EXCEPT ![self] = <<Ins(p, <<id>>)>>]
                                    /\ UNCHANGED << hasBuf, buf, bufIntent, 
                                                    bufIns >>
                               ELSE /\ buf' = [buf EXCEPT ![self] = buf[self] \o (<<Ins(p, <<id>>)>>)]
                                    /\ hasBuf' = [hasBuf EXCEPT ![self] = TRUE]
                                    /\ bufIntent' = [bufIntent EXCEPT ![self] = bufIntent[self] \cup ({})]
                                    /\ bufIns' = [bufIns EXCEPT ![self] = bufIns[self] \cup ({id})]
                                    /\ UNCHANGED << c2s, waiting, inflight >>
                         /\ used' = [used EXCEPT ![self] = used[self] + 1]
                         /\ cset' = [cset EXCEPT ![self] = SetOf(cset[self], (<<Ins(p, <<id>>)>>))]
                         /\ text' = [text EXCEPT ![self] = ApplyOp(text[self], (<<Ins(p, <<id>>)>>))]
                  /\ UNCHANGED <<issuedSets, draft>>
               \/ /\ used[self] < Budget(self) /\ ~draft[self].on /\ text[self] # <<>>
                  /\ \E p \in 0..(Len(text[self]) - 1):
                       \E r \in 1..(Len(text[self]) - p):
                         /\ IF ~waiting[self]
                               THEN /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], [k |-> "op", base |-> rev[self], op |-> (<<Del(p, r)>>), intent |-> (Ids(SubSeq(text[self], p + 1, p + r))), ins |-> ({})])]
                                    /\ waiting' = [waiting EXCEPT ![self] = TRUE]
                                    /\ inflight' = [inflight EXCEPT ![self] = <<Del(p, r)>>]
                                    /\ UNCHANGED << hasBuf, buf, bufIntent, 
                                                    bufIns >>
                               ELSE /\ buf' = [buf EXCEPT ![self] = buf[self] \o (<<Del(p, r)>>)]
                                    /\ hasBuf' = [hasBuf EXCEPT ![self] = TRUE]
                                    /\ bufIntent' = [bufIntent EXCEPT ![self] = bufIntent[self] \cup (Ids(SubSeq(text[self], p + 1, p + r)))]
                                    /\ bufIns' = [bufIns EXCEPT ![self] = bufIns[self] \cup ({})]
                                    /\ UNCHANGED << c2s, waiting, inflight >>
                         /\ used' = [used EXCEPT ![self] = used[self] + 1]
                         /\ cset' = [cset EXCEPT ![self] = SetOf(cset[self], (<<Del(p, r)>>))]
                         /\ text' = [text EXCEPT ![self] = ApplyOp(text[self], (<<Del(p, r)>>))]
                  /\ UNCHANGED <<issuedSets, draft>>
               \/ /\ used[self] < Budget(self) /\ ~draft[self].on
                  /\ LET x == <<self, used[self] + 1>> IN
                       /\ issuedSets' = (issuedSets \cup {x})
                       /\ IF ~waiting[self]
                             THEN /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], [k |-> "op", base |-> rev[self], op |-> (<<SetP(x)>>), intent |-> ({}), ins |-> ({})])]
                                  /\ waiting' = [waiting EXCEPT ![self] = TRUE]
                                  /\ inflight' = [inflight EXCEPT ![self] = <<SetP(x)>>]
                                  /\ UNCHANGED << hasBuf, buf, bufIntent, 
                                                  bufIns >>
                             ELSE /\ buf' = [buf EXCEPT ![self] = buf[self] \o (<<SetP(x)>>)]
                                  /\ hasBuf' = [hasBuf EXCEPT ![self] = TRUE]
                                  /\ bufIntent' = [bufIntent EXCEPT ![self] = bufIntent[self] \cup ({})]
                                  /\ bufIns' = [bufIns EXCEPT ![self] = bufIns[self] \cup ({})]
                                  /\ UNCHANGED << c2s, waiting, inflight >>
                       /\ used' = [used EXCEPT ![self] = used[self] + 1]
                       /\ cset' = [cset EXCEPT ![self] = SetOf(cset[self], (<<SetP(x)>>))]
                       /\ text' = [text EXCEPT ![self] = ApplyOp(text[self], (<<SetP(x)>>))]
                  /\ draft' = draft
               \/ /\ self \in FileClients /\ used[self] < Budget(self) /\ ~draft[self].on /\
                     (~FileNeedsSync \/ (~waiting[self] /\ ~hasBuf[self]))
                  /\ LET fid == <<self, used[self] + 1>> IN
                       \E ch \in  {[t |-> x, s |-> cset[self], ok |-> b] : x \in FileTexts(text[self], fid), b \in BOOLEAN}
                                 \cup {[t |-> text[self], s |-> fid, ok |-> TRUE]}:
                         LET nt == ch.t IN
                           LET st == ch.s IN
                             LET ok == ch.ok IN
                               /\ draft' = [draft EXCEPT ![self] = [on |-> TRUE, base |-> rev[self], txt |-> nt, set |-> st, valid |-> ok,
                                                                    replied |-> FALSE, intent |-> Ids(text[self]) \ Ids(nt),
                                                                    ins |-> Ids(nt) \ Ids(text[self]), fid |-> fid]]
                               /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], [k |-> "file", base |-> rev[self], txt |-> nt, set |-> st,
                                                                  valid |-> ok, intent |-> Ids(text[self]) \ Ids(nt),
                                                                  ins |-> Ids(nt) \ Ids(text[self])])]
                               /\ IF ok /\ st # cset[self]
                                     THEN /\ issuedSets' = (issuedSets \cup {st})
                                     ELSE /\ TRUE
                                          /\ UNCHANGED issuedSets
                               /\ used' = [used EXCEPT ![self] = used[self] + 1]
                  /\ UNCHANGED <<text, cset, waiting, inflight, hasBuf, buf, bufIntent, bufIns>>
               \/ /\ draft[self].on /\ ~draft[self].valid /\ draft[self].replied
                  /\ draft' = [draft EXCEPT ![self].valid = TRUE,
                                            ![self].replied = FALSE]
                  /\ c2s' = [c2s EXCEPT ![self] = Append(c2s[self], [k |-> "file", base |-> draft'[self].base, txt |-> draft'[self].txt,
                                                     set |-> draft'[self].set, valid |-> TRUE, intent |-> draft'[self].intent,
                                                     ins |-> draft'[self].ins])]
                  /\ IF draft'[self].set # shist[draft'[self].base + 1].s
                        THEN /\ issuedSets' = (issuedSets \cup {draft'[self].set})
                        ELSE /\ TRUE
                             /\ UNCHANGED issuedSets
                  /\ UNCHANGED <<text, cset, waiting, inflight, hasBuf, buf, bufIntent, bufIns, used>>
            /\ UNCHANGED << stext, sset, slog, shist, setLog, sIns, sDel, 
                            invalidHandled, s2c, rev >>

rx(self) == /\ LET me == ClientOf(self) IN
                 /\ s2c[me] # <<>>
                 /\ LET m == Head(s2c[me]) IN
                      /\ s2c' = [s2c EXCEPT ![me] = Tail(s2c[me])]
                      /\ IF m.k = "ack"
                            THEN /\ IF hasBuf[me]
                                       THEN /\ c2s' = [c2s EXCEPT ![me] = Append(c2s[me], [k |-> "op", base |-> rev[me] + 1, op |-> buf[me],
                                                                               intent |-> bufIntent[me], ins |-> bufIns[me]])]
                                            /\ inflight' = [inflight EXCEPT ![me] = buf[me]]
                                            /\ buf' = [buf EXCEPT ![me] = <<>>]
                                            /\ hasBuf' = [hasBuf EXCEPT ![me] = FALSE]
                                            /\ bufIntent' = [bufIntent EXCEPT ![me] = {}]
                                            /\ bufIns' = [bufIns EXCEPT ![me] = {}]
                                            /\ UNCHANGED waiting
                                       ELSE /\ waiting' = [waiting EXCEPT ![me] = FALSE]
                                            /\ inflight' = [inflight EXCEPT ![me] = <<>>]
                                            /\ UNCHANGED << c2s, hasBuf, buf, 
                                                            bufIntent, bufIns >>
                                 /\ rev' = [rev EXCEPT ![me] = rev[me] + 1]
                                 /\ UNCHANGED << text, cset, draft >>
                            ELSE /\ IF m.k = "invalid"
                                       THEN /\ draft' = [draft EXCEPT ![me].replied = TRUE]
                                            /\ UNCHANGED << text, cset, rev, 
                                                            inflight, buf >>
                                       ELSE /\ LET x1 == XS(inflight[me], m.op, FALSE) IN
                                                 LET x2 == XS(buf[me], x1[2], FALSE) IN
                                                   LET ap == IF ~waiting[me] THEN m.op ELSE IF hasBuf[me] THEN x2[2] ELSE x1[2] IN
                                                     /\ rev' = [rev EXCEPT ![me] = rev[me] + 1]
                                                     /\ IF waiting[me]
                                                           THEN /\ inflight' = [inflight EXCEPT ![me] = x1[1]]
                                                           ELSE /\ TRUE
                                                                /\ UNCHANGED inflight
                                                     /\ IF hasBuf[me]
                                                           THEN /\ buf' = [buf EXCEPT ![me] = x2[1]]
                                                           ELSE /\ TRUE
                                                                /\ buf' = buf
                                                     /\ text' = [text EXCEPT ![me] = ApplyOp(text[me], ap)]
                                                     /\ cset' = [cset EXCEPT ![me] = SetOf(cset[me], ap)]
                                                     /\ IF m.k = "fileop"
                                                           THEN /\ draft' = [draft EXCEPT ![me] = NoDraft]
                                                           ELSE /\ TRUE
                                                                /\ draft' = draft
                                 /\ UNCHANGED << c2s, waiting, hasBuf, 
                                                 bufIntent, bufIns >>
            /\ UNCHANGED << stext, sset, slog, shist, setLog, sIns, sDel, 
                            issuedSets, invalidHandled, used >>

Next == srv
           \/ (\E self \in Clients: ed(self))
           \/ (\E self \in RxIds: rx(self))

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(srv)
        /\ \A self \in RxIds : WF_vars(rx(self))

\* END TRANSLATION 
=============================================================================
