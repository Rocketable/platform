------------------------- MODULE AgentEditorExternalMCP -------------------------
(* "Test as External MCP" in the agent editor's draft test chat, as built.

   Two editors co-edit one draft: its settings (sticky, whether it reads a
   $ROCKETCLAW_METADATA_* variable) and its External MCP setting (switch, metadata pairs,
   caller), stored on the draft as one object. Each editor's page holds a possibly stale
   view of the fields the page checks.

   A send (SendAgentDraft) holds a per-draft lock, captures the draft once and runs every
   check on that capture. An accepted message is stored in the test chat's queue with its
   record (a row of agent_draft_messages: mode, pairs, caller, whether Live is left out).
   The chat's first record fixes its mode and locked keys. The send returns at once; the
   chat's bridge runs queued messages later, in queue order: column A, then the variant
   columns (B) and Live, which reuse A's captured environment.

   Metadata pairs are typed under raw key names; the bridge turns each key into a variable
   name (externalMCPMetadataVariable), so two raw keys can become one variable. The model has
   raw keys k1, k1x (both variable V1, like a-b and a_b), k2 (V2), and cid (VC, the variable
   of external_conversation_id, which every External MCP message also carries with the chat
   id "id").

   Bridge metadata rule (bridge.go turnShellEnv), per variable, on a column-A turn:
   - first External MCP turn of a conversation: the sent variables are the turn's metadata
     and are stored as the conversation's one metadata entry;
   - any later turn, whatever its source: stored variables keep their stored values; other
     sent variables apply to that turn only and are never stored.
   Two raw keys of one variable with different values give either value (Go map order).

   Constants select the code as built or one earlier or alternative behavior:
   - ServerStickyCheck / CheckOnCaptured: the send refuses an External MCP message whose
     version is not sticky: off, checked on the capture (or on a separate earlier read).
   - DraftWriteGuard: draft writes keep (on => sticky = off). PageClearsOn: only a page that
     sets sticky to non-off also turns the switch off.
   - FreezeMode: after the first message, "none" sends in the draft's current mode, "hold" in
     the chat's first mode, "refuse" refuses a send whose mode differs (restart).
   - MetaRule: "real" sends the draft's current pairs; "chatfreeze" the first record's pairs.
   - ConflictCheck: a send whose pairs change or drop a locked key is sent ("none"), refused
     on a separate earlier read ("early"), or refused on the capture ("captured").
   - NormalizedLocks: locked keys are compared as the variables they become (a-b = a_b);
     otherwise by raw name.
   - CidAfterPairs: external_conversation_id is set after the typed pairs (so a typed pair of
     that name is overwritten, as real External MCP does); otherwise before them.
   - LiveSticky / LivePolicy: the live file's sticky; "skip" leaves Live out (recorded as not
     run) of an External MCP message when the live file is not sticky: off.
   - QueueEdits: generic queue RPCs accepted on the test chat (delete, reorder, steer).
   - GenericPrompt: someone sends a plain message to the test chat through the generic
     Prompt RPC. PromptRefused: that RPC refuses it (it queues nothing); otherwise the
     message is queued with no record and no draft files.
   - WholeObject: a pairs or caller write also writes the page's view of the switch. A page
     shows the switch as last read from the server, re-reads after its own write commits,
     and shows the pairs and caller inputs only while it shows the switch on. A write takes
     pairs and caller from the server's current value (pages hold no pairs).
   - OutOfOrder: a set_external_mcp request may commit after a later one from the same page
     (one request in flight at a time; its late commit is modeled on the switch).
   - SerialWrites: the External MCP panel sends its next write only after the previous one
     committed. NoticeWrites: the "Test as External MCP" notice button writes the switch.
     NoticeInChain: it goes through the panel's one-at-a-time chain (useMcpWrites);
     otherwise it writes directly, outside the chain.
   - ExtraPairs: pair sets editors may type beyond the plain ones: "alias" {k1x=v1};
     "clash" {k1=v1, k1x=v2} at any time; "clashlocked" the same set, sent only while the
     chat's first record locks V1; "reserved" {k1=v1, cid=v1}.
   - PairsCheck: the send refuses an External MCP capture with two keys of one variable, or
     a key of the external_conversation_id variable (agentDraftPairsProblem).
   - MetaVarEdits: editors change whether the agent reads a metadata variable (only
     MetaVarSuggestion reads it). *)
EXTENDS Naturals, Sequences, FiniteSets, TLC

CONSTANTS ServerStickyCheck, CheckOnCaptured, DraftWriteGuard, PageClearsOn,
          FreezeMode, MetaRule, ConflictCheck, NormalizedLocks, CidAfterPairs,
          LiveSticky, LivePolicy, MaxMsgs, MaxRestarts, Callers,
          QueueEdits, GenericPrompt, PromptRefused, WholeObject, OutOfOrder, SerialWrites,
          NoticeWrites, NoticeInChain, PairsCheck, ExtraPairs, MetaVarEdits

