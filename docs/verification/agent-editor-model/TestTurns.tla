----------------------------- MODULE TestTurns -----------------------------
(* Test turns in the Agent Editor's draft test chat (R11-R15).            *)
(* Columns: "A" = parent conversation running the draft, "B" = draft       *)
(* variant shadow (<name>/b), "L" = live shadow running the saved file.    *)
(* Mechanism switches (constants) select the planned design or a fix.      *)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS
  MaxMsgs,        \* user messages per chat
  MaxEdits,       \* draft edits (revision++), any variant
  MaxSaves,       \* saves of the live agent file
  MaxToggles,     \* add/remove variant B in the draft
  MaxRestarts,    \* Restart or Restart-and-replay
  CaptureAt,      \* when draft columns fix their version: "send" | "parentStart" | "run"
  LiveCaptured,   \* TRUE: live file bytes captured with the draft; FALSE: live shadow reads disk when it runs
  LabelFromUsed,  \* TRUE: label = version the loader actually used; FALSE: label = version at send
  RestartNewConv, \* TRUE: Restart makes a new conversation; FALSE: clears the same conversation in place
  KeyById,        \* TRUE: answers placed by message id; FALSE: by position in the column
  LayerOnRoster,  \* TRUE: shadow roster read through the draft layer; FALSE: from disk
  SaveAtomic,     \* TRUE: a save replaces all agent files at once; FALSE: file by file
  ShadowOwnFraming \* TRUE: each column frames the prompt with its own definition; FALSE: shadows reuse the parent's prepared input

ASSUME CaptureAt \in {"send", "parentStart", "run"}

Cols == {"A", "B", "L"}
Epochs == 1..(MaxRestarts + 1)

(* --algorithm TestTurns {
variables
  epoch = 1,
  rev = [c \in {"A", "B"} |-> 0],   \* draft revision per draft column
  live = <<0, 0>>,                   \* saved file versions: <<agent file, second file>>
  saving = FALSE,
  hasB = TRUE, diskHasB = TRUE,      \* variant B in the draft / on disk
  edits = 0, saves = 0, toggles = 0, restarts = 0,
  sent = [e \in Epochs |-> <<>>],    \* user messages per chat
  replayed = [e \in Epochs |-> FALSE],
  queue = [c \in Cols |-> <<>>],     \* admitted turns (manifests) per column, FIFO
  started = {},                      \* <<e, m>> whose parent turn has started
  ans = [c \in Cols |-> <<>>];       \* answers per column

define {
  NoCap == <<-1, -1>>
  CurVer(c) == IF c = "L" THEN live ELSE <<rev[c], rev[c]>>
  Capture(c) == IF c = "L" /\ ~LiveCaptured THEN NoCap ELSE CurVer(c)
  RosterB == IF LayerOnRoster THEN hasB ELSE diskHasB
  NewMsg(t) == [text |-> t,
                cols |-> {"A", "L"} \cup (IF RosterB THEN {"B"} ELSE {}),
                wantB |-> hasB,
                sv |-> [c \in Cols |-> CurVer(c)],
                cap |-> [c \in Cols |-> IF CaptureAt = "send" THEN Capture(c) ELSE NoCap],
                aframe |-> <<"none", -1>>]
  Idx(msgs, c) == SelectSeq([i \in 1..Len(msgs) |-> i], LAMBDA i: c \in msgs[i].cols)
  Entries(e, msgs, c) == [k \in 1..Len(Idx(msgs, c)) |-> [e |-> e, m |-> Idx(msgs, c)[k]]]

  \* 1. VersionAtSend: the definition used is the one current when the message was sent.
  VersionAtSendDraft == \A c \in {"A", "B"}: \A i \in 1..Len(ans[c]):
                          ans[c][i].used = sent[ans[c][i].e][ans[c][i].m].sv[c]
  VersionAtSendLive == \A i \in 1..Len(ans["L"]):
                          ans["L"][i].used = sent[ans["L"][i].e][ans["L"][i].m].sv["L"]
  \* 2. LabelTruthful: the shown version is the one actually loaded (all files from it).
  LabelTruthful == \A c \in Cols: \A i \in 1..Len(ans[c]):
                     ans[c][i].used = <<ans[c][i].label, ans[c][i].label>>
  \* 3. AlignedPerMessage, over the answers shown in the current chat.
  ChatAns(c) == SelectSeq(ans[c], LAMBDA a: a.tag = epoch)
  AlignedPerMessage == \A c \in Cols: LET s == ChatAns(c) IN
                         \A i \in 1..Len(s): (KeyById \/ s[i].m = i)
                                             /\ \A j \in (i + 1)..Len(s): s[i].m < s[j].m
  \* 4. RestartIsolation: an answer is shown only in the chat its message was sent in.
  RestartIsolation == \A c \in Cols: \A i \in 1..Len(ans[c]): ans[c][i].tag = ans[c][i].e
  ReplaySameOrder == \A e \in Epochs \ {1}: replayed[e] =>
                        /\ Len(sent[e]) >= Len(sent[e - 1])
                        /\ \A i \in 1..Len(sent[e - 1]): sent[e][i].text = sent[e - 1][i].text
  \* 6a. R14: one message goes to every variant the draft has at send.
  RosterMatchesDraft == \A e \in Epochs: \A i \in 1..Len(sent[e]):
                          ("B" \in sent[e][i].cols) = sent[e][i].wantB
  \* 6b. The prompt framing (additionalInstructions, easy mode) comes from the column's own definition.
  FramingOwn == \A c \in Cols: \A i \in 1..Len(ans[c]): ans[c][i].frame = <<c, ans[c][i].used[1]>>
  \* 5. Liveness.
  Sent(e, m, c) == m \in 1..Len(sent[e]) /\ c \in sent[e][m].cols
  Answered(e, m, c) == \E i \in 1..Len(ans[c]): ans[c][i].e = e /\ ans[c][i].m = m
  EveryColumnAnswers == \A e \in Epochs, m \in 1..MaxMsgs, c \in Cols:
                          Sent(e, m, c) ~> Answered(e, m, c)
} \* end define

process (User = "U") {
  U: while (TRUE) {
    either { \* send a message
      await Len(sent[epoch]) < MaxMsgs;
      with (i = Len(sent[epoch]) + 1) {
        with (msg = NewMsg(<<epoch, i>>)) {
          sent[epoch] := Append(sent[epoch], msg);
          queue := [c \in Cols |-> IF c \in msg.cols THEN Append(queue[c], [e |-> epoch, m |-> i]) ELSE queue[c]];
        }
      }
    } or { \* edit the draft of one variant
      await edits < MaxEdits;
      with (c \in IF hasB THEN {"A", "B"} ELSE {"A"}) { rev[c] := rev[c] + 1; };
      edits := edits + 1;
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
    } or { \* add or remove variant B in the draft
      await toggles < MaxToggles;
      hasB := ~hasB; toggles := toggles + 1;
    } or { \* Restart: new empty chat; turns of the old chat keep running
      await restarts < MaxRestarts;
      epoch := epoch + 1; restarts := restarts + 1;
    } or { \* Restart and replay: new chat, every earlier user message re-sent with the current versions
      await restarts < MaxRestarts;
      with (ne = epoch + 1) {
        with (msgs = [i \in 1..Len(sent[epoch]) |-> NewMsg(sent[epoch][i].text)]) {
          sent[ne] := msgs; replayed[ne] := TRUE;
          queue := [c \in Cols |-> queue[c] \o Entries(ne, msgs, c)];
        };
        epoch := ne;
      };
      restarts := restarts + 1;
    }
  }
}

fair process (Col \in Cols)
variables cur = [e |-> 0, m |-> 0], used = NoCap, frame = <<"none", -1>>;
{
  Claim: while (TRUE) {
    await queue[self] # <<>>;
    with (h = Head(queue[self])) {
      \* A shadow claims its manifest only after the parent's turn captured its input
      \* (claimShadowTurn needs non-empty inputs, written when the parent turn starts).
      await self = "A" \/ <<h.e, h.m>> \in started;
      if (self = "A") {
        used := IF CaptureAt = "send" THEN sent[h.e][h.m].cap["A"] ELSE CurVer("A");
        frame := <<"A", (IF CaptureAt = "send" THEN sent[h.e][h.m].cap["A"] ELSE CurVer("A"))[1]>>;
        started := started \cup {<<h.e, h.m>>};
        if (CaptureAt = "parentStart") {
          sent[h.e][h.m].cap := [c \in Cols |-> Capture(c)] || sent[h.e][h.m].aframe := <<"A", CurVer("A")[1]>>;
        } else {
          sent[h.e][h.m].aframe := <<"A", (IF CaptureAt = "send" THEN sent[h.e][h.m].cap["A"] ELSE CurVer("A"))[1]>>;
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
  Finish:
    ans[self] := Append(ans[self],
                   [e |-> cur.e, m |-> cur.m, used |-> used, frame |-> frame,
                    label |-> IF LabelFromUsed THEN used[1] ELSE sent[cur.e][cur.m].sv[self][1],
                    tag |-> IF RestartNewConv THEN cur.e ELSE epoch]);
  }
}
} *)
\* BEGIN TRANSLATION (chksum(pcal) = "1bba4671" /\ chksum(tla) = "7bfc3886")
VARIABLES epoch, rev, live, saving, hasB, diskHasB, edits, saves, toggles, 
          restarts, sent, replayed, queue, started, ans, pc

(* define statement *)
NoCap == <<-1, -1>>
CurVer(c) == IF c = "L" THEN live ELSE <<rev[c], rev[c]>>
Capture(c) == IF c = "L" /\ ~LiveCaptured THEN NoCap ELSE CurVer(c)
RosterB == IF LayerOnRoster THEN hasB ELSE diskHasB
NewMsg(t) == [text |-> t,
              cols |-> {"A", "L"} \cup (IF RosterB THEN {"B"} ELSE {}),
              wantB |-> hasB,
              sv |-> [c \in Cols |-> CurVer(c)],
              cap |-> [c \in Cols |-> IF CaptureAt = "send" THEN Capture(c) ELSE NoCap],
              aframe |-> <<"none", -1>>]
Idx(msgs, c) == SelectSeq([i \in 1..Len(msgs) |-> i], LAMBDA i: c \in msgs[i].cols)
Entries(e, msgs, c) == [k \in 1..Len(Idx(msgs, c)) |-> [e |-> e, m |-> Idx(msgs, c)[k]]]


VersionAtSendDraft == \A c \in {"A", "B"}: \A i \in 1..Len(ans[c]):
                        ans[c][i].used = sent[ans[c][i].e][ans[c][i].m].sv[c]
VersionAtSendLive == \A i \in 1..Len(ans["L"]):
                        ans["L"][i].used = sent[ans["L"][i].e][ans["L"][i].m].sv["L"]

LabelTruthful == \A c \in Cols: \A i \in 1..Len(ans[c]):
                   ans[c][i].used = <<ans[c][i].label, ans[c][i].label>>

ChatAns(c) == SelectSeq(ans[c], LAMBDA a: a.tag = epoch)
AlignedPerMessage == \A c \in Cols: LET s == ChatAns(c) IN
                       \A i \in 1..Len(s): (KeyById \/ s[i].m = i)
                                           /\ \A j \in (i + 1)..Len(s): s[i].m < s[j].m

RestartIsolation == \A c \in Cols: \A i \in 1..Len(ans[c]): ans[c][i].tag = ans[c][i].e
ReplaySameOrder == \A e \in Epochs \ {1}: replayed[e] =>
                      /\ Len(sent[e]) >= Len(sent[e - 1])
                      /\ \A i \in 1..Len(sent[e - 1]): sent[e][i].text = sent[e - 1][i].text

RosterMatchesDraft == \A e \in Epochs: \A i \in 1..Len(sent[e]):
                        ("B" \in sent[e][i].cols) = sent[e][i].wantB

FramingOwn == \A c \in Cols: \A i \in 1..Len(ans[c]): ans[c][i].frame = <<c, ans[c][i].used[1]>>

Sent(e, m, c) == m \in 1..Len(sent[e]) /\ c \in sent[e][m].cols
Answered(e, m, c) == \E i \in 1..Len(ans[c]): ans[c][i].e = e /\ ans[c][i].m = m
EveryColumnAnswers == \A e \in Epochs, m \in 1..MaxMsgs, c \in Cols:
                        Sent(e, m, c) ~> Answered(e, m, c)

VARIABLES cur, used, frame

vars == << epoch, rev, live, saving, hasB, diskHasB, edits, saves, toggles, 
           restarts, sent, replayed, queue, started, ans, pc, cur, used, 
           frame >>

ProcSet == {"U"} \cup (Cols)

Init == (* Global variables *)
        /\ epoch = 1
        /\ rev = [c \in {"A", "B"} |-> 0]
        /\ live = <<0, 0>>
        /\ saving = FALSE
        /\ hasB = TRUE
        /\ diskHasB = TRUE
        /\ edits = 0
        /\ saves = 0
        /\ toggles = 0
        /\ restarts = 0
        /\ sent = [e \in Epochs |-> <<>>]
        /\ replayed = [e \in Epochs |-> FALSE]
        /\ queue = [c \in Cols |-> <<>>]
        /\ started = {}
        /\ ans = [c \in Cols |-> <<>>]
        (* Process Col *)
        /\ cur = [self \in Cols |-> [e |-> 0, m |-> 0]]
        /\ used = [self \in Cols |-> NoCap]
        /\ frame = [self \in Cols |-> <<"none", -1>>]
        /\ pc = [self \in ProcSet |-> CASE self = "U" -> "U"
                                        [] self \in Cols -> "Claim"]

U == /\ pc["U"] = "U"
     /\ \/ /\ Len(sent[epoch]) < MaxMsgs
           /\ LET i == Len(sent[epoch]) + 1 IN
                LET msg == NewMsg(<<epoch, i>>) IN
                  /\ sent' = [sent EXCEPT ![epoch] = Append(sent[epoch], msg)]
                  /\ queue' = [c \in Cols |-> IF c \in msg.cols THEN Append(queue[c], [e |-> epoch, m |-> i]) ELSE queue[c]]
           /\ UNCHANGED <<epoch, rev, live, saving, hasB, diskHasB, edits, saves, toggles, restarts, replayed>>
        \/ /\ edits < MaxEdits
           /\ \E c \in IF hasB THEN {"A", "B"} ELSE {"A"}:
                rev' = [rev EXCEPT ![c] = rev[c] + 1]
           /\ edits' = edits + 1
           /\ UNCHANGED <<epoch, live, saving, hasB, diskHasB, saves, toggles, restarts, sent, replayed, queue>>
        \/ /\ saves < MaxSaves /\ ~saving
           /\ IF SaveAtomic
                 THEN /\ live' = <<live[1] + 1, live[2] + 1>>
                      /\ saves' = saves + 1
                      /\ diskHasB' = hasB
                      /\ UNCHANGED saving
                 ELSE /\ live' = [live EXCEPT ![1] = live[1] + 1]
                      /\ saving' = TRUE
                      /\ UNCHANGED << diskHasB, saves >>
           /\ UNCHANGED <<epoch, rev, hasB, edits, toggles, restarts, sent, replayed, queue>>
        \/ /\ saving
           /\ live' = [live EXCEPT ![2] = live[2] + 1]
           /\ saving' = FALSE
           /\ saves' = saves + 1
           /\ diskHasB' = hasB
           /\ UNCHANGED <<epoch, rev, hasB, edits, toggles, restarts, sent, replayed, queue>>
        \/ /\ toggles < MaxToggles
           /\ hasB' = ~hasB
           /\ toggles' = toggles + 1
           /\ UNCHANGED <<epoch, rev, live, saving, diskHasB, edits, saves, restarts, sent, replayed, queue>>
        \/ /\ restarts < MaxRestarts
           /\ epoch' = epoch + 1
           /\ restarts' = restarts + 1
           /\ UNCHANGED <<rev, live, saving, hasB, diskHasB, edits, saves, toggles, sent, replayed, queue>>
        \/ /\ restarts < MaxRestarts
           /\ LET ne == epoch + 1 IN
                /\ LET msgs == [i \in 1..Len(sent[epoch]) |-> NewMsg(sent[epoch][i].text)] IN
                     /\ sent' = [sent EXCEPT ![ne] = msgs]
                     /\ replayed' = [replayed EXCEPT ![ne] = TRUE]
                     /\ queue' = [c \in Cols |-> queue[c] \o Entries(ne, msgs, c)]
                /\ epoch' = ne
           /\ restarts' = restarts + 1
           /\ UNCHANGED <<rev, live, saving, hasB, diskHasB, edits, saves, toggles>>
     /\ pc' = [pc EXCEPT !["U"] = "U"]
     /\ UNCHANGED << started, ans, cur, used, frame >>

User == U

Claim(self) == /\ pc[self] = "Claim"
               /\ queue[self] # <<>>
               /\ LET h == Head(queue[self]) IN
                    /\ self = "A" \/ <<h.e, h.m>> \in started
                    /\ IF self = "A"
                          THEN /\ used' = [used EXCEPT ![self] = IF CaptureAt = "send" THEN sent[h.e][h.m].cap["A"] ELSE CurVer("A")]
                               /\ frame' = [frame EXCEPT ![self] = <<"A", (IF CaptureAt = "send" THEN sent[h.e][h.m].cap["A"] ELSE CurVer("A"))[1]>>]
                               /\ started' = (started \cup {<<h.e, h.m>>})
                               /\ IF CaptureAt = "parentStart"
                                     THEN /\ sent' = [sent EXCEPT ![h.e][h.m].cap = [c \in Cols |-> Capture(c)],
                                                                  ![h.e][h.m].aframe = <<"A", CurVer("A")[1]>>]
                                     ELSE /\ sent' = [sent EXCEPT ![h.e][h.m].aframe = <<"A", (IF CaptureAt = "send" THEN sent[h.e][h.m].cap["A"] ELSE CurVer("A"))[1]>>]
                          ELSE /\ used' = [used EXCEPT ![self] = IF sent[h.e][h.m].cap[self] # NoCap THEN sent[h.e][h.m].cap[self] ELSE CurVer(self)]
                               /\ frame' = [frame EXCEPT ![self] = IF ShadowOwnFraming
                                                                   THEN <<self, (IF sent[h.e][h.m].cap[self] # NoCap THEN sent[h.e][h.m].cap[self] ELSE CurVer(self))[1]>>
                                                                   ELSE sent[h.e][h.m].aframe]
                               /\ UNCHANGED << sent, started >>
                    /\ cur' = [cur EXCEPT ![self] = h]
                    /\ queue' = [queue EXCEPT ![self] = Tail(queue[self])]
               /\ pc' = [pc EXCEPT ![self] = "Finish"]
               /\ UNCHANGED << epoch, rev, live, saving, hasB, diskHasB, edits, 
                               saves, toggles, restarts, replayed, ans >>

Finish(self) == /\ pc[self] = "Finish"
                /\ ans' = [ans EXCEPT ![self] = Append(ans[self],
                                                  [e |-> cur[self].e, m |-> cur[self].m, used |-> used[self], frame |-> frame[self],
                                                   label |-> IF LabelFromUsed THEN used[self][1] ELSE sent[cur[self].e][cur[self].m].sv[self][1],
                                                   tag |-> IF RestartNewConv THEN cur[self].e ELSE epoch])]
                /\ pc' = [pc EXCEPT ![self] = "Claim"]
                /\ UNCHANGED << epoch, rev, live, saving, hasB, diskHasB, 
                                edits, saves, toggles, restarts, sent, 
                                replayed, queue, started, cur, used, frame >>

Col(self) == Claim(self) \/ Finish(self)

Next == User
           \/ (\E self \in Cols: Col(self))

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in Cols : WF_vars(Col(self))

\* END TRANSLATION 
=============================================================================
