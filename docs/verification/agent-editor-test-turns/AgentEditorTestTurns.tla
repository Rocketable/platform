------------------------ MODULE AgentEditorTestTurns ------------------------
(* Test turns in the Agent Editor's draft test chat.                         *)
(* Columns: "A" = the parent conversation running the draft's first variant, *)
(* "B" = variant shadow <name>/b, "L" = live shadow running the saved file.  *)
(* Agent content is abstracted to a version number. Switches (constants)     *)
(* select the code as built or an earlier/rejected mechanism; README.md maps *)
(* each check to its switches.                                               *)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS
  MaxMsgs,          \* composer sends, over all chats
  MaxEdits,         \* teammates' draft edits (committed at once), any variant
  MaxSaves,         \* saves of the live agent file
  MaxToggles,       \* add/remove variant B in the draft
  MaxRestarts,      \* Restart or Restart & replay presses
  CaptureAt,        \* when draft columns fix their version: "send" | "parentStart" | "run"
  LiveCaptured,     \* TRUE: live file bytes captured with the draft; FALSE: live shadow reads disk when it runs
  LabelFromRecord,  \* TRUE: label = the message's record written at send; FALSE: label = what the loader used
  RestartNewConv,   \* TRUE: Restart makes a new conversation; FALSE: clears the same conversation in place
  KeyById,          \* TRUE: answers placed by message id; FALSE: by position in the column
  LayerOnRoster,    \* TRUE: shadow roster from the capture; FALSE: from disk
  SaveAtomic,       \* TRUE: the live definition is one file read whole; FALSE: two files saved one by one
  ShadowOwnFraming, \* TRUE: each column frames the prompt with its own definition; FALSE: the parent's framing
  MaxJobs,          \* background jobs turns of A may ask for
  SubJobs,          \* TRUE: a job may belong to a subagent session of A's turn
  WakeFix,          \* later turns: "none" (pre-fix: disk) | "copy" | "copyFanOut" | "inert" (as built)
  PageWakeAOnly,    \* TRUE: a row without a record shows in column A only; FALSE: shadow cells say "Waiting…"
  MaxTyped,         \* keystrokes this editor types (held until its op commits)
  Flush,            \* what waits for this editor's pending edits: "none" | "send" (Send only) | "all"
  ReplayFrom,       \* Restart & replay's texts: "started" (turns started) | "cached" (streamed turns + polled
                    \* queue) | "fresh" (read queue, then History) | "records" (the page reads the chat's
                    \* records) | "server" (the restart transaction reads them under the per-draft lock)
  StashInTx,        \* TRUE: queue stash and record commit together; FALSE: the stash commits first, the record
                    \* at the end of SendAgentDraft's transaction (both under the per-draft lock)
  MaxRecordFailures, \* SendAgentDraft transactions that fail after their stash (record insert or commit)
  MaxRefusals,      \* Restart & replay sends refused by the draft's checks
  RefuseSwitches,   \* TRUE (pre-fix): the new chat is made and bound before the checks, so a refusal leaves
                    \* the draft on it; FALSE (as built): it is made only after every check passed
  MaxTeamSends,     \* sends from a teammate's page, which this page's buttons do not wait for
  RestartWhileSending, \* TRUE: Restart can be pressed while a Send is pending or not yet re-read
  SendWhileRestarting  \* TRUE: Send can be pressed while a Restart is in progress

ASSUME CaptureAt \in {"send", "parentStart", "run"}
ASSUME WakeFix \in {"none", "copy", "copyFanOut", "inert"}
ASSUME Flush \in {"none", "send", "all"}
ASSUME ReplayFrom \in {"started", "cached", "fresh", "records", "server"}

Cols == {"A", "B", "L"}
Drafts == {"A", "B"}
Epochs == 1..(MaxRestarts + 1)
NoSend == [on |-> FALSE, t |-> 0, seen |-> [c \in Drafts |-> 0]]
NoOpen == [on |-> FALSE, e |-> 1, k |-> 0]
NoRestart == [on |-> FALSE, replay |-> FALSE, phase |-> "ready", q |-> {}, h |-> 0, texts |-> <<>>,
              seen |-> [c \in Drafts |-> 0]]
\* The set S of message numbers in send order.
AscSeq(S) == [k \in 1..Cardinality(S) |-> CHOOSE x \in S : Cardinality({y \in S : y < x}) = k - 1]

(* --algorithm AgentEditorTestTurns {
variables
  epoch = 1,                         \* the chat the page shows (one conversation per chat)
  rev = [c \in Drafts |-> 0],        \* draft revision per draft column (server)
  live = <<0, 0>>,                   \* saved file versions: <<agent file, second file>>
  saving = FALSE,
  hasB = TRUE, diskHasB = TRUE,      \* variant B in the draft / on disk
  edits = 0, saves = 0, toggles = 0, restarts = 0, sends = 0,
  tall = [c \in Drafts |-> 0],       \* keystrokes this editor typed, per variant
  tin = [c \in Drafts |-> 0],        \* of those, committed on the server
  psend = NoSend,                    \* a pressed Send waiting to reach the server
  prest = NoRestart,                 \* a pressed Restart (& replay) waiting to reach the server
  refresh = FALSE,                   \* a stored Send whose draft and queue the page has not re-read yet
  streamed = 0,                      \* started turns of the shown chat its live stream already shows
  qcache = {},                       \* the shown chat's queued messages as of the page's last queue read
  hist = {},                         \* <<e, m>> whose user text History shows (from the turn's first checkpoint)
  team = 0,                          \* teammates' sends so far
  norec = {},                        \* <<e, m>> stashed whose record never committed (failed transaction)
  fails = 0, refusals = 0,
  open = NoOpen,                     \* a SendAgentDraft holding the per-draft lock: its last k messages of chat e
                                     \* are stashed (claimable) but their records are not committed yet
  sent = [e \in Epochs |-> <<>>],    \* stored user messages per chat
  rep = [e \in Epochs |-> -1],       \* messages Restart & replay re-sent into chat e (-1: not a replay)
  queue = [c \in Cols |-> <<>>],     \* admitted turns per column, FIFO
  started = {},                      \* <<e, m>> whose parent turn has started
  ans = [c \in Cols |-> <<>>],       \* answers to user messages per column
  jobs = <<>>,                       \* background jobs and Completion Notes
  njobs = 0, nfin = 0,
  acap = <<-1, -1>>,                 \* agent files A's running turn carries (its subagents' lineage)
  wans = <<>>;                       \* later (wake) turns: of A, or of a subagent session

define {
  NoCap == <<-1, -1>>
  Buf(c) == tall[c] - tin[c]
  Drained == \A c \in Drafts: Buf(c) = 0
  CurVer(c) == IF c = "L" THEN live ELSE <<rev[c], rev[c]>>
  Capture(c) == IF c = "L" /\ ~LiveCaptured THEN NoCap ELSE CurVer(c)
  \* The saved agent file as A loads it from disk. The draft starts as a copy of saved version 0
  \* (draft revision 0); later saves get ids 10+ to keep them apart from draft revisions.
  DiskA == IF live = <<0, 0>> THEN <<0, 0>> ELSE <<live[1] + 10, live[2] + 10>>
  RosterB == IF LayerOnRoster THEN hasB ELSE diskHasB
  \* One message as SendAgentDraft stores it: queue item + agent_draft_messages record, from one capture.
  \* seen = this editor's keystrokes when the button was pressed.
  NewMsg(t, seen) ==
    LET cols == {"A", "L"} \cup (IF RosterB THEN {"B"} ELSE {}) IN
    [text |-> t, cols |-> cols, wantB |-> hasB,
     sv |-> [c \in Cols |-> CurVer(c)],
     typedOk |-> \A c \in cols \cap Drafts: tin[c] >= seen[c],
     cap |-> [c \in Cols |-> IF CaptureAt = "send" THEN Capture(c) ELSE NoCap],
     aframe |-> <<"none", -1>>]
  \* The page lists a message once its parent turn started; queued ones show only as "Waiting its turn".
  Shown(e) == Cardinality({i \in 1..Len(sent[e]) : <<e, i>> \in started})
  \* Stored messages of chat e whose turn has not started: the server's queue.
  Queued(e) == {i \in 1..Len(sent[e]) : <<e, i>> \notin started}
  Texts(L) == [k \in 1..Len(L) |-> sent[epoch][L[k]].text]
  \* Messages of chat e whose user text History shows. A claimed turn has left the queue at its start,
  \* but reaches History only at the looper's first checkpoint.
  InHistory(e) == Cardinality({i \in 1..Len(sent[e]) : <<e, i>> \in hist})
  \* Messages of chat e whose agent_draft_messages record has committed, in seq order.
  RecIdx(e) == SelectSeq([i \in 1..Len(sent[e]) |-> i],
                 LAMBDA i: <<e, i>> \notin norec /\ ~(open.on /\ open.e = e /\ i > Len(sent[e]) - open.k))
  \* What a SendAgentDraft that stores k messages into chat e leaves open after its stash.
  Opened(e, k) == IF StashInTx \/ k = 0 THEN NoOpen ELSE [on |-> TRUE, e |-> e, k |-> k]
  ACap(h) == IF CaptureAt = "send" THEN sent[h.e][h.m].cap["A"] ELSE CurVer("A")
  Idx(msgs, c) == SelectSeq([i \in 1..Len(msgs) |-> i], LAMBDA i: c \in msgs[i].cols)
  Entries(e, msgs, c) == [k \in 1..Len(Idx(msgs, c)) |-> [e |-> e, m |-> Idx(msgs, c)[k]]]
  \* Completion Notes waiting for A: its own jobs' and its subagents' reports.
  Notes == {j \in DOMAIN jobs : jobs[j].st = "done" /\ jobs[j].kind # "sub"}
  Deliver(js) == [j \in DOMAIN jobs |-> IF j \in js THEN [jobs[j] EXCEPT !.st = "delivered"] ELSE jobs[j]]

  \* The definition each column ran is the one current when its message was sent (stored).
  VersionAtSendDraft == \A c \in Drafts: \A i \in 1..Len(ans[c]):
                          ans[c][i].used = sent[ans[c][i].e][ans[c][i].m].sv[c]
  VersionAtSendLive == \A i \in 1..Len(ans["L"]):
                          ans["L"][i].used = sent[ans["L"][i].e][ans["L"][i].m].sv["L"]
  \* The label shown is the version actually loaded, and one real version (all files from it).
  LabelTruthful == \A c \in Cols: \A i \in 1..Len(ans[c]):
                     ans[c][i].used = ans[c][i].label /\ ans[c][i].label[1] = ans[c][i].label[2]
  \* In the chat shown, each column answers each message at most once, in send order, placed by id.
  ChatAns(c) == SelectSeq(ans[c], LAMBDA a: a.tag = epoch)
  AlignedPerMessage == \A c \in Cols: LET s == ChatAns(c) IN
                         \A i \in 1..Len(s): (KeyById \/ s[i].m = i)
                                             /\ \A j \in (i + 1)..Len(s): s[i].m < s[j].m
  \* An answer shows only in the chat its message was sent in.
  RestartIsolation == \A c \in Cols: \A i \in 1..Len(ans[c]): ans[c][i].tag = ans[c][i].e
  \* Restart & replay re-sends every message of the chat it replaces, in order (the button's words:
  \* "gets every earlier message again").
  ReplayAll == \A e \in Epochs \ {1}: rep[e] >= 0 =>
                  /\ rep[e] = Len(sent[e - 1])
                  /\ \A i \in 1..rep[e]: sent[e][i].text = sent[e - 1][i].text
  \* One message goes to every variant the draft has when it is stored.
  RosterMatchesDraft == \A e \in Epochs: \A i \in 1..Len(sent[e]):
                          ("B" \in sent[e][i].cols) = sent[e][i].wantB
  \* The prompt framing (additionalInstructions, easy mode) comes from the column's own definition.
  FramingOwn == \A c \in Cols: \A i \in 1..Len(ans[c]): ans[c][i].frame = <<c, ans[c][i].used[1]>>
  \* A later turn of A runs the agent files of the message whose work it continues.
  WakeUsesCapture == \A i \in 1..Len(wans): wans[i].who = "A" => wans[i].used \in wans[i].want
  \* Same for a subagent session's later turn.
  SubWakeUsesCapture == \A i \in 1..Len(wans): wans[i].who = "sub" => wans[i].used \in wans[i].want
  \* One later turn continues one message: its notes all come from turns with one capture.
  WakeOneCapture == \A i \in 1..Len(wans): wans[i].who = "A" => Cardinality(wans[i].want) = 1
  \* Shadow columns never run a later turn of A (they never ran its job).
  NoShadowWakeTurn == \A i \in 1..Len(wans): ~wans[i].fan
  \* A row never shows "Waiting…" in a shadow column that will never answer it.
  WakeRowHonest == \A i \in 1..Len(wans): wans[i].who = "A" => PageWakeAOnly \/ wans[i].fan
  \* A message runs every keystroke this editor showed when Send or Restart & replay was pressed.
  SendIncludesTyped == \A e \in Epochs: \A i \in 1..Len(sent[e]): sent[e][i].typedOk
  \* Reachability canary (must be violated): a pressed send waits while keystrokes are pending.
  NeverWaitsForKeystrokes == ~((psend.on \/ prest.on) /\ ~Drained)
  \* Liveness.
  Sent(e, m, c) == m \in 1..Len(sent[e]) /\ c \in sent[e][m].cols
  Answered(e, m, c) == \E i \in 1..Len(ans[c]): ans[c][i].e = e /\ ans[c][i].m = m
  \* Sent and Answered never become false again, so "every Sent(e, m, c) leads to Answered(e, m, c)"
  \* is the same as: eventually every sent message stays answered by every column it was sent to.
  EveryColumnAnswers == <>[](\A e \in Epochs, m \in 1..MaxMsgs, c \in Cols: Sent(e, m, c) => Answered(e, m, c))
  \* A pressed Send or Restart always reaches the server (P ~> ~P is []<>~P).
  PressedSendGoesOut == []<>~psend.on /\ []<>~prest.on
} \* end define

\* The people at the page: this editor, and teammates whose edits and saves commit at once.
process (User = "U") {
  U: while (TRUE) {
    either { \* press Send (the composer takes no second Send until the reply)
      await sends < MaxMsgs /\ ~psend.on /\ (SendWhileRestarting \/ ~prest.on);
      psend := [on |-> TRUE, t |-> sends + 1, seen |-> tall];
      sends := sends + 1;
    } or { \* a teammate edits one variant
      await edits < MaxEdits;
      with (c \in IF hasB THEN Drafts ELSE {"A"}) { rev[c] := rev[c] + 1; };
      edits := edits + 1;
    } or { \* this editor types: the edit waits in the co-editing queue or as a File tab's unsent text
      await tall["A"] + tall["B"] < MaxTyped;
      with (c \in IF hasB THEN Drafts ELSE {"A"}) { tall[c] := tall[c] + 1; };
    } or { \* save the live file (atomic, or first file of a non-atomic save)
      await saves < MaxSaves /\ ~saving;
      if (SaveAtomic) {
        live := <<live[1] + 1, live[2] + 1>>; saves := saves + 1; diskHasB := hasB;
      } else {
        live[1] := live[1] + 1; saving := TRUE;
      }
    } or { \* second file of a non-atomic save
      await saving;
      live[2] := live[2] + 1; saving := FALSE; saves := saves + 1; diskHasB := hasB;
    } or { \* add or remove variant B (never while this editor holds unsent keystrokes for it)
      await toggles < MaxToggles /\ (hasB => Buf("B") = 0);
      hasB := ~hasB; toggles := toggles + 1;
    } or { \* a teammate's page sends: SendAgentDraft in the chat the server has then; this page's
           \* buttons do not wait for it (their keystrokes are theirs: nothing of this editor's is owed)
      await team < MaxTeamSends /\ ~open.on;
      with (i = Len(sent[epoch]) + 1, msg = NewMsg(100 + team, [c \in Drafts |-> 0])) {
        sent[epoch] := Append(sent[epoch], msg);
        queue := [c \in Cols |-> IF c \in msg.cols THEN Append(queue[c], [e |-> epoch, m |-> i]) ELSE queue[c]];
        open := Opened(epoch, 1);
      };
      team := team + 1;
    } or { \* press Restart, or Restart & replay ("started"/"cached": texts from what the page holds now;
           \* "fresh": the page reads the queue, then History; "records": the page reads the chat's records)
      await restarts < MaxRestarts /\ ~prest.on /\ (RestartWhileSending \/ (~psend.on /\ ~refresh));
      with (rp \in BOOLEAN) {
        prest := [on |-> TRUE, replay |-> rp, q |-> {}, h |-> 0,
                  phase |-> IF rp /\ ReplayFrom \in {"fresh", "records"} THEN "click" ELSE "ready",
                  texts |-> IF ~rp THEN <<>>
                            ELSE IF ReplayFrom = "started" THEN Texts([i \in 1..Shown(epoch) |-> i])
                            ELSE IF ReplayFrom = "cached" THEN Texts(AscSeq((1..streamed) \cup qcache))
                            ELSE <<>>,
                  seen |-> tall];
      };
      restarts := restarts + 1;
    }
  }
}

\* This editor's page: its co-editing client and its requests. Both always make progress.
fair process (Client = "W") {
  W: while (TRUE) {
    either { \* pending edits of one variant commit as one op (200 ms pause, or drained() at once)
      with (c \in {x \in Drafts : Buf(x) > 0}) { rev[c] := rev[c] + 1; tin[c] := tall[c]; };
    } or { \* the Send request reaches SendAgentDraft (per-draft lock): one capture, the queue item, the record
      await psend.on /\ ~open.on /\ (Flush # "none" => Drained);
      with (i = Len(sent[epoch]) + 1, msg = NewMsg(psend.t, psend.seen)) {
        sent[epoch] := Append(sent[epoch], msg);
        queue := [c \in Cols |-> IF c \in msg.cols THEN Append(queue[c], [e |-> epoch, m |-> i]) ELSE queue[c]];
        open := Opened(epoch, 1);
      };
      psend := NoSend; refresh := ~RestartWhileSending;
    } or { \* SendAgentDraft's transaction commits its records and releases the per-draft lock
      await open.on;
      open := NoOpen;
    } or { \* ... or fails after the stash (record insert or commit): the stashed messages stay queued and
           \* run, with no record
      await open.on /\ fails < MaxRecordFailures;
      norec := norec \cup {<<open.e, i>> : i \in (Len(sent[open.e]) - open.k + 1)..Len(sent[open.e])};
      open := NoOpen; fails := fails + 1;
    } or { \* the page re-reads the queue (after a Send, and every 2 s)
      await refresh \/ (ReplayFrom = "cached" /\ qcache # Queued(epoch));
      qcache := IF ReplayFrom = "cached" THEN Queued(epoch) ELSE {}; refresh := FALSE;
    } or { \* the live stream shows the next started turn (it lags the server)
      await ReplayFrom = "cached" /\ streamed < Shown(epoch);
      streamed := streamed + 1;
    } or { \* the page's fresh reads, one request each: "records" reads the committed agent_draft_messages in
           \* seq order; "fresh" reads the queue, then History
      await prest.on /\ prest.phase # "ready";
      if (ReplayFrom = "records") {
        prest := [prest EXCEPT !.phase = "ready", !.texts = Texts(RecIdx(epoch))];
      } else if (prest.phase = "click") {
        with (q = Queued(epoch)) {
          prest := [prest EXCEPT !.q = q, !.phase = "half"];
        }
      } else {
        with (h = InHistory(epoch)) {
          prest := [prest EXCEPT !.h = h, !.phase = "ready", !.texts = Texts(AscSeq((1..h) \cup prest.q))];
        }
      }
    } or { \* the restart request: SendAgentDraft with Restart (per-draft lock), a new conversation, then the
           \* replayed texts under one capture; "server": the texts are the old chat's records, read in this
           \* transaction (no other SendAgentDraft is open, so every earlier message has its record)
      await prest.on /\ prest.phase = "ready" /\ ~open.on /\ (Flush = "all" => Drained);
      with (ne = epoch + 1,
            texts = IF ReplayFrom = "server" /\ prest.replay THEN Texts(RecIdx(epoch)) ELSE prest.texts,
            \* the draft's checks may refuse a send with texts (none with no texts)
            refused \in IF texts # <<>> /\ refusals < MaxRefusals THEN BOOLEAN ELSE {FALSE},
            msgs = IF refused THEN <<>> ELSE [i \in 1..Len(texts) |-> NewMsg(texts[i], prest.seen)]) {
        refusals := refusals + (IF refused THEN 1 ELSE 0);
        if (~refused \/ RefuseSwitches) {
          sent[ne] := msgs;
          rep[ne] := IF prest.replay THEN Len(msgs) ELSE -1;
          queue := [c \in Cols |-> queue[c] \o Entries(ne, msgs, c)];
          open := Opened(ne, Len(msgs));
          epoch := ne;
        }
      };
      prest := NoRestart; streamed := 0; qcache := {};
    }
  }
}

\* Background jobs finish on their own time; a subagent session's job wakes that session.
process (Jobs = "J") {
  J: while (TRUE) {
    either { \* a running job finishes and leaves its Completion Note
      with (j \in {k \in DOMAIN jobs : jobs[k].st = "run"}) {
        jobs[j] := [jobs[j] EXCEPT !.st = "done", !.fin = nfin + 1];
        nfin := nfin + 1;
      }
    } or { \* the subagent session's wake turn takes the agent files from its lineage, the top-level
           \* session's running turn (none when idle); its answer reports to A as a note with origin '{}'
      await WakeFix # "inert";
      with (j \in {k \in DOMAIN jobs : jobs[k].st = "done" /\ jobs[k].kind = "sub"}) {
        wans := Append(wans, [who |-> "sub", e |-> jobs[j].e, used |-> IF acap # NoCap THEN acap ELSE DiskA,
                              want |-> {jobs[j].mcap}, fan |-> FALSE]);
        jobs := Append([jobs EXCEPT ![j].st = "delivered"],
                       [e |-> jobs[j].e, kind |-> "report", st |-> "done", fin |-> nfin + 1, ocap |-> NoCap, mcap |-> jobs[j].mcap]);
        nfin := nfin + 1;
      }
    }
  }
}

fair process (Col \in Cols)
variables cur = [e |-> 0, m |-> 0], used = NoCap, frame = <<"none", -1>>,
          want = {}, wmcap = NoCap, fan = FALSE;
{
  Claim: while (TRUE) {
    either { \* a user message's turn
      await queue[self] # <<>>;
      with (h = Head(queue[self])) {
        \* A shadow claims its manifest only after the parent's turn captured its input.
        await self = "A" \/ <<h.e, h.m>> \in started;
        if (self = "A") {
          used := ACap(h);
          frame := <<"A", ACap(h)[1]>>;
          started := started \cup {<<h.e, h.m>>};
          acap := ACap(h);
          if (CaptureAt = "parentStart") {
            sent[h.e][h.m].cap := [c \in Cols |-> Capture(c)] || sent[h.e][h.m].aframe := <<"A", CurVer("A")[1]>>;
          } else {
            sent[h.e][h.m].aframe := <<"A", ACap(h)[1]>>;
          }
        } else {
          used := IF sent[h.e][h.m].cap[self] # NoCap THEN sent[h.e][h.m].cap[self] ELSE CurVer(self);
          frame := IF ShadowOwnFraming
                   THEN <<self, (IF sent[h.e][h.m].cap[self] # NoCap THEN sent[h.e][h.m].cap[self] ELSE CurVer(self))[1]>>
                   ELSE sent[h.e][h.m].aframe;
        };
        cur := h;
        queue[self] := Tail(queue[self]);
      };
    } or { \* A's wake turn: claimWakeNotes takes every pending note of the conversation
      await self = "A" /\ WakeFix \in {"none", "copy", "copyFanOut"} /\ Notes # {};
      with (e \in {jobs[j].e : j \in Notes}) {
        with (b = {j \in Notes : jobs[j].e = e}) {
          with (f = CHOOSE j \in b : \A k \in b : jobs[j].fin <= jobs[k].fin) {
            \* Pre-fix the wake inbound's Metadata holds only background_job_ids: no agent files.
            \* "copy": copy the agent-file keys from the earliest note's origin (the job's turn inbound).
            with (oc = IF WakeFix = "none" THEN NoCap ELSE jobs[f].ocap) {
              used := IF oc # NoCap THEN oc ELSE DiskA;
              frame := <<"A", (IF oc # NoCap THEN oc ELSE DiskA)[1]>>;
              acap := oc;
              want := {jobs[j].mcap : j \in b};
              wmcap := jobs[f].mcap;
              \* startShadowManifests fans out whenever the inbound carries agent files.
              fan := WakeFix = "copyFanOut" /\ oc # NoCap;
              jobs := Deliver(b);
              cur := [e |-> e, m |-> 0];
            }
          }
        }
      }
    };
  Finish:
    if (cur.m > 0) {
      \* A's user text reaches History at its first checkpoint; put here at the turn's end, the latest it
      \* can be (only the "fresh" replay reads History, and a later checkpoint only widens its gap).
      if (self = "A") { hist := hist \cup {<<cur.e, cur.m>>}; };
      ans[self] := Append(ans[self],
                     [e |-> cur.e, m |-> cur.m, used |-> used, frame |-> frame,
                      label |-> IF LabelFromRecord THEN sent[cur.e][cur.m].sv[self] ELSE used,
                      tag |-> IF RestartNewConv THEN cur.e ELSE epoch]);
    } else {
      \* No agent_draft_messages record exists for a wake turn's input: its row has no label.
      wans := Append(wans, [who |-> "A", e |-> cur.e, used |-> used, want |-> want, fan |-> fan]);
    };
    if (self = "A") {
      \* Shadows always run background work inline; as built ("inert") a draft test turn does too,
      \* and loses the tools that start a later turn. Pre-fix A could leave a job running.
      either { skip } or {
        await njobs < MaxJobs /\ WakeFix # "inert";
        with (k \in IF SubJobs THEN {"own", "sub"} ELSE {"own"}) {
          jobs := Append(jobs, [e |-> cur.e, kind |-> k, st |-> "run", fin |-> 0,
                                ocap |-> IF k = "own" THEN acap ELSE NoCap,
                                mcap |-> IF cur.m > 0 THEN sent[cur.e][cur.m].sv["A"] ELSE wmcap]);
        };
        njobs := njobs + 1;
      };
      acap := NoCap; want := {}; wmcap := NoCap; fan := FALSE;
    }
  }
}
} *)
\* BEGIN TRANSLATION
VARIABLES pc, epoch, rev, live, saving, hasB, diskHasB, edits, saves, toggles, 
          restarts, sends, tall, tin, psend, prest, refresh, streamed, qcache, 
          hist, team, norec, fails, refusals, open, sent, rep, queue, started, 
          ans, jobs, njobs, nfin, acap, wans

(* define statement *)
NoCap == <<-1, -1>>
Buf(c) == tall[c] - tin[c]
Drained == \A c \in Drafts: Buf(c) = 0
CurVer(c) == IF c = "L" THEN live ELSE <<rev[c], rev[c]>>
Capture(c) == IF c = "L" /\ ~LiveCaptured THEN NoCap ELSE CurVer(c)


DiskA == IF live = <<0, 0>> THEN <<0, 0>> ELSE <<live[1] + 10, live[2] + 10>>
RosterB == IF LayerOnRoster THEN hasB ELSE diskHasB


NewMsg(t, seen) ==
  LET cols == {"A", "L"} \cup (IF RosterB THEN {"B"} ELSE {}) IN
  [text |-> t, cols |-> cols, wantB |-> hasB,
   sv |-> [c \in Cols |-> CurVer(c)],
   typedOk |-> \A c \in cols \cap Drafts: tin[c] >= seen[c],
   cap |-> [c \in Cols |-> IF CaptureAt = "send" THEN Capture(c) ELSE NoCap],
   aframe |-> <<"none", -1>>]

Shown(e) == Cardinality({i \in 1..Len(sent[e]) : <<e, i>> \in started})

Queued(e) == {i \in 1..Len(sent[e]) : <<e, i>> \notin started}
Texts(L) == [k \in 1..Len(L) |-> sent[epoch][L[k]].text]


InHistory(e) == Cardinality({i \in 1..Len(sent[e]) : <<e, i>> \in hist})

RecIdx(e) == SelectSeq([i \in 1..Len(sent[e]) |-> i],
               LAMBDA i: <<e, i>> \notin norec /\ ~(open.on /\ open.e = e /\ i > Len(sent[e]) - open.k))

Opened(e, k) == IF StashInTx \/ k = 0 THEN NoOpen ELSE [on |-> TRUE, e |-> e, k |-> k]
ACap(h) == IF CaptureAt = "send" THEN sent[h.e][h.m].cap["A"] ELSE CurVer("A")
Idx(msgs, c) == SelectSeq([i \in 1..Len(msgs) |-> i], LAMBDA i: c \in msgs[i].cols)
Entries(e, msgs, c) == [k \in 1..Len(Idx(msgs, c)) |-> [e |-> e, m |-> Idx(msgs, c)[k]]]

Notes == {j \in DOMAIN jobs : jobs[j].st = "done" /\ jobs[j].kind # "sub"}
Deliver(js) == [j \in DOMAIN jobs |-> IF j \in js THEN [jobs[j] EXCEPT !.st = "delivered"] ELSE jobs[j]]


VersionAtSendDraft == \A c \in Drafts: \A i \in 1..Len(ans[c]):
                        ans[c][i].used = sent[ans[c][i].e][ans[c][i].m].sv[c]
VersionAtSendLive == \A i \in 1..Len(ans["L"]):
                        ans["L"][i].used = sent[ans["L"][i].e][ans["L"][i].m].sv["L"]

LabelTruthful == \A c \in Cols: \A i \in 1..Len(ans[c]):
                   ans[c][i].used = ans[c][i].label /\ ans[c][i].label[1] = ans[c][i].label[2]

ChatAns(c) == SelectSeq(ans[c], LAMBDA a: a.tag = epoch)
AlignedPerMessage == \A c \in Cols: LET s == ChatAns(c) IN
                       \A i \in 1..Len(s): (KeyById \/ s[i].m = i)
                                           /\ \A j \in (i + 1)..Len(s): s[i].m < s[j].m

RestartIsolation == \A c \in Cols: \A i \in 1..Len(ans[c]): ans[c][i].tag = ans[c][i].e


ReplayAll == \A e \in Epochs \ {1}: rep[e] >= 0 =>
                /\ rep[e] = Len(sent[e - 1])
                /\ \A i \in 1..rep[e]: sent[e][i].text = sent[e - 1][i].text

RosterMatchesDraft == \A e \in Epochs: \A i \in 1..Len(sent[e]):
                        ("B" \in sent[e][i].cols) = sent[e][i].wantB

FramingOwn == \A c \in Cols: \A i \in 1..Len(ans[c]): ans[c][i].frame = <<c, ans[c][i].used[1]>>

WakeUsesCapture == \A i \in 1..Len(wans): wans[i].who = "A" => wans[i].used \in wans[i].want

SubWakeUsesCapture == \A i \in 1..Len(wans): wans[i].who = "sub" => wans[i].used \in wans[i].want

WakeOneCapture == \A i \in 1..Len(wans): wans[i].who = "A" => Cardinality(wans[i].want) = 1

NoShadowWakeTurn == \A i \in 1..Len(wans): ~wans[i].fan

WakeRowHonest == \A i \in 1..Len(wans): wans[i].who = "A" => PageWakeAOnly \/ wans[i].fan

SendIncludesTyped == \A e \in Epochs: \A i \in 1..Len(sent[e]): sent[e][i].typedOk

NeverWaitsForKeystrokes == ~((psend.on \/ prest.on) /\ ~Drained)

Sent(e, m, c) == m \in 1..Len(sent[e]) /\ c \in sent[e][m].cols
Answered(e, m, c) == \E i \in 1..Len(ans[c]): ans[c][i].e = e /\ ans[c][i].m = m


EveryColumnAnswers == <>[](\A e \in Epochs, m \in 1..MaxMsgs, c \in Cols: Sent(e, m, c) => Answered(e, m, c))

PressedSendGoesOut == []<>~psend.on /\ []<>~prest.on

VARIABLES cur, used, frame, want, wmcap, fan

vars == << pc, epoch, rev, live, saving, hasB, diskHasB, edits, saves, 
           toggles, restarts, sends, tall, tin, psend, prest, refresh, 
           streamed, qcache, hist, team, norec, fails, refusals, open, sent, 
           rep, queue, started, ans, jobs, njobs, nfin, acap, wans, cur, used, 
           frame, want, wmcap, fan >>

ProcSet == {"U"} \cup {"W"} \cup {"J"} \cup (Cols)

Init == (* Global variables *)
        /\ epoch = 1
        /\ rev = [c \in Drafts |-> 0]
        /\ live = <<0, 0>>
        /\ saving = FALSE
        /\ hasB = TRUE
        /\ diskHasB = TRUE
        /\ edits = 0
        /\ saves = 0
        /\ toggles = 0
        /\ restarts = 0
        /\ sends = 0
        /\ tall = [c \in Drafts |-> 0]
        /\ tin = [c \in Drafts |-> 0]
        /\ psend = NoSend
        /\ prest = NoRestart
        /\ refresh = FALSE
        /\ streamed = 0
        /\ qcache = {}
        /\ hist = {}
        /\ team = 0
        /\ norec = {}
        /\ fails = 0
        /\ refusals = 0
        /\ open = NoOpen
        /\ sent = [e \in Epochs |-> <<>>]
        /\ rep = [e \in Epochs |-> -1]
        /\ queue = [c \in Cols |-> <<>>]
        /\ started = {}
        /\ ans = [c \in Cols |-> <<>>]
        /\ jobs = <<>>
        /\ njobs = 0
        /\ nfin = 0
        /\ acap = <<-1, -1>>
        /\ wans = <<>>
        (* Process Col *)
        /\ cur = [self \in Cols |-> [e |-> 0, m |-> 0]]
        /\ used = [self \in Cols |-> NoCap]
        /\ frame = [self \in Cols |-> <<"none", -1>>]
        /\ want = [self \in Cols |-> {}]
        /\ wmcap = [self \in Cols |-> NoCap]
        /\ fan = [self \in Cols |-> FALSE]
        /\ pc = [self \in ProcSet |-> CASE self = "U" -> "U"
                                        [] self = "W" -> "W"
                                        [] self = "J" -> "J"
                                        [] self \in Cols -> "Claim"]

U == /\ pc["U"] = "U"
     /\ \/ /\ sends < MaxMsgs /\ ~psend.on /\ (SendWhileRestarting \/ ~prest.on)
           /\ psend' = [on |-> TRUE, t |-> sends + 1, seen |-> tall]
           /\ sends' = sends + 1
           /\ UNCHANGED <<rev, live, saving, hasB, diskHasB, edits, saves, toggles, restarts, tall, prest, team, open, sent, queue>>
        \/ /\ edits < MaxEdits
           /\ \E c \in IF hasB THEN Drafts ELSE {"A"}:
                rev' = [rev EXCEPT ![c] = rev[c] + 1]
           /\ edits' = edits + 1
           /\ UNCHANGED <<live, saving, hasB, diskHasB, saves, toggles, restarts, sends, tall, psend, prest, team, open, sent, queue>>
        \/ /\ tall["A"] + tall["B"] < MaxTyped
           /\ \E c \in IF hasB THEN Drafts ELSE {"A"}:
                tall' = [tall EXCEPT ![c] = tall[c] + 1]
           /\ UNCHANGED <<rev, live, saving, hasB, diskHasB, edits, saves, toggles, restarts, sends, psend, prest, team, open, sent, queue>>
        \/ /\ saves < MaxSaves /\ ~saving
           /\ IF SaveAtomic
                 THEN /\ live' = <<live[1] + 1, live[2] + 1>>
                      /\ saves' = saves + 1
                      /\ diskHasB' = hasB
                      /\ UNCHANGED saving
                 ELSE /\ live' = [live EXCEPT ![1] = live[1] + 1]
                      /\ saving' = TRUE
                      /\ UNCHANGED << diskHasB, saves >>
           /\ UNCHANGED <<rev, hasB, edits, toggles, restarts, sends, tall, psend, prest, team, open, sent, queue>>
        \/ /\ saving
           /\ live' = [live EXCEPT ![2] = live[2] + 1]
           /\ saving' = FALSE
           /\ saves' = saves + 1
           /\ diskHasB' = hasB
           /\ UNCHANGED <<rev, hasB, edits, toggles, restarts, sends, tall, psend, prest, team, open, sent, queue>>
        \/ /\ toggles < MaxToggles /\ (hasB => Buf("B") = 0)
           /\ hasB' = ~hasB
           /\ toggles' = toggles + 1
           /\ UNCHANGED <<rev, live, saving, diskHasB, edits, saves, restarts, sends, tall, psend, prest, team, open, sent, queue>>
        \/ /\ team < MaxTeamSends /\ ~open.on
           /\ LET i == Len(sent[epoch]) + 1 IN
                LET msg == NewMsg(100 + team, [c \in Drafts |-> 0]) IN
                  /\ sent' = [sent EXCEPT ![epoch] = Append(sent[epoch], msg)]
                  /\ queue' = [c \in Cols |-> IF c \in msg.cols THEN Append(queue[c], [e |-> epoch, m |-> i]) ELSE queue[c]]
                  /\ open' = Opened(epoch, 1)
           /\ team' = team + 1
           /\ UNCHANGED <<rev, live, saving, hasB, diskHasB, edits, saves, toggles, restarts, sends, tall, psend, prest>>
        \/ /\ restarts < MaxRestarts /\ ~prest.on /\ (RestartWhileSending \/ (~psend.on /\ ~refresh))
           /\ \E rp \in BOOLEAN:
                prest' = [on |-> TRUE, replay |-> rp, q |-> {}, h |-> 0,
                          phase |-> IF rp /\ ReplayFrom \in {"fresh", "records"} THEN "click" ELSE "ready",
                          texts |-> IF ~rp THEN <<>>
                                    ELSE IF ReplayFrom = "started" THEN Texts([i \in 1..Shown(epoch) |-> i])
                                    ELSE IF ReplayFrom = "cached" THEN Texts(AscSeq((1..streamed) \cup qcache))
                                    ELSE <<>>,
                          seen |-> tall]
           /\ restarts' = restarts + 1
           /\ UNCHANGED <<rev, live, saving, hasB, diskHasB, edits, saves, toggles, sends, tall, psend, team, open, sent, queue>>
     /\ pc' = [pc EXCEPT !["U"] = "U"]
     /\ UNCHANGED << epoch, tin, refresh, streamed, qcache, hist, norec, fails, 
                     refusals, rep, started, ans, jobs, njobs, nfin, acap, 
                     wans, cur, used, frame, want, wmcap, fan >>

User == U

W == /\ pc["W"] = "W"
     /\ \/ /\ \E c \in {x \in Drafts : Buf(x) > 0}:
                /\ rev' = [rev EXCEPT ![c] = rev[c] + 1]
                /\ tin' = [tin EXCEPT ![c] = tall[c]]
           /\ UNCHANGED <<epoch, psend, prest, refresh, streamed, qcache, norec, fails, refusals, open, sent, rep, queue>>
        \/ /\ psend.on /\ ~open.on /\ (Flush # "none" => Drained)
           /\ LET i == Len(sent[epoch]) + 1 IN
                LET msg == NewMsg(psend.t, psend.seen) IN
                  /\ sent' = [sent EXCEPT ![epoch] = Append(sent[epoch], msg)]
                  /\ queue' = [c \in Cols |-> IF c \in msg.cols THEN Append(queue[c], [e |-> epoch, m |-> i]) ELSE queue[c]]
                  /\ open' = Opened(epoch, 1)
           /\ psend' = NoSend
           /\ refresh' = ~RestartWhileSending
           /\ UNCHANGED <<epoch, rev, tin, prest, streamed, qcache, norec, fails, refusals, rep>>
        \/ /\ open.on
           /\ open' = NoOpen
           /\ UNCHANGED <<epoch, rev, tin, psend, prest, refresh, streamed, qcache, norec, fails, refusals, sent, rep, queue>>
        \/ /\ open.on /\ fails < MaxRecordFailures
           /\ norec' = (norec \cup {<<open.e, i>> : i \in (Len(sent[open.e]) - open.k + 1)..Len(sent[open.e])})
           /\ open' = NoOpen
           /\ fails' = fails + 1
           /\ UNCHANGED <<epoch, rev, tin, psend, prest, refresh, streamed, qcache, refusals, sent, rep, queue>>
        \/ /\ refresh \/ (ReplayFrom = "cached" /\ qcache # Queued(epoch))
           /\ qcache' = (IF ReplayFrom = "cached" THEN Queued(epoch) ELSE {})
           /\ refresh' = FALSE
           /\ UNCHANGED <<epoch, rev, tin, psend, prest, streamed, norec, fails, refusals, open, sent, rep, queue>>
        \/ /\ ReplayFrom = "cached" /\ streamed < Shown(epoch)
           /\ streamed' = streamed + 1
           /\ UNCHANGED <<epoch, rev, tin, psend, prest, refresh, qcache, norec, fails, refusals, open, sent, rep, queue>>
        \/ /\ prest.on /\ prest.phase # "ready"
           /\ IF ReplayFrom = "records"
                 THEN /\ prest' = [prest EXCEPT !.phase = "ready", !.texts = Texts(RecIdx(epoch))]
                 ELSE /\ IF prest.phase = "click"
                            THEN /\ LET q == Queued(epoch) IN
                                      prest' = [prest EXCEPT !.q = q, !.phase = "half"]
                            ELSE /\ LET h == InHistory(epoch) IN
                                      prest' = [prest EXCEPT !.h = h, !.phase = "ready", !.texts = Texts(AscSeq((1..h) \cup prest.q))]
           /\ UNCHANGED <<epoch, rev, tin, psend, refresh, streamed, qcache, norec, fails, refusals, open, sent, rep, queue>>
        \/ /\ prest.on /\ prest.phase = "ready" /\ ~open.on /\ (Flush = "all" => Drained)
           /\ LET ne == epoch + 1 IN
                LET texts == IF ReplayFrom = "server" /\ prest.replay THEN Texts(RecIdx(epoch)) ELSE prest.texts IN
                  \E refused \in IF texts # <<>> /\ refusals < MaxRefusals THEN BOOLEAN ELSE {FALSE}:
                    LET msgs == IF refused THEN <<>> ELSE [i \in 1..Len(texts) |-> NewMsg(texts[i], prest.seen)] IN
                      /\ refusals' = refusals + (IF refused THEN 1 ELSE 0)
                      /\ IF ~refused \/ RefuseSwitches
                            THEN /\ sent' = [sent EXCEPT ![ne] = msgs]
                                 /\ rep' = [rep EXCEPT ![ne] = IF prest.replay THEN Len(msgs) ELSE -1]
                                 /\ queue' = [c \in Cols |-> queue[c] \o Entries(ne, msgs, c)]
                                 /\ open' = Opened(ne, Len(msgs))
                                 /\ epoch' = ne
                            ELSE /\ TRUE
                                 /\ UNCHANGED << epoch, open, sent, rep, queue >>
           /\ prest' = NoRestart
           /\ streamed' = 0
           /\ qcache' = {}
           /\ UNCHANGED <<rev, tin, psend, refresh, norec, fails>>
     /\ pc' = [pc EXCEPT !["W"] = "W"]
     /\ UNCHANGED << live, saving, hasB, diskHasB, edits, saves, toggles, 
                     restarts, sends, tall, hist, team, started, ans, jobs, 
                     njobs, nfin, acap, wans, cur, used, frame, want, wmcap, 
                     fan >>

Client == W

J == /\ pc["J"] = "J"
     /\ \/ /\ \E j \in {k \in DOMAIN jobs : jobs[k].st = "run"}:
                /\ jobs' = [jobs EXCEPT ![j] = [jobs[j] EXCEPT !.st = "done", !.fin = nfin + 1]]
                /\ nfin' = nfin + 1
           /\ wans' = wans
        \/ /\ WakeFix # "inert"
           /\ \E j \in {k \in DOMAIN jobs : jobs[k].st = "done" /\ jobs[k].kind = "sub"}:
                /\ wans' = Append(wans, [who |-> "sub", e |-> jobs[j].e, used |-> IF acap # NoCap THEN acap ELSE DiskA,
                                         want |-> {jobs[j].mcap}, fan |-> FALSE])
                /\ jobs' = Append([jobs EXCEPT ![j].st = "delivered"],
                                  [e |-> jobs[j].e, kind |-> "report", st |-> "done", fin |-> nfin + 1, ocap |-> NoCap, mcap |-> jobs[j].mcap])
                /\ nfin' = nfin + 1
     /\ pc' = [pc EXCEPT !["J"] = "J"]
     /\ UNCHANGED << epoch, rev, live, saving, hasB, diskHasB, edits, saves, 
                     toggles, restarts, sends, tall, tin, psend, prest, 
                     refresh, streamed, qcache, hist, team, norec, fails, 
                     refusals, open, sent, rep, queue, started, ans, njobs, 
                     acap, cur, used, frame, want, wmcap, fan >>

Jobs == J

Claim(self) == /\ pc[self] = "Claim"
               /\ \/ /\ queue[self] # <<>>
                     /\ LET h == Head(queue[self]) IN
                          /\ self = "A" \/ <<h.e, h.m>> \in started
                          /\ IF self = "A"
                                THEN /\ used' = [used EXCEPT ![self] = ACap(h)]
                                     /\ frame' = [frame EXCEPT ![self] = <<"A", ACap(h)[1]>>]
                                     /\ started' = (started \cup {<<h.e, h.m>>})
                                     /\ acap' = ACap(h)
                                     /\ IF CaptureAt = "parentStart"
                                           THEN /\ sent' = [sent EXCEPT ![h.e][h.m].cap = [c \in Cols |-> Capture(c)],
                                                                        ![h.e][h.m].aframe = <<"A", CurVer("A")[1]>>]
                                           ELSE /\ sent' = [sent EXCEPT ![h.e][h.m].aframe = <<"A", ACap(h)[1]>>]
                                ELSE /\ used' = [used EXCEPT ![self] = IF sent[h.e][h.m].cap[self] # NoCap THEN sent[h.e][h.m].cap[self] ELSE CurVer(self)]
                                     /\ frame' = [frame EXCEPT ![self] = IF ShadowOwnFraming
                                                                         THEN <<self, (IF sent[h.e][h.m].cap[self] # NoCap THEN sent[h.e][h.m].cap[self] ELSE CurVer(self))[1]>>
                                                                         ELSE sent[h.e][h.m].aframe]
                                     /\ UNCHANGED << sent, started, acap >>
                          /\ cur' = [cur EXCEPT ![self] = h]
                          /\ queue' = [queue EXCEPT ![self] = Tail(queue[self])]
                     /\ UNCHANGED <<jobs, want, wmcap, fan>>
                  \/ /\ self = "A" /\ WakeFix \in {"none", "copy", "copyFanOut"} /\ Notes # {}
                     /\ \E e \in {jobs[j].e : j \in Notes}:
                          LET b == {j \in Notes : jobs[j].e = e} IN
                            LET f == CHOOSE j \in b : \A k \in b : jobs[j].fin <= jobs[k].fin IN
                              LET oc == IF WakeFix = "none" THEN NoCap ELSE jobs[f].ocap IN
                                /\ used' = [used EXCEPT ![self] = IF oc # NoCap THEN oc ELSE DiskA]
                                /\ frame' = [frame EXCEPT ![self] = <<"A", (IF oc # NoCap THEN oc ELSE DiskA)[1]>>]
                                /\ acap' = oc
                                /\ want' = [want EXCEPT ![self] = {jobs[j].mcap : j \in b}]
                                /\ wmcap' = [wmcap EXCEPT ![self] = jobs[f].mcap]
                                /\ fan' = [fan EXCEPT ![self] = WakeFix = "copyFanOut" /\ oc # NoCap]
                                /\ jobs' = Deliver(b)
                                /\ cur' = [cur EXCEPT ![self] = [e |-> e, m |-> 0]]
                     /\ UNCHANGED <<sent, queue, started>>
               /\ pc' = [pc EXCEPT ![self] = "Finish"]
               /\ UNCHANGED << epoch, rev, live, saving, hasB, diskHasB, edits, 
                               saves, toggles, restarts, sends, tall, tin, 
                               psend, prest, refresh, streamed, qcache, hist, 
                               team, norec, fails, refusals, open, rep, ans, 
                               njobs, nfin, wans >>

Finish(self) == /\ pc[self] = "Finish"
                /\ IF cur[self].m > 0
                      THEN /\ IF self = "A"
                                 THEN /\ hist' = (hist \cup {<<cur[self].e, cur[self].m>>})
                                 ELSE /\ TRUE
                                      /\ hist' = hist
                           /\ ans' = [ans EXCEPT ![self] = Append(ans[self],
                                                             [e |-> cur[self].e, m |-> cur[self].m, used |-> used[self], frame |-> frame[self],
                                                              label |-> IF LabelFromRecord THEN sent[cur[self].e][cur[self].m].sv[self] ELSE used[self],
                                                              tag |-> IF RestartNewConv THEN cur[self].e ELSE epoch])]
                           /\ wans' = wans
                      ELSE /\ wans' = Append(wans, [who |-> "A", e |-> cur[self].e, used |-> used[self], want |-> want[self], fan |-> fan[self]])
                           /\ UNCHANGED << hist, ans >>
                /\ IF self = "A"
                      THEN /\ \/ /\ TRUE
                                 /\ UNCHANGED <<jobs, njobs>>
                              \/ /\ njobs < MaxJobs /\ WakeFix # "inert"
                                 /\ \E k \in IF SubJobs THEN {"own", "sub"} ELSE {"own"}:
                                      jobs' = Append(jobs, [e |-> cur[self].e, kind |-> k, st |-> "run", fin |-> 0,
                                                            ocap |-> IF k = "own" THEN acap ELSE NoCap,
                                                            mcap |-> IF cur[self].m > 0 THEN sent[cur[self].e][cur[self].m].sv["A"] ELSE wmcap[self]])
                                 /\ njobs' = njobs + 1
                           /\ acap' = NoCap
                           /\ want' = [want EXCEPT ![self] = {}]
                           /\ wmcap' = [wmcap EXCEPT ![self] = NoCap]
                           /\ fan' = [fan EXCEPT ![self] = FALSE]
                      ELSE /\ TRUE
                           /\ UNCHANGED << jobs, njobs, acap, want, wmcap, fan >>
                /\ pc' = [pc EXCEPT ![self] = "Claim"]
                /\ UNCHANGED << epoch, rev, live, saving, hasB, diskHasB, 
                                edits, saves, toggles, restarts, sends, tall, 
                                tin, psend, prest, refresh, streamed, qcache, 
                                team, norec, fails, refusals, open, sent, rep, 
                                queue, started, nfin, cur, used, frame >>

Col(self) == Claim(self) \/ Finish(self)

Next == User \/ Client \/ Jobs
           \/ (\E self \in Cols: Col(self))

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Client)
        /\ \A self \in Cols : WF_vars(Col(self))

\* END TRANSLATION

\* The same behaviors with no fairness, for the liveness canary.
UnfairSpec == Init /\ [][Next]_vars
=============================================================================