ASSUME /\ ServerStickyCheck \in BOOLEAN /\ CheckOnCaptured \in BOOLEAN
       /\ DraftWriteGuard \in BOOLEAN /\ PageClearsOn \in BOOLEAN
       /\ FreezeMode \in {"none", "hold", "refuse"}
       /\ MetaRule \in {"real", "chatfreeze"}
       /\ ConflictCheck \in {"none", "early", "captured"}
       /\ NormalizedLocks \in BOOLEAN /\ CidAfterPairs \in BOOLEAN
       /\ LiveSticky \in {"off", "user"} /\ LivePolicy \in {"run", "skip"}
       /\ MaxMsgs \in Nat /\ MaxRestarts \in Nat /\ "c1" \in Callers
       /\ QueueEdits \subseteq {"delete", "reorder", "steer"} /\ GenericPrompt \in BOOLEAN
       /\ WholeObject \in BOOLEAN /\ OutOfOrder \in BOOLEAN /\ SerialWrites \in BOOLEAN
       /\ PromptRefused \in BOOLEAN /\ NoticeWrites \in BOOLEAN /\ NoticeInChain \in BOOLEAN
       /\ PairsCheck \in BOOLEAN
       /\ ExtraPairs \subseteq {"alias", "clash", "clashlocked", "reserved"}
       /\ MetaVarEdits \in BOOLEAN

