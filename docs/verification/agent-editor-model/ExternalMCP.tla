------------------------------- MODULE ExternalMCP -------------------------------
(* "Test as External MCP" mode in the Agent Editor's draft test chat (R27-R30, AE7, AE8).

   Two editors co-edit one draft: its settings (sticky, whether it uses a
   $ROCKETCLAW_METADATA_* variable) and its External MCP settings (switch, metadata
   pairs, caller), all stored on the draft (KTD6). Each editor's page holds a
   possibly stale view of the fields the page checks. A send captures the draft, builds
   the inbound as submitExternalMCPInput does, and runs column A (the parent
   conversation), then B (variant shadow) and Live (shadow).

   Real External MCP metadata rule (bridge.go turnShellEnv), applied by the bridge to
   every column-A turn whatever the plan variant:
   - first External MCP turn of a conversation: the sent pairs are the turn's metadata
     and are stored as the conversation's one metadata entry;
   - later turns, whatever their source: stored keys keep their stored values (a
     different or missing value is ignored); keys not in the entry are added for that
     turn only and never stored.
   Other code facts: ask-user only for a human Web-sourced turn; shadows run the parent's
   captured inputs and environment (incl. that turn's added keys) and never ask the user;
   real External MCP refuses non-`sticky: off` agents per call (Exposed index).

   Constants select the plan variant:
   - ServerStickyCheck: the draft prompt RPC refuses an External MCP send when sticky is not off.
   - CheckOnCaptured: that check reads the captured version, not a separate earlier draft read.
   - DraftWriteGuard: draft writes keep (on => sticky = off): turning the switch on is refused
     server-side when sticky is not off, and setting sticky to non-off turns the switch off.
   - PageClearsOn: only the second half: a page that sets sticky to non-off also turns the
     switch off; turning it on is still checked only on the (possibly stale) page.
   - FreezeMode: after the first message, "none" sends in the draft's current mode, "hold"
     sends in the chat's first mode, "refuse" refuses a send whose mode differs (restart).
   - MetaRule: "real" sends the draft's current pairs every message (the bridge applies the
     real rule); "chatfreeze" sends the pairs the chat held at its first message.
   - ConflictCheck: what happens to a send whose pairs change or drop a stored key:
     "none" sends it (the bridge silently keeps the stored value), "early" refuses it on a
     separate earlier read of the draft, "captured" refuses it on the captured version.
     A refusal offers a restart (AE8).
   - LiveSticky: sticky of the live agent's saved file.
   - LivePolicy: "run" runs Live in MCP mode regardless; "skip" refuses the Live column when
     LiveSticky is not off.
   - Callers: caller names an editor may type; one value unless a run checks caller agreement. *)
EXTENDS Naturals, Sequences, FiniteSets, TLC

CONSTANTS ServerStickyCheck, CheckOnCaptured, DraftWriteGuard, PageClearsOn,
          FreezeMode, MetaRule, ConflictCheck,
          LiveSticky, LivePolicy, MaxMsgs, MaxRestarts, Callers

ASSUME /\ ServerStickyCheck \in BOOLEAN /\ CheckOnCaptured \in BOOLEAN
       /\ DraftWriteGuard \in BOOLEAN /\ PageClearsOn \in BOOLEAN
       /\ FreezeMode \in {"none", "hold", "refuse"}
       /\ MetaRule \in {"real", "chatfreeze"}
       /\ ConflictCheck \in {"none", "early", "captured"}
       /\ LiveSticky \in {"off", "user"} /\ LivePolicy \in {"run", "skip"}
       /\ MaxMsgs \in Nat /\ MaxRestarts \in Nat /\ "c1" \in Callers

Editors == {1, 2}
Keys == {"k1", "k2"}
Values == {"_", "v1", "v2"}      \* "_" = key absent
NoMeta == [k1 |-> "_", k2 |-> "_"]
\* Pair sets an editor may enter; the empty set is allowed, as in External MCP.
Metas == {NoMeta, [k1 |-> "v1", k2 |-> "_"], [k1 |-> "v2", k2 |-> "_"], [k1 |-> "v1", k2 |-> "v1"]}
Draft0 == [sticky |-> "off", usesMetaVar |-> FALSE, on |-> FALSE,
           meta |-> [k1 |-> "v1", k2 |-> "_"], caller |-> "c1"]