Editors == {1, 2}
Bridge == 0
RawKeys == {"k1", "k1x", "k2", "cid"}
Var(k) == CASE k \in {"k1", "k1x"} -> "V1" [] k = "k2" -> "V2" [] k = "cid" -> "VC"
Vars == {"V1", "V2", "VC"}
UserVars == {"V1", "V2"}
Values == {"_", "v1", "v2", "id"}      \* "_" = absent; "id" = the test chat's id
P(a, b, c, d) == [k1 |-> a, k1x |-> b, k2 |-> c, cid |-> d]
NoPairs == P("_", "_", "_", "_")
NoEnv == [V \in Vars |-> "_"]
AliasPair == P("_", "v1", "_", "_")
ClashPair == P("v1", "v2", "_", "_")
ReservedPair == P("v1", "_", "_", "v1")
\* Pair sets an editor may type; the empty set is allowed, as in External MCP.
Pairsets ==
  {NoPairs, P("v1", "_", "_", "_"), P("v2", "_", "_", "_"), P("v1", "_", "v1", "_")}
  \cup (IF "alias" \in ExtraPairs THEN {AliasPair} ELSE {})
  \cup (IF ExtraPairs \cap {"clash", "clashlocked"} # {} THEN {ClashPair} ELSE {})
  \cup (IF "reserved" \in ExtraPairs THEN {ReservedPair} ELSE {})
Draft0 == [sticky |-> "off", usesMetaVar |-> FALSE, on |-> FALSE,
           meta |-> P("v1", "_", "_", "_"), caller |-> "c1"]
Frozen0 == [set |-> FALSE, on |-> FALSE, meta |-> NoPairs]
Stored0 == [set |-> FALSE, meta |-> NoEnv]
\* The set_external_mcp request still in flight: its page (0: none), switch, and whether
\* it came from the panel's chain.
NoWrite == [owner |-> 0, on |-> FALSE, chain |-> FALSE]
PageView(d) == [sticky |-> d.sticky, on |-> d.on, usesMetaVar |-> d.usesMetaVar]

\* The pairs a record shows (and the page shows on its row): none for a Web message.
Pairs(v) == IF v.on THEN v.meta ELSE NoPairs
RawOf(V) == {k \in RawKeys : Var(k) = V}
Shown(p, V) == {p[k] : k \in RawOf(V)} \ {"_"}
\* The values the bridge may give V for the raw pairs p.
Choices(p, V) == IF Shown(p, V) = {} THEN {"_"} ELSE Shown(p, V)
EnvsOf(p) == {[V1 |-> a, V2 |-> b, VC |-> c] :
                a \in Choices(p, "V1"), b \in Choices(p, "V2"), c \in Choices(p, "VC")}
\* The inbound's ExternalMCPMetadata: typed pairs plus external_conversation_id.
Inbound(v) ==
  IF ~v.on THEN NoPairs
  ELSE IF CidAfterPairs \/ v.meta.cid = "_" THEN [v.meta EXCEPT !.cid = "id"] ELSE v.meta
\* bridge.go turnShellEnv, as the draft path runs it on column A.
BridgeEnvs(stored, v) ==
  {IF ~stored.set THEN e ELSE [V \in Vars |-> IF stored.meta[V] # "_" THEN stored.meta[V] ELSE e[V]]
     : e \in EnvsOf(Inbound(v))}
NewStored(stored, v, e) == IF ~stored.set /\ v.on THEN [set |-> TRUE, meta |-> e] ELSE stored
\* changedAgentDraftKey: a locked key that the sent pairs drop or change.
LockBroken(locked, sent) ==
  \E k \in RawKeys :
    /\ locked[k] # "_"
    /\ IF NormalizedLocks
         THEN Shown(sent, Var(k)) = {} \/ \E s \in RawOf(Var(k)) : sent[s] \notin {"_", locked[k]}
         ELSE sent[k] # locked[k]
Conflict(fr, m) == fr.set /\ fr.on /\ LockBroken(fr.meta, m)
LocksV1(fr) == fr.set /\ fr.on /\ Shown(fr.meta, "V1") # {}
\* Scenario limit of "clashlocked": the clashing set is sent only while V1 is locked.
ClashBarred(v, fr) ==
  "clash" \notin ExtraPairs /\ v.on /\ Cardinality(Shown(v.meta, "V1")) > 1 /\ ~LocksV1(fr)
LiveRuns(v) == LivePolicy = "run" \/ ~v.on \/ LiveSticky = "off"
\* The version a send captures, given the chat's first record.
Captured(d, fr) ==
  IF fr.set THEN [d EXCEPT !.on = IF FreezeMode = "hold" THEN fr.on ELSE @,
                           !.meta = IF MetaRule = "chatfreeze" THEN fr.meta ELSE @]
            ELSE d
\* Checks on a separate read of the draft before the capture.
EarlyRefused(d, fr) ==
  \/ ServerStickyCheck /\ ~CheckOnCaptured /\ d.on /\ d.sticky # "off"
  \/ ConflictCheck = "early" /\ d.on /\ Conflict(fr, d.meta)
\* agentDraftPairsProblem: two keys of one variable, or a key of external_conversation_id's.
PairsProblem(m) == (\E V \in UserVars : Cardinality({k \in RawOf(V) : m[k] # "_"}) > 1) \/ m.cid # "_"
\* Checks on the captured version (agent_draft_chat.go: sticky, pairs, mode, locked keys).
Refused(v, fr) ==
  \/ ServerStickyCheck /\ CheckOnCaptured /\ v.on /\ v.sticky # "off"
  \/ PairsCheck /\ v.on /\ PairsProblem(v.meta)
  \/ FreezeMode = "refuse" /\ fr.set /\ v.on # fr.on
  \/ ConflictCheck = "captured" /\ v.on /\ Conflict(fr, v.meta)
\* One message's record, plus ghost fields the properties read: entered = the pairs the
\* draft held, drafton = the draft's switch, sugg = the metadata-variable suggestion shown,
\* generic = queued by the generic Prompt RPC (no record row, no draft files).
Record(v, d, s) == [v |-> v, entered |-> d.meta, drafton |-> d.on, sugg |-> s,
                    live |-> LiveRuns(v), generic |-> FALSE]
GenericRecord(d) == [v |-> [d EXCEPT !.on = FALSE, !.usesMetaVar = FALSE], entered |-> NoPairs,
                     drafton |-> d.on, sugg |-> FALSE, live |-> FALSE, generic |-> TRUE]

MkTurn(ch, i, col, r, env) ==
  [chat |-> ch, msg |-> i, col |-> col,
   mode |-> IF r.v.on THEN "mcp" ELSE "web",
   env |-> env,
   caller |-> IF r.v.on THEN r.v.caller ELSE "web",
   ask |-> col = "A" /\ ~r.v.on,
   sticky |-> IF col = "Live" \/ r.generic THEN LiveSticky ELSE r.v.sticky]
\* The column turns one message gets: A, B, and Live unless its record leaves Live out. A
\* generic message has no draft files, so the test chat starts no shadows for it.
Columns(ch, i, r, env) ==
  {MkTurn(ch, i, c, r, env) :
     c \in IF r.generic THEN {"A"} ELSE IF r.live THEN {"A", "B", "Live"} ELSE {"A", "B"}}

(* --algorithm AgentEditorExternalMCP
variables
  draft = Draft0,
  page = [e \in Editors |-> PageView(Draft0)],
  chat = 0,          \* restart makes a new chat (new A, B and Live conversations)
  recs = <<>>,       \* the current chat's records, one per queued message, in send order
  q = <<>>,          \* indices into recs of the chat's waiting messages, in run order
  stored = Stored0,  \* A's stored External MCP metadata entry (written by the bridge)
  frozen = Frozen0,  \* {mode, pairs} of the chat's first record (written by the send)
  hist = <<>>,       \* pairs the editors entered, one per External MCP message run
  busy = FALSE,      \* the per-draft send lock
  running = 0,       \* index of the message whose turn the bridge is running (0: none)
  renv = NoEnv,      \* that turn's environment
  first = {},        \* the current chat's first A turn (empty before one ran)
  turns = {},        \* the column turns of the latest message run
  late = NoWrite,    \* a set_external_mcp request not yet committed
  overtaken = FALSE; \* a later write of the same page committed before it

define
  TypeOK ==
    /\ draft.sticky \in {"off", "user"} /\ draft.on \in BOOLEAN
    /\ draft.meta \in Pairsets /\ draft.caller \in Callers
    /\ stored.set \in BOOLEAN /\ stored.meta \in [Vars -> Values]
    /\ Len(recs) <= MaxMsgs /\ \A i \in 1..Len(q) : q[i] \in 1..Len(recs)
    /\ \A t \in turns : t.env \in [Vars -> Values] /\ t.col \in {"A", "B", "Live"}
                        /\ t.msg \in 1..Len(recs)

  \* What real External MCP may give V for the pair sets h, in order, on one conversation:
  \* a variable of the first set keeps its first value; another variable comes from the
  \* latest set only. external_conversation_id is set after the caller's pairs.
  RealChoices(h, V) ==
    LET f == Choices([h[1] EXCEPT !.cid = "id"], V) IN
    IF f # {"_"} THEN f ELSE Choices([h[Len(h)] EXCEPT !.cid = "id"], V)

  \* Every External MCP turn, in every column, gets what real External MCP would give for
  \* the same sequence of entered pairs (turns holds the latest run's turns).
  RealRule ==
    \A t \in turns : t.mode = "mcp" => \A V \in Vars : t.env[V] \in RealChoices(hist, V)

  \* Every turn of one chat, in every column, has the first turn's mode.
  FrozenPerChat == \A f \in first, t \in turns : f.chat = t.chat => f.mode = t.mode

  \* An External MCP turn ran with exactly the variables its message's row shows
  \* (chat.tsx Row reads record.externalMcp). The chat id behind external_conversation_id is
  \* not shown, so VC is only checked when a typed pair names it.
  InboundHonest ==
    \A t \in turns : t.mode = "mcp" =>
      \A V \in Vars :
        LET s == Shown(Pairs(recs[t.msg].v), V) IN
        \/ s = {t.env[V]}
        \/ s = {} /\ (V = "VC" \/ t.env[V] = "_")

  \* The variables the page marks as fixed (keys of the chat's first record) are the ones the
  \* bridge stored; a key shown as "this message only" is not kept by the bridge.
  LocksAgree ==
    stored.set => \A V \in UserVars :
      (stored.meta[V] # "_") = (frozen.on /\ Shown(frozen.meta, V) # {})

  \* The mode a turn ran in is the mode the draft (and so the page) showed at send.
  ModeHonest == \A t \in turns : (t.mode = "mcp") = recs[t.msg].drafton

  \* A Web-mode turn carries no External MCP metadata (no Web/External MCP hybrid turn).
  NoHybridTurn == \A t \in turns : t.mode = "web" => t.env = NoEnv

  \* No External MCP turn runs an agent version whose sticky is not off.
  OnlyStickyOff == \A t \in turns : t.mode = "mcp" => t.sticky = "off"

  \* Within one message every column that ran got the same External MCP inbound, and no
  \* ask-user in External MCP mode.
  ColumnsAgree ==
    \A t1, t2 \in turns :
      t1.chat = t2.chat /\ t1.msg = t2.msg =>
        /\ t1.mode = t2.mode /\ t1.env = t2.env /\ t1.caller = t2.caller
        /\ (t1.mode = "mcp" => ~t1.ask)

  \* Live answered a message exactly when its record does not list Live as not run.
  NotRunHonest ==
    pc[Bridge] = "BRun" =>
      \A t \in turns : t.col = "A" =>
        (recs[t.msg].live = \E u \in turns : u.col = "Live" /\ u.msg = t.msg)

  \* A Web-mode first message of a sticky: off agent that reads a metadata variable was sent
  \* from a page that showed the suggestion to test it as External MCP.
  MetaVarSuggestion ==
    \A t \in turns :
      t.col = "A" /\ t.msg = 1 /\ recs[1].v.usesMetaVar /\ recs[1].v.sticky = "off"
        /\ t.mode = "web" => recs[1].sugg

  \* While the send lock is held, its sender is past the send gate.
  BusyHasSender == busy => \E e \in Editors : pc[e] # "Loop"

  \* A page's External MCP writes commit in the order the page made them: no write is still
  \* in flight whose later write already committed and whose commit would undo it.
  NoOwnRegress == ~(overtaken /\ late.on # draft.on)

  \* A pairs or caller write never changes the switch (no write overwrites another person's
  \* switch with a stale view). An action property.
  NoSwitchOverwrite ==
    [][(draft'.meta # draft.meta \/ draft'.caller # draft.caller) => draft'.on = draft.on]_draft

  \* The rejected whole-set rule: External MCP metadata never changes within a chat.
  MetaFrozenInMCPChat ==
    \A f \in first, t \in turns :
      f.chat = t.chat /\ f.mode = "mcp" /\ t.mode = "mcp" => f.env = t.env

  \* Non-vacuity probes: each must be VIOLATED.
  NoMCPTurn == \A t \in turns : t.mode # "mcp"
  NoMCPInRestartedChat == \A t \in turns : t.mode = "mcp" => t.chat = 0
  NoLiveMCPTurn == \A t \in turns : ~(t.col = "Live" /\ t.mode = "mcp")
  NoSecondMCPMessage == \A t \in turns : t.mode = "mcp" => t.msg = 1
  \* A second message that names locked k1 as k1x runs.
  NoRenamedLockedKey ==
    \A t \in turns :
      t.mode = "mcp" /\ t.msg = 2 =>
        ~(Pairs(recs[1].v).k1 # "_" /\ Pairs(recs[2].v).k1 = "_" /\ Pairs(recs[2].v).k1x # "_")
end define;

\* One set_external_mcp request with whole value w: committed now, or (OutOfOrder) left in
\* flight to commit later, after writes the page makes meanwhile. The panel's chain waits
\* for its own previous write; the notice button does not. A commit is followed by the
\* page's re-read (chat.tsx apply settles by refetching the draft).
macro writeMcp(w, chained) begin
  await ~(SerialWrites /\ chained /\ late.owner = self /\ late.chain);
  either
    draft := [draft EXCEPT !.on = w.on, !.meta = w.meta, !.caller = w.caller];
    page[self] := PageView(draft);
    if late.owner = self then overtaken := TRUE; end if;
  or
    await OutOfOrder /\ late.owner = 0;
    late := [owner |-> self, on |-> w.on, chain |-> chained];
    draft := [draft EXCEPT !.meta = w.meta, !.caller = w.caller];
  end either;
end macro;

process editor \in Editors
variables sugg = FALSE;
begin
Loop:
  while TRUE do
    either
      \* a co-edit update reaches this page
      page[self] := PageView(draft);
    or
      \* the panel's switch, enabled only when the page sees sticky: off; the page shows the
      \* switch as last read from the server, so it sees its own write only on a later read
      await page[self].sticky = "off" /\ ~page[self].on;
      await ~DraftWriteGuard \/ draft.sticky = "off";
      writeMcp([draft EXCEPT !.on = TRUE], TRUE);
    or
      \* the notice's "Test as External MCP" button, shown before the chat's first message
      await NoticeWrites /\ page[self].sticky = "off" /\ ~page[self].on /\ recs = <<>>;
      await ~DraftWriteGuard \/ draft.sticky = "off";
      writeMcp([draft EXCEPT !.on = TRUE], NoticeInChain);
    or
      await page[self].on;
      writeMcp([draft EXCEPT !.on = FALSE], TRUE);
    or
      with s \in {"off", "user"} do
        draft := [draft EXCEPT !.sticky = s,
                               !.on = IF (DraftWriteGuard \/ PageClearsOn) /\ s # "off" THEN FALSE ELSE @];
        page[self] := [page[self] EXCEPT !.sticky = s,
                                         !.on = IF (DraftWriteGuard \/ PageClearsOn) /\ s # "off" THEN FALSE ELSE @];
      end with;
    or
      \* a pairs edit (the inputs show only while the page shows the switch on); the whole
      \* object carries this page's view of the switch
      await page[self].on;
      with m \in Pairsets, o = IF WholeObject THEN page[self].on ELSE draft.on do
        writeMcp([draft EXCEPT !.meta = m, !.on = o], TRUE);
      end with;
    or
      await page[self].on;
      with c \in Callers, o = IF WholeObject THEN page[self].on ELSE draft.on do
        writeMcp([draft EXCEPT !.caller = c, !.on = o], TRUE);
      end with;
    or
      \* this page's in-flight External MCP write commits
      await late.owner = self;
      draft.on := late.on;
      page[self] := PageView(draft);
      late := NoWrite;
      overtaken := FALSE;
    or
      await MetaVarEdits;
      with u = ~draft.usesMetaVar do
        draft.usesMetaVar := u;
        page[self].usesMetaVar := u;
      end with;
    or
      \* restart: a new chat with whatever the draft holds now (once the old one is idle)
      await ~busy /\ recs # <<>> /\ q = <<>> /\ pc[Bridge] = "BRun" /\ chat < MaxRestarts;
      chat := chat + 1; recs := <<>>; stored := Stored0; frozen := Frozen0; hist := <<>>;
      first := {}; turns := {};
    or
      \* someone deletes a waiting message of the test chat
      await "delete" \in QueueEdits /\ q # <<>>;
      with i \in 1..Len(q) do
        q := SubSeq(q, 1, i - 1) \o SubSeq(q, i + 1, Len(q));
      end with;
    or
      \* someone moves the second waiting message first
      await "reorder" \in QueueEdits /\ Len(q) >= 2;
      q := <<q[2], q[1]>> \o SubSeq(q, 3, Len(q));
    or
      \* someone steers the next waiting message into the running turn: it joins that turn
      await "steer" \in QueueEdits /\ q # <<>> /\ pc[Bridge] = "BShadows";
      turns := turns \cup Columns(chat, Head(q), recs[Head(q)], renv);
      q := Tail(q);
    or
      \* someone sends a plain Web message through the generic Prompt RPC (no draft lock);
      \* when refused it leaves nothing
      await GenericPrompt /\ Len(recs) < MaxMsgs;
      if ~PromptRefused then
        q := Append(q, Len(recs) + 1);
        recs := Append(recs, GenericRecord(draft));
      end if;
    or
      \* send from this page; checks on a separate read happen before the lock's capture
      await ~busy /\ Len(recs) < MaxMsgs /\ ~EarlyRefused(draft, frozen);
      busy := TRUE;
      sugg := page[self].usesMetaVar /\ ~page[self].on /\ recs = <<>> /\ page[self].sticky = "off";
      Capture:
        \* one capture; every check runs on it; an accepted message is queued with its record
        await ~ClashBarred(Captured(draft, frozen), frozen);
        with v = Captured(draft, frozen), i = Len(recs) + 1 do
          if ~Refused(v, frozen) then
            recs := Append(recs, Record(v, draft, sugg));
            q := Append(q, i);
            frozen := IF frozen.set THEN frozen ELSE [set |-> TRUE, on |-> v.on, meta |-> v.meta];
          end if;
        end with;
        busy := FALSE; sugg := FALSE;
    end either;
  end while;
end process;

\* The test chat's bridge runs its queued messages in queue order.
process bridge = Bridge
begin
BRun:
  while TRUE do
    await q # <<>>;
    with i = Head(q), r = recs[Head(q)], e \in BridgeEnvs(stored, recs[Head(q)].v),
         h = IF recs[Head(q)].v.on THEN Append(hist, recs[Head(q)].entered) ELSE hist do
      running := i; renv := e; hist := h;
      turns := {MkTurn(chat, i, "A", r, e)};
      if first = {} then
        first := {MkTurn(chat, i, "A", r, e)};
      end if;
      stored := NewStored(stored, r.v, e);
      q := Tail(q);
    end with;
  BShadows:
    \* B and Live run A's captured inputs and environment
    turns := turns \cup Columns(chat, running, recs[running], renv);
    running := 0; renv := NoEnv;
  end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, draft, page, chat, recs, q, stored, frozen, hist, busy, running, 
          renv, first, turns, late, overtaken

(* define statement *)
TypeOK ==
  /\ draft.sticky \in {"off", "user"} /\ draft.on \in BOOLEAN
  /\ draft.meta \in Pairsets /\ draft.caller \in Callers
  /\ stored.set \in BOOLEAN /\ stored.meta \in [Vars -> Values]
  /\ Len(recs) <= MaxMsgs /\ \A i \in 1..Len(q) : q[i] \in 1..Len(recs)
  /\ \A t \in turns : t.env \in [Vars -> Values] /\ t.col \in {"A", "B", "Live"}
                      /\ t.msg \in 1..Len(recs)




RealChoices(h, V) ==
  LET f == Choices([h[1] EXCEPT !.cid = "id"], V) IN
  IF f # {"_"} THEN f ELSE Choices([h[Len(h)] EXCEPT !.cid = "id"], V)



RealRule ==
  \A t \in turns : t.mode = "mcp" => \A V \in Vars : t.env[V] \in RealChoices(hist, V)


FrozenPerChat == \A f \in first, t \in turns : f.chat = t.chat => f.mode = t.mode




InboundHonest ==
  \A t \in turns : t.mode = "mcp" =>
    \A V \in Vars :
      LET s == Shown(Pairs(recs[t.msg].v), V) IN
      \/ s = {t.env[V]}
      \/ s = {} /\ (V = "VC" \/ t.env[V] = "_")



LocksAgree ==
  stored.set => \A V \in UserVars :
    (stored.meta[V] # "_") = (frozen.on /\ Shown(frozen.meta, V) # {})


ModeHonest == \A t \in turns : (t.mode = "mcp") = recs[t.msg].drafton


NoHybridTurn == \A t \in turns : t.mode = "web" => t.env = NoEnv


OnlyStickyOff == \A t \in turns : t.mode = "mcp" => t.sticky = "off"



ColumnsAgree ==
  \A t1, t2 \in turns :
    t1.chat = t2.chat /\ t1.msg = t2.msg =>
      /\ t1.mode = t2.mode /\ t1.env = t2.env /\ t1.caller = t2.caller
      /\ (t1.mode = "mcp" => ~t1.ask)


NotRunHonest ==
  pc[Bridge] = "BRun" =>
    \A t \in turns : t.col = "A" =>
      (recs[t.msg].live = \E u \in turns : u.col = "Live" /\ u.msg = t.msg)



MetaVarSuggestion ==
  \A t \in turns :
    t.col = "A" /\ t.msg = 1 /\ recs[1].v.usesMetaVar /\ recs[1].v.sticky = "off"
      /\ t.mode = "web" => recs[1].sugg


BusyHasSender == busy => \E e \in Editors : pc[e] # "Loop"



NoOwnRegress == ~(overtaken /\ late.on # draft.on)



NoSwitchOverwrite ==
  [][(draft'.meta # draft.meta \/ draft'.caller # draft.caller) => draft'.on = draft.on]_draft


MetaFrozenInMCPChat ==
  \A f \in first, t \in turns :
    f.chat = t.chat /\ f.mode = "mcp" /\ t.mode = "mcp" => f.env = t.env


NoMCPTurn == \A t \in turns : t.mode # "mcp"
NoMCPInRestartedChat == \A t \in turns : t.mode = "mcp" => t.chat = 0
NoLiveMCPTurn == \A t \in turns : ~(t.col = "Live" /\ t.mode = "mcp")
NoSecondMCPMessage == \A t \in turns : t.mode = "mcp" => t.msg = 1

NoRenamedLockedKey ==
  \A t \in turns :
    t.mode = "mcp" /\ t.msg = 2 =>
      ~(Pairs(recs[1].v).k1 # "_" /\ Pairs(recs[2].v).k1 = "_" /\ Pairs(recs[2].v).k1x # "_")

VARIABLE sugg

vars == << pc, draft, page, chat, recs, q, stored, frozen, hist, busy, 
           running, renv, first, turns, late, overtaken, sugg >>

ProcSet == (Editors) \cup {Bridge}

Init == (* Global variables *)
        /\ draft = Draft0
        /\ page = [e \in Editors |-> PageView(Draft0)]
        /\ chat = 0
        /\ recs = <<>>
        /\ q = <<>>
        /\ stored = Stored0
        /\ frozen = Frozen0
        /\ hist = <<>>
        /\ busy = FALSE
        /\ running = 0
        /\ renv = NoEnv
        /\ first = {}
        /\ turns = {}
        /\ late = NoWrite
        /\ overtaken = FALSE
        (* Process editor *)
        /\ sugg = [self \in Editors |-> FALSE]
        /\ pc = [self \in ProcSet |-> CASE self \in Editors -> "Loop"
                                        [] self = Bridge -> "BRun"]

Loop(self) == /\ pc[self] = "Loop"
              /\ \/ /\ page' = [page EXCEPT ![self] = PageView(draft)]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<draft, chat, recs, q, stored, frozen, hist, busy, first, turns, late, overtaken, sugg>>
                 \/ /\ page[self].sticky = "off" /\ ~page[self].on
                    /\ ~DraftWriteGuard \/ draft.sticky = "off"
                    /\ ~(SerialWrites /\ TRUE /\ late.owner = self /\ late.chain)
                    /\ \/ /\ draft' = [draft EXCEPT !.on = ([draft EXCEPT !.on = TRUE]).on, !.meta = ([draft EXCEPT !.on = TRUE]).meta, !.caller = ([draft EXCEPT !.on = TRUE]).caller]
                          /\ page' = [page EXCEPT ![self] = PageView(draft')]
                          /\ IF late.owner = self
                                THEN /\ overtaken' = TRUE
                                ELSE /\ TRUE
                                     /\ UNCHANGED overtaken
                          /\ late' = late
                       \/ /\ OutOfOrder /\ late.owner = 0
                          /\ late' = [owner |-> self, on |-> ([draft EXCEPT !.on = TRUE]).on, chain |-> TRUE]
                          /\ draft' = [draft EXCEPT !.meta = ([draft EXCEPT !.on = TRUE]).meta, !.caller = ([draft EXCEPT !.on = TRUE]).caller]
                          /\ UNCHANGED <<page, overtaken>>
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, recs, q, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ NoticeWrites /\ page[self].sticky = "off" /\ ~page[self].on /\ recs = <<>>
                    /\ ~DraftWriteGuard \/ draft.sticky = "off"
                    /\ ~(SerialWrites /\ NoticeInChain /\ late.owner = self /\ late.chain)
                    /\ \/ /\ draft' = [draft EXCEPT !.on = ([draft EXCEPT !.on = TRUE]).on, !.meta = ([draft EXCEPT !.on = TRUE]).meta, !.caller = ([draft EXCEPT !.on = TRUE]).caller]
                          /\ page' = [page EXCEPT ![self] = PageView(draft')]
                          /\ IF late.owner = self
                                THEN /\ overtaken' = TRUE
                                ELSE /\ TRUE
                                     /\ UNCHANGED overtaken
                          /\ late' = late
                       \/ /\ OutOfOrder /\ late.owner = 0
                          /\ late' = [owner |-> self, on |-> ([draft EXCEPT !.on = TRUE]).on, chain |-> NoticeInChain]
                          /\ draft' = [draft EXCEPT !.meta = ([draft EXCEPT !.on = TRUE]).meta, !.caller = ([draft EXCEPT !.on = TRUE]).caller]
                          /\ UNCHANGED <<page, overtaken>>
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, recs, q, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ page[self].on
                    /\ ~(SerialWrites /\ TRUE /\ late.owner = self /\ late.chain)
                    /\ \/ /\ draft' = [draft EXCEPT !.on = ([draft EXCEPT !.on = FALSE]).on, !.meta = ([draft EXCEPT !.on = FALSE]).meta, !.caller = ([draft EXCEPT !.on = FALSE]).caller]
                          /\ page' = [page EXCEPT ![self] = PageView(draft')]
                          /\ IF late.owner = self
                                THEN /\ overtaken' = TRUE
                                ELSE /\ TRUE
                                     /\ UNCHANGED overtaken
                          /\ late' = late
                       \/ /\ OutOfOrder /\ late.owner = 0
                          /\ late' = [owner |-> self, on |-> ([draft EXCEPT !.on = FALSE]).on, chain |-> TRUE]
                          /\ draft' = [draft EXCEPT !.meta = ([draft EXCEPT !.on = FALSE]).meta, !.caller = ([draft EXCEPT !.on = FALSE]).caller]
                          /\ UNCHANGED <<page, overtaken>>
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, recs, q, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ \E s \in {"off", "user"}:
                         /\ draft' = [draft EXCEPT !.sticky = s,
                                                   !.on = IF (DraftWriteGuard \/ PageClearsOn) /\ s # "off" THEN FALSE ELSE @]
                         /\ page' = [page EXCEPT ![self] = [page[self] EXCEPT !.sticky = s,
                                                                              !.on = IF (DraftWriteGuard \/ PageClearsOn) /\ s # "off" THEN FALSE ELSE @]]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, recs, q, stored, frozen, hist, busy, first, turns, late, overtaken, sugg>>
                 \/ /\ page[self].on
                    /\ \E m \in Pairsets:
                         LET o == IF WholeObject THEN page[self].on ELSE draft.on IN
                           /\ ~(SerialWrites /\ TRUE /\ late.owner = self /\ late.chain)
                           /\ \/ /\ draft' = [draft EXCEPT !.on = ([draft EXCEPT !.meta = m, !.on = o]).on, !.meta = ([draft EXCEPT !.meta = m, !.on = o]).meta, !.caller = ([draft EXCEPT !.meta = m, !.on = o]).caller]
                                 /\ page' = [page EXCEPT ![self] = PageView(draft')]
                                 /\ IF late.owner = self
                                       THEN /\ overtaken' = TRUE
                                       ELSE /\ TRUE
                                            /\ UNCHANGED overtaken
                                 /\ late' = late
                              \/ /\ OutOfOrder /\ late.owner = 0
                                 /\ late' = [owner |-> self, on |-> ([draft EXCEPT !.meta = m, !.on = o]).on, chain |-> TRUE]
                                 /\ draft' = [draft EXCEPT !.meta = ([draft EXCEPT !.meta = m, !.on = o]).meta, !.caller = ([draft EXCEPT !.meta = m, !.on = o]).caller]
                                 /\ UNCHANGED <<page, overtaken>>
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, recs, q, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ page[self].on
                    /\ \E c \in Callers:
                         LET o == IF WholeObject THEN page[self].on ELSE draft.on IN
                           /\ ~(SerialWrites /\ TRUE /\ late.owner = self /\ late.chain)
                           /\ \/ /\ draft' = [draft EXCEPT !.on = ([draft EXCEPT !.caller = c, !.on = o]).on, !.meta = ([draft EXCEPT !.caller = c, !.on = o]).meta, !.caller = ([draft EXCEPT !.caller = c, !.on = o]).caller]
                                 /\ page' = [page EXCEPT ![self] = PageView(draft')]
                                 /\ IF late.owner = self
                                       THEN /\ overtaken' = TRUE
                                       ELSE /\ TRUE
                                            /\ UNCHANGED overtaken
                                 /\ late' = late
                              \/ /\ OutOfOrder /\ late.owner = 0
                                 /\ late' = [owner |-> self, on |-> ([draft EXCEPT !.caller = c, !.on = o]).on, chain |-> TRUE]
                                 /\ draft' = [draft EXCEPT !.meta = ([draft EXCEPT !.caller = c, !.on = o]).meta, !.caller = ([draft EXCEPT !.caller = c, !.on = o]).caller]
                                 /\ UNCHANGED <<page, overtaken>>
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, recs, q, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ late.owner = self
                    /\ draft' = [draft EXCEPT !.on = late.on]
                    /\ page' = [page EXCEPT ![self] = PageView(draft')]
                    /\ late' = NoWrite
                    /\ overtaken' = FALSE
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, recs, q, stored, frozen, hist, busy, first, turns, sugg>>
                 \/ /\ MetaVarEdits
                    /\ LET u == ~draft.usesMetaVar IN
                         /\ draft' = [draft EXCEPT !.usesMetaVar = u]
                         /\ page' = [page EXCEPT ![self].usesMetaVar = u]
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<chat, recs, q, stored, frozen, hist, busy, first, turns, late, overtaken, sugg>>
                 \/ /\ ~busy /\ recs # <<>> /\ q = <<>> /\ pc[Bridge] = "BRun" /\ chat < MaxRestarts
                    /\ chat' = chat + 1
                    /\ recs' = <<>>
                    /\ stored' = Stored0
                    /\ frozen' = Frozen0
                    /\ hist' = <<>>
                    /\ first' = {}
                    /\ turns' = {}
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<draft, page, q, busy, late, overtaken, sugg>>
                 \/ /\ "delete" \in QueueEdits /\ q # <<>>
                    /\ \E i \in 1..Len(q):
                         q' = SubSeq(q, 1, i - 1) \o SubSeq(q, i + 1, Len(q))
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<draft, page, chat, recs, stored, frozen, hist, busy, first, turns, late, overtaken, sugg>>
                 \/ /\ "reorder" \in QueueEdits /\ Len(q) >= 2
                    /\ q' = <<q[2], q[1]>> \o SubSeq(q, 3, Len(q))
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<draft, page, chat, recs, stored, frozen, hist, busy, first, turns, late, overtaken, sugg>>
                 \/ /\ "steer" \in QueueEdits /\ q # <<>> /\ pc[Bridge] = "BShadows"
                    /\ turns' = (turns \cup Columns(chat, Head(q), recs[Head(q)], renv))
                    /\ q' = Tail(q)
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<draft, page, chat, recs, stored, frozen, hist, busy, first, late, overtaken, sugg>>
                 \/ /\ GenericPrompt /\ Len(recs) < MaxMsgs
                    /\ IF ~PromptRefused
                          THEN /\ q' = Append(q, Len(recs) + 1)
                               /\ recs' = Append(recs, GenericRecord(draft))
                          ELSE /\ TRUE
                               /\ UNCHANGED << recs, q >>
                    /\ pc' = [pc EXCEPT ![self] = "Loop"]
                    /\ UNCHANGED <<draft, page, chat, stored, frozen, hist, busy, first, turns, late, overtaken, sugg>>
                 \/ /\ ~busy /\ Len(recs) < MaxMsgs /\ ~EarlyRefused(draft, frozen)
                    /\ busy' = TRUE
                    /\ sugg' = [sugg EXCEPT ![self] = page[self].usesMetaVar /\ ~page[self].on /\ recs = <<>> /\ page[self].sticky = "off"]
                    /\ pc' = [pc EXCEPT ![self] = "Capture"]
                    /\ UNCHANGED <<draft, page, chat, recs, q, stored, frozen, hist, first, turns, late, overtaken>>
              /\ UNCHANGED << running, renv >>

Capture(self) == /\ pc[self] = "Capture"
                 /\ ~ClashBarred(Captured(draft, frozen), frozen)
                 /\ LET v == Captured(draft, frozen) IN
                      LET i == Len(recs) + 1 IN
                        IF ~Refused(v, frozen)
                           THEN /\ recs' = Append(recs, Record(v, draft, sugg[self]))
                                /\ q' = Append(q, i)
                                /\ frozen' = IF frozen.set THEN frozen ELSE [set |-> TRUE, on |-> v.on, meta |-> v.meta]
                           ELSE /\ TRUE
                                /\ UNCHANGED << recs, q, frozen >>
                 /\ busy' = FALSE
                 /\ sugg' = [sugg EXCEPT ![self] = FALSE]
                 /\ pc' = [pc EXCEPT ![self] = "Loop"]
                 /\ UNCHANGED << draft, page, chat, stored, hist, running, 
                                 renv, first, turns, late, overtaken >>

editor(self) == Loop(self) \/ Capture(self)

BRun == /\ pc[Bridge] = "BRun"
        /\ q # <<>>
        /\ LET i == Head(q) IN
             LET r == recs[Head(q)] IN
               \E e \in BridgeEnvs(stored, recs[Head(q)].v):
                 LET h == IF recs[Head(q)].v.on THEN Append(hist, recs[Head(q)].entered) ELSE hist IN
                   /\ running' = i
                   /\ renv' = e
                   /\ hist' = h
                   /\ turns' = {MkTurn(chat, i, "A", r, e)}
                   /\ IF first = {}
                         THEN /\ first' = {MkTurn(chat, i, "A", r, e)}
                         ELSE /\ TRUE
                              /\ first' = first
                   /\ stored' = NewStored(stored, r.v, e)
                   /\ q' = Tail(q)
        /\ pc' = [pc EXCEPT ![Bridge] = "BShadows"]
        /\ UNCHANGED << draft, page, chat, recs, frozen, busy, late, overtaken, 
                        sugg >>

BShadows == /\ pc[Bridge] = "BShadows"
            /\ turns' = (turns \cup Columns(chat, running, recs[running], renv))
            /\ running' = 0
            /\ renv' = NoEnv
            /\ pc' = [pc EXCEPT ![Bridge] = "BRun"]
            /\ UNCHANGED << draft, page, chat, recs, q, stored, frozen, hist, 
                            busy, first, late, overtaken, sugg >>

bridge == BRun \/ BShadows

Next == bridge
           \/ (\E self \in Editors: editor(self))

Spec == Init /\ [][Next]_vars

\* END TRANSLATION
=============================================================================