Frozen0 == [set |-> FALSE, on |-> FALSE, meta |-> NoMeta]
Stored0 == [set |-> FALSE, meta |-> NoMeta]
PageView(d) == [sticky |-> d.sticky, on |-> d.on, usesMetaVar |-> d.usesMetaVar]

\* The inbound's ExternalMCPMetadata: a Web send carries none.
Pairs(v) == IF v.on THEN v.meta ELSE NoMeta
\* bridge.go turnShellEnv, as the draft path runs it on column A.
BridgeEnv(stored, v) ==
    IF ~stored.set THEN Pairs(v)
    ELSE [k \in Keys |-> IF stored.meta[k] # "_" THEN stored.meta[k] ELSE Pairs(v)[k]]
NewStored(stored, v) == IF ~stored.set /\ v.on THEN [set |-> TRUE, meta |-> v.meta] ELSE stored
\* A send that changes or drops a key the conversation stored.
Conflict(stored, m) == stored.set /\ \E k \in Keys : stored.meta[k] # "_" /\ m[k] # stored.meta[k]
LiveRuns(v) == LivePolicy = "run" \/ ~v.on \/ LiveSticky = "off"

MkTurn(ch, m, col, v, env, sugg, entered, drafton, n) ==
    [chat |-> ch, msg |-> m, col |-> col,
     mode |-> IF v.on THEN "mcp" ELSE "web",
     env |-> env, sent |-> Pairs(v), entered |-> entered, drafton |-> drafton, n |-> n,
     caller |-> IF v.on THEN v.caller ELSE "web",
     ask |-> col = "A" /\ ~v.on,
     sticky |-> IF col = "Live" THEN LiveSticky ELSE v.sticky,
     usesVar |-> v.usesMetaVar, sugg |-> sugg]

(* --algorithm ExternalMCP
variables
  draft = Draft0,
  page = [e \in Editors |-> PageView(Draft0)],
  chat = 0,          \* restart makes a new chat (new A, B and Live conversations)
  msgs = 0,          \* messages sent in the current chat
  stored = Stored0,  \* A's stored External MCP metadata entry
  frozen = Frozen0,  \* {mode, pairs} of the chat's first message
  hist = <<>>,       \* pairs the editors entered, one per MCP message of this chat, in order
  busy = FALSE,      \* one message in flight; columns are serialised per chat
  first = {},        \* the current chat's first A turn (empty before its first message)
  turns = {};        \* the column turns of the current chat's latest message

define
  TypeOK ==
    /\ draft.sticky \in {"off", "user"} /\ draft.on \in BOOLEAN
    /\ draft.meta \in Metas /\ draft.caller \in Callers
    /\ stored.set \in BOOLEAN /\ stored.meta \in Metas
    /\ \A t \in turns : t.env \in [Keys -> Values] /\ t.col \in {"A", "B", "Live"}

  \* What real External MCP gives a caller who sent the pair sets h, in order, on one
  \* conversation: the first set's keys keep their first values; any other key the latest
  \* set carries applies to that call only.
  RealExternalMCPEnv(h) ==
    [k \in Keys |-> IF h[1][k] # "_" THEN h[1][k] ELSE h[Len(h)][k]]

  \* P1 (R29, decided): every MCP turn, in every column, gets exactly what real External MCP
  \* would give for the same sequence of entered metadata.
  RealRule ==
    \A t \in turns : t.mode = "mcp" => t.env = RealExternalMCPEnv(SubSeq(hist, 1, t.n))

  \* P1m (P5): every turn of one chat, in every column, has the first turn's mode.
  \* Every turn of a chat is in turns at some state, so comparing with the first covers all pairs.
  FrozenPerChat == \A f \in first, t \in turns : f.chat = t.chat => f.mode = t.mode

  \* P1a (R29, AE8): the metadata an MCP turn ran with is what the send carried, which is
  \* what the page shows for that message; no silent mismatch.
  InboundHonest == \A t \in turns : t.mode = "mcp" => t.env = t.sent

  \* The mode a turn ran in is the mode the draft (and so the page) showed at send.
  ModeHonest == \A t \in turns : (t.mode = "mcp") = t.drafton

  \* P5: a Web-mode turn carries no External MCP metadata (no Web/MCP hybrid turn).
  NoHybridTurn == \A t \in turns : t.mode = "web" => t.env = NoMeta

  \* P2 (R28, AE7): no External MCP turn runs for an agent version whose sticky is not off.
  OnlyStickyOff == \A t \in turns : t.mode = "mcp" => t.sticky = "off"

  \* P3 (R27): within one message every column that ran got the same External MCP inbound.
  ColumnsAgree ==
    \A t1, t2 \in turns :
      t1.chat = t2.chat /\ t1.msg = t2.msg =>
        /\ t1.mode = t2.mode /\ t1.env = t2.env /\ t1.caller = t2.caller
        /\ (t1.mode = "mcp" => ~t1.ask)

  \* P4 (R30): a Web-mode first message of an agent using $ROCKETCLAW_METADATA_* was sent
  \* from a page that showed the suggestion.
  MetaVarSuggestion ==
    \A t \in turns : t.col = "A" /\ t.msg = 0 /\ t.usesVar /\ t.mode = "web" => t.sugg

  \* While a message is in flight, its sender is past the send gate.
  BusyHasSender == busy => \E e \in Editors : pc[e] # "Loop"

  \* The rejected whole-set rule: MCP-mode metadata never changes within a chat.
  MetaFrozenInMCPChat ==
    \A f \in first, t \in turns :
      f.chat = t.chat /\ f.mode = "mcp" /\ t.mode = "mcp" => f.env = t.env

  \* Non-vacuity probes: each must be VIOLATED.
  NoMCPTurn == \A t \in turns : t.mode # "mcp"
  NoMCPInRestartedChat == \A t \in turns : t.mode = "mcp" => t.chat = 0
  NoLiveMCPTurn == \A t \in turns : ~(t.col = "Live" /\ t.mode = "mcp")
  NoSecondMCPMessage == \A t \in turns : t.mode = "mcp" => t.msg = 0
end define;

process editor \in Editors
variables v = Draft0, sugg = FALSE, env = NoMeta, entered = NoMeta, drafton = FALSE;
begin
Loop:
  while TRUE do
    either
      \* a co-edit update reaches this page
      page[self] := PageView(draft);
    or
      \* R28: the page enables the switch only when it sees sticky: off
      await page[self].sticky = "off" /\ ~page[self].on;
      await ~DraftWriteGuard \/ draft.sticky = "off";
      draft.on := TRUE;
      page[self].on := TRUE;
    or
      await page[self].on;
      draft.on := FALSE;
      page[self].on := FALSE;
    or
      with s \in {"off", "user"} do
        draft := [draft EXCEPT !.sticky = s,
                               !.on = IF (DraftWriteGuard \/ PageClearsOn) /\ s # "off" THEN FALSE ELSE @];
        page[self] := [page[self] EXCEPT !.sticky = s,
                                         !.on = IF (DraftWriteGuard \/ PageClearsOn) /\ s # "off" THEN FALSE ELSE @];
      end with;
    or
      with m \in Metas do draft.meta := m; end with;
    or
      with c \in Callers do draft.caller := c; end with;
    or
      with u = ~draft.usesMetaVar do
        draft.usesMetaVar := u;
        page[self].usesMetaVar := u;
      end with;
    or
      \* restart: a new chat with whatever the draft holds now
      await ~busy /\ msgs > 0 /\ chat < MaxRestarts;
      chat := chat + 1; msgs := 0; stored := Stored0; frozen := Frozen0; hist := <<>>;
      first := {}; turns := {};
    or
      \* send from this page
      await ~busy /\ msgs < MaxMsgs;
      busy := TRUE;
      sugg := page[self].usesMetaVar /\ ~page[self].on /\ msgs = 0;
      Check:
        \* server checks against a separate, earlier read of the draft
        if \/ ServerStickyCheck /\ ~CheckOnCaptured /\ draft.on /\ draft.sticky # "off"
           \/ ConflictCheck = "early" /\ draft.on /\ Conflict(stored, draft.meta) then
          busy := FALSE; sugg := FALSE;
          goto Loop;
        end if;
      Capture:
        \* the version this message's columns run, and the inbound built from it
        v := IF frozen.set
               THEN [draft EXCEPT !.on = IF FreezeMode = "hold" THEN frozen.on ELSE @,
                                  !.meta = IF MetaRule = "chatfreeze" THEN frozen.meta ELSE @]
               ELSE draft;
        entered := draft.meta;
        drafton := draft.on;
      RunA:
        if \/ ServerStickyCheck /\ CheckOnCaptured /\ v.on /\ v.sticky # "off"
           \/ FreezeMode = "refuse" /\ frozen.set /\ v.on # frozen.on
           \/ ConflictCheck = "captured" /\ v.on /\ Conflict(stored, v.meta) then
          busy := FALSE; v := Draft0; sugg := FALSE; entered := NoMeta; drafton := FALSE;
          goto Loop;
        else
          with e = BridgeEnv(stored, v),
               h = IF v.on THEN Append(hist, entered) ELSE hist do
            env := e;
            hist := h;
            turns := {MkTurn(chat, msgs, "A", v, e, sugg, entered, drafton, Len(h))};
            if first = {} then
              first := {MkTurn(chat, msgs, "A", v, e, sugg, entered, drafton, Len(h))};
            end if;
          end with;
          stored := NewStored(stored, v);
          frozen := IF frozen.set THEN frozen ELSE [set |-> TRUE, on |-> v.on, meta |-> v.meta];
        end if;
      RunShadows:
        \* B and Live run A's captured inputs and environment
        turns := turns \cup {MkTurn(chat, msgs, "B", v, env, sugg, entered, drafton, Len(hist))}
                       \cup (IF LiveRuns(v)
                               THEN {MkTurn(chat, msgs, "Live", v, env, sugg, entered, drafton, Len(hist))}
                               ELSE {});
        msgs := msgs + 1;
        busy := FALSE; v := Draft0; sugg := FALSE; env := NoMeta; entered := NoMeta; drafton := FALSE;
    end either;
  end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "f34de2e2" /\ chksum(tla) = "f8763b0d")
VARIABLES draft, page, chat, msgs, stored, frozen, hist, busy, first, turns, 
          pc

(* define statement *)
TypeOK ==
  /\ draft.sticky \in {"off", "user"} /\ draft.on \in BOOLEAN
  /\ draft.meta \in Metas /\ draft.caller \in Callers
  /\ stored.set \in BOOLEAN /\ stored.meta \in Metas
  /\ \A t \in turns : t.env \in [Keys -> Values] /\ t.col \in {"A", "B", "Live"}




RealExternalMCPEnv(h) ==
  [k \in Keys |-> IF h[1][k] # "_" THEN h[1][k] ELSE h[Len(h)][k]]



RealRule ==
  \A t \in turns : t.mode = "mcp" => t.env = RealExternalMCPEnv(SubSeq(hist, 1, t.n))



FrozenPerChat == \A f \in first, t \in turns : f.chat = t.chat => f.mode = t.mode



InboundHonest == \A t \in turns : t.mode = "mcp" => t.env = t.sent


ModeHonest == \A t \in turns : (t.mode = "mcp") = t.drafton


NoHybridTurn == \A t \in turns : t.mode = "web" => t.env = NoMeta


OnlyStickyOff == \A t \in turns : t.mode = "mcp" => t.sticky = "off"


ColumnsAgree ==
  \A t1, t2 \in turns :
    t1.chat = t2.chat /\ t1.msg = t2.msg =>
      /\ t1.mode = t2.mode /\ t1.env = t2.env /\ t1.caller = t2.caller
      /\ (t1.mode = "mcp" => ~t1.ask)



MetaVarSuggestion ==
  \A t \in turns : t.col = "A" /\ t.msg = 0 /\ t.usesVar /\ t.mode = "web" => t.sugg


BusyHasSender == busy => \E e \in Editors : pc[e] # "Loop"


MetaFrozenInMCPChat ==
  \A f \in first, t \in turns :
    f.chat = t.chat /\ f.mode = "mcp" /\ t.mode = "mcp" => f.env = t.env


NoMCPTurn == \A t \in turns : t.mode # "mcp"
NoMCPInRestartedChat == \A t \in turns : t.mode = "mcp" => t.chat = 0
NoLiveMCPTurn == \A t \in turns : ~(t.col = "Live" /\ t.mode = "mcp")
NoSecondMCPMessage == \A t \in turns : t.mode = "mcp" => t.msg = 0

VARIABLES v, sugg, env, entered, drafton

vars == << draft, page, chat, msgs, stored, frozen, hist, busy, first, turns, 
           pc, v, sugg, env, entered, drafton >>

ProcSet == (Editors)

Init == (* Global variables *)
        /\ draft = Draft0
        /\ page = [e \in Editors |-> PageView(Draft0)]
        /\ chat = 0
        /\ msgs = 0
        /\ stored = Stored0
        /\ frozen = Frozen0
        /\ hist = <<>>
        /\ busy = FALSE
        /\ first = {}
        /\ turns = {}
        (* Process editor *)
        /\ v = [self \in Editors |-> Draft0]
        /\ sugg = [self \in Editors |-> FALSE]
        /\ env = [self \in Editors |-> NoMeta]
        /\ entered = [self \in Editors |-> NoMeta]
        /\ drafton = [self \in Editors |-> FALSE]
        /\ pc = [self \in ProcSet |-> "Loop"]

Loop(self) == /\ pc[self] = "Loop"
              /\ \/ /\ page' = [page EXCEPT ![self] = PageView(draft)]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<draft, chat, msgs, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ page[self].sticky = "off" /\ ~page[self].on
                    /\ ~DraftWriteGuard \/ draft.sticky = "off"
                    /\ draft' = [draft EXCEPT !.on = TRUE]
                    /\ page' = [page EXCEPT ![self].on = TRUE]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, msgs, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ page[self].on
                    /\ draft' = [draft EXCEPT !.on = FALSE]
                    /\ page' = [page EXCEPT ![self].on = FALSE]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, msgs, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ \E s \in {"off", "user"}:
                         /\ draft' = [draft EXCEPT !.sticky = s,
                                                   !.on = IF (DraftWriteGuard \/ PageClearsOn) /\ s # "off" THEN FALSE ELSE @]
                         /\ page' = [page EXCEPT ![self] = [page[self] EXCEPT !.sticky = s,
                                                                              !.on = IF (DraftWriteGuard \/ PageClearsOn) /\ s # "off" THEN FALSE ELSE @]]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, msgs, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ \E m \in Metas:
                         draft' = [draft EXCEPT !.meta = m]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<page, chat, msgs, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ \E c \in Callers:
                         draft' = [draft EXCEPT !.caller = c]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<page, chat, msgs, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ LET u == ~draft.usesMetaVar IN
                         /\ draft' = [draft EXCEPT !.usesMetaVar = u]
                         /\ page' = [page EXCEPT ![self].usesMetaVar = u]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, msgs, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ ~busy /\ msgs > 0 /\ chat < MaxRestarts
                    /\ chat' = chat + 1
                    /\ msgs' = 0
                    /\ stored' = Stored0
                    /\ frozen' = Frozen0
                    /\ hist' = <<>>
                    /\ first' = {}
                    /\ turns' = {}
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<draft, page, busy, sugg>>
                 \/ /\ ~busy /\ msgs < MaxMsgs
                    /\ busy' = TRUE
                    /\ sugg' = [sugg EXCEPT ![self] = page[self].usesMetaVar /\ ~page[self].on /\ msgs = 0]
                    /\ pc' = [pc EXCEPT ![self] = "Check"]
                    /\ UNCHANGED <<draft, page, chat, msgs, stored, frozen, hist, first, turns>>
              /\ UNCHANGED << v, env, entered, drafton >>

Check(self) == /\ pc[self] = "Check"
               /\ IF \/ ServerStickyCheck /\ ~CheckOnCaptured /\ draft.on /\ draft.sticky # "off"
                     \/ ConflictCheck = "early" /\ draft.on /\ Conflict(stored, draft.meta)
                     THEN /\ busy' = FALSE
                          /\ sugg' = [sugg EXCEPT ![self] = FALSE]
                          /\ pc' = [pc EXCEPT ![self] = "Loop"]
                     ELSE /\ pc' = [pc EXCEPT ![self] = "Capture"]
                          /\ UNCHANGED << busy, sugg >>
               /\ UNCHANGED << draft, page, chat, msgs, stored, frozen, hist, 
                               first, turns, v, env, entered, drafton >>

Capture(self) == /\ pc[self] = "Capture"
                 /\ v' = [v EXCEPT ![self] = IF frozen.set
                                               THEN [draft EXCEPT !.on = IF FreezeMode = "hold" THEN frozen.on ELSE @,
                                                                  !.meta = IF MetaRule = "chatfreeze" THEN frozen.meta ELSE @]
                                               ELSE draft]
                 /\ entered' = [entered EXCEPT ![self] = draft.meta]
                 /\ drafton' = [drafton EXCEPT ![self] = draft.on]
                 /\ pc' = [pc EXCEPT ![self] = "RunA"]
                 /\ UNCHANGED << draft, page, chat, msgs, stored, frozen, hist, 
                                 busy, first, turns, sugg, env >>

RunA(self) == /\ pc[self] = "RunA"
              /\ IF \/ ServerStickyCheck /\ CheckOnCaptured /\ v[self].on /\ v[self].sticky # "off"
                    \/ FreezeMode = "refuse" /\ frozen.set /\ v[self].on # frozen.on
                    \/ ConflictCheck = "captured" /\ v[self].on /\ Conflict(stored, v[self].meta)
                    THEN /\ busy' = FALSE
                         /\ v' = [v EXCEPT ![self] = Draft0]
                         /\ sugg' = [sugg EXCEPT ![self] = FALSE]
                         /\ entered' = [entered EXCEPT ![self] = NoMeta]
                         /\ drafton' = [drafton EXCEPT ![self] = FALSE]
                         /\ pc' = [pc EXCEPT ![self] = "Loop"]
                         /\ UNCHANGED << stored, frozen, hist, first, turns, 
                                         env >>
                    ELSE /\ LET e == BridgeEnv(stored, v[self]) IN
                              LET h == IF v[self].on THEN Append(hist, entered[self]) ELSE hist IN
                                /\ env' = [env EXCEPT ![self] = e]
                                /\ hist' = h
                                /\ turns' = {MkTurn(chat, msgs, "A", v[self], e, sugg[self], entered[self], drafton[self], Len(h))}
                                /\ IF first = {}
                                      THEN /\ first' = {MkTurn(chat, msgs, "A", v[self], e, sugg[self], entered[self], drafton[self], Len(h))}
                                      ELSE /\ TRUE
                                           /\ first' = first
                         /\ stored' = NewStored(stored, v[self])
                         /\ frozen' = IF frozen.set THEN frozen ELSE [set |-> TRUE, on |-> v[self].on, meta |-> v[self].meta]
                         /\ pc' = [pc EXCEPT ![self] = "RunShadows"]
                         /\ UNCHANGED << busy, v, sugg, entered, drafton >>
              /\ UNCHANGED << draft, page, chat, msgs >>

RunShadows(self) == /\ pc[self] = "RunShadows"
                    /\ turns' = (turns \cup {MkTurn(chat, msgs, "B", v[self], env[self], sugg[self], entered[self], drafton[self], Len(hist))}
                                       \cup (IF LiveRuns(v[self])
                                               THEN {MkTurn(chat, msgs, "Live", v[self], env[self], sugg[self], entered[self], drafton[self], Len(hist))}
                                               ELSE {}))
                    /\ msgs' = msgs + 1
                    /\ busy' = FALSE
                    /\ v' = [v EXCEPT ![self] = Draft0]
                    /\ sugg' = [sugg EXCEPT ![self] = FALSE]
                    /\ env' = [env EXCEPT ![self] = NoMeta]
                    /\ entered' = [entered EXCEPT ![self] = NoMeta]
                    /\ drafton' = [drafton EXCEPT ![self] = FALSE]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED << draft, page, chat, stored, frozen, hist, 
                                    first >>

editor(self) == Loop(self) \/ Check(self) \/ Capture(self) \/ RunA(self)
                   \/ RunShadows(self)

Next == (\E self \in Editors: editor(self))

Spec == Init /\ [][Next]_vars

\* END TRANSLATION 
=============================================================================
