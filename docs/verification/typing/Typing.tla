--------------------------------- MODULE Typing ---------------------------------
(* Who else is typing in a Web chat. Ana types in chat c1. Her browser tab reports
   it to the server (internal/rocketclaw/web/src/typing.tsx reportTyping): typing
   on a keystroke, at most once every Throttle ticks while she goes on, and not
   typing once when her draft empties after that, whether she deleted it or sent
   it. Each report leaves at once, numbered by the tab, so no report waits on
   another; on its way it may be overtaken by a later one, or never arrive.

   The server (internal/rocketclaw/frontend/rpc/typing.go reportTyping) passes a
   report on to every watch of the chat but the reporter's own, dropping it for a
   watch whose Buffer is full. While the chat has a watch, it keeps the newest
   number it took from each tab; Ana has one tab that types, so one number. The
   number goes with the chat's last watch. A report for a chat nobody watches
   goes nowhere.

   Bea watches the chat she views, and so does Ana's second tab. Either may move
   to another chat (c2) and back; each move closes its watch and opens a new one,
   which hears nothing until the server registers it. A watch (typing.tsx
   useTypists) keeps a typist from their typing report until their not-typing
   report, and with Expires hides them Expiry ticks after it. Ana is the only
   typist, so a watch holds at most one entry.

   Ticks stand for seconds scaled down: Throttle for 2 s, Expiry for 5 s. Ages
   stop counting at those bounds, so the clock runs forever over finitely many
   states.

   The model is the PlusCal algorithm below; `make` translates it into the TLA+
   between the TRANSLATION markers, which is generated, and checks each shape.
   Ana and the people who move watches act when they like; the network, each
   watch and the clock keep going, each fairly on its own.

   The CONSTANTS switch between implementation shapes:
   - Ordered: the server drops a report numbered no higher than the newest it
     took from that tab.
   - Expires: a watch hides a typist Expiry ticks after their last report.
   - KeyedWatch: a watch's typists start empty when it moves to another chat
     (the key on Typists in typing.tsx TypingPill).
   - SelfFilter: the server skips the watches of the person reporting. *)
EXTENDS Integers, Sequences

CONSTANTS Throttle, Expiry, MaxReports, Buffer, Ordered, Expires, KeyedWatch, SelfFilter

Watchers == {"bea", "ana2"}
Owner(w) == IF w = "bea" THEN "bea" ELSE "ana"
Chats == {"c1", "c2"}
NoEntry == [seq |-> 0, age |-> 0, chat |-> "none"]
Min(a, b) == IF a < b THEN a ELSE b
Max(a, b) == IF a > b THEN a ELSE b
\* Chat c1 has a watch the server registered.
Watched(reg, view) == \E w \in Watchers : reg[w] /\ view[w] = "c1"
\* The server passes Ana's reports on to w: its watch is registered on c1, and it is not hers when filtered.
Hears(w, reg, view) == reg[w] /\ view[w] = "c1" /\ (~SelfFilter \/ Owner(w) # "ana")

(* --algorithm Typing
variables
    draft = FALSE,      \* Ana's draft in c1 holds text
    rep = -1,           \* -1: no typing report since the draft last emptied; else ticks since the last one
    sent = 0,           \* reports Ana's tab made, which number them
    net = {},           \* reports on their way to the server
    newest = 0,         \* the newest report number the server took from Ana's tab while c1 is watched
    viewing = [w \in Watchers |-> "c1"],
    registered = [w \in Watchers |-> TRUE],
    buf = [w \in Watchers |-> <<>>],     \* events the server holds for each watch
    entry = [w \in Watchers |-> NoEntry],
    falseSeen = [w \in Watchers |-> 0],  \* ghost: the newest not-typing report each watch heard
    stopped = 0,        \* ghost: the newest not-typing report that reached the server
    arrived = 0,        \* ghost: the newest report that reached the server
    overtaken = FALSE,  \* ghost: a report reached the server after a later one
    stale = FALSE,      \* ghost: the server dropped a report older than one it took
    dropped = FALSE;    \* ghost: an event was dropped for a full watch

define
    Shown(w) == entry[w] # NoEntry /\ (~Expires \/ entry[w].age < Expiry)
end define;

macro Relay(m) begin
    buf := [w \in Watchers |-> IF Hears(w, registered, viewing) /\ Len(buf[w]) < Buffer
                                  THEN Append(buf[w], [seq |-> m.seq, typing |-> m.typing])
                                  ELSE buf[w]];
    dropped := dropped \/ \E w \in Watchers : Hears(w, registered, viewing) /\ Len(buf[w]) >= Buffer;
end macro;

\* Ana, who types when she likes.
process Ana = <<"ana">>
begin
AnaActs:
    while TRUE do
        either
            \* A keystroke leaves text in the draft.
            with report = (rep = -1 \/ rep >= Throttle) do
                await ~report \/ sent < MaxReports;
                draft := TRUE;
                if report then
                    net := net \cup {[seq |-> sent + 1, typing |-> TRUE]};
                    sent := sent + 1;
                    rep := 0;
                end if;
            end with;
        or
            \* The draft empties: deleted, or sent by any path.
            await draft;
            with report = (rep # -1) do
                await ~report \/ sent < MaxReports;
                draft := FALSE;
                if report then
                    net := net \cup {[seq |-> sent + 1, typing |-> FALSE]};
                    sent := sent + 1;
                end if;
                rep := -1;
            end with;
        end either;
    end while;
end process;

\* Bea, or Ana in her second tab, moves to the other chat: the old watch closes and a new one opens. The server
\* forgets the chat's newest number with its last watch.
process Viewers = <<"viewers">>
begin
ViewersMove:
    while TRUE do
        with w \in Watchers, c \in Chats \ {viewing[w]} do
            viewing[w] := c;
            registered[w] := FALSE;
            buf[w] := <<>>;
            falseSeen[w] := 0;
            if KeyedWatch then entry[w] := NoEntry end if;
            newest := IF \E v \in Watchers \ {w} : registered[v] /\ viewing[v] = "c1" THEN newest ELSE 0;
        end with;
    end while;
end process;

\* A report reaches the server, which passes it on unless it is older than one it took; or it never arrives.
fair process Network = <<"network">>
begin
NetworkRuns:
    while TRUE do
        with m \in net do
            either
                overtaken := overtaken \/ m.seq < arrived;
                arrived := Max(arrived, m.seq);
                stopped := IF m.typing THEN stopped ELSE Max(stopped, m.seq);
                if ~Watched(registered, viewing) then
                    skip;
                elsif Ordered /\ m.seq <= newest then
                    stale := TRUE;
                else
                    newest := m.seq;
                    Relay(m);
                end if;
            or
                skip;
            end either;
            net := net \ {m};
        end with;
    end while;
end process;

\* A watch registers with the server, and the browser reads its events in order.
fair process Watch \in {<<"watch", w>> : w \in Watchers}
begin
WatchRuns:
    while TRUE do
        either
            await ~registered[self[2]];
            registered[self[2]] := TRUE;
        or
            await buf[self[2]] # <<>>;
            with e = Head(buf[self[2]]) do
                if e.typing then
                    entry[self[2]] := [seq |-> e.seq, age |-> 0, chat |-> viewing[self[2]]];
                else
                    entry[self[2]] := NoEntry;
                    falseSeen[self[2]] := Max(falseSeen[self[2]], e.seq);
                end if;
            end with;
            buf[self[2]] := Tail(buf[self[2]]);
        end either;
    end while;
end process;

fair process Clock = <<"clock">>
begin
Ticks:
    while TRUE do
        rep := IF rep = -1 THEN -1 ELSE Min(rep + 1, Throttle);
        entry := [w \in Watchers |-> IF entry[w] = NoEntry THEN NoEntry
                                      ELSE [entry[w] EXCEPT !.age = Min(@ + 1, Expiry)]];
    end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES draft, rep, sent, net, newest, viewing, registered, buf, entry, 
          falseSeen, stopped, arrived, overtaken, stale, dropped

(* define statement *)
Shown(w) == entry[w] # NoEntry /\ (~Expires \/ entry[w].age < Expiry)


vars == << draft, rep, sent, net, newest, viewing, registered, buf, entry, 
           falseSeen, stopped, arrived, overtaken, stale, dropped >>

ProcSet == {<<"ana">>} \cup {<<"viewers">>} \cup {<<"network">>} \cup ({<<"watch", w>> : w \in Watchers}) \cup {<<"clock">>}

Init == (* Global variables *)
        /\ draft = FALSE
        /\ rep = -1
        /\ sent = 0
        /\ net = {}
        /\ newest = 0
        /\ viewing = [w \in Watchers |-> "c1"]
        /\ registered = [w \in Watchers |-> TRUE]
        /\ buf = [w \in Watchers |-> <<>>]
        /\ entry = [w \in Watchers |-> NoEntry]
        /\ falseSeen = [w \in Watchers |-> 0]
        /\ stopped = 0
        /\ arrived = 0
        /\ overtaken = FALSE
        /\ stale = FALSE
        /\ dropped = FALSE

Ana == /\ \/ /\ LET report == (rep = -1 \/ rep >= Throttle) IN
                  /\ ~report \/ sent < MaxReports
                  /\ draft' = TRUE
                  /\ IF report
                        THEN /\ net' = (net \cup {[seq |-> sent + 1, typing |-> TRUE]})
                             /\ sent' = sent + 1
                             /\ rep' = 0
                        ELSE /\ TRUE
                             /\ UNCHANGED << rep, sent, net >>
          \/ /\ draft
             /\ LET report == (rep # -1) IN
                  /\ ~report \/ sent < MaxReports
                  /\ draft' = FALSE
                  /\ IF report
                        THEN /\ net' = (net \cup {[seq |-> sent + 1, typing |-> FALSE]})
                             /\ sent' = sent + 1
                        ELSE /\ TRUE
                             /\ UNCHANGED << sent, net >>
                  /\ rep' = -1
       /\ UNCHANGED << newest, viewing, registered, buf, entry, falseSeen, 
                       stopped, arrived, overtaken, stale, dropped >>

Viewers == /\ \E w \in Watchers:
                \E c \in Chats \ {viewing[w]}:
                  /\ viewing' = [viewing EXCEPT ![w] = c]
                  /\ registered' = [registered EXCEPT ![w] = FALSE]
                  /\ buf' = [buf EXCEPT ![w] = <<>>]
                  /\ falseSeen' = [falseSeen EXCEPT ![w] = 0]
                  /\ IF KeyedWatch
                        THEN /\ entry' = [entry EXCEPT ![w] = NoEntry]
                        ELSE /\ TRUE
                             /\ entry' = entry
                  /\ newest' = (IF \E v \in Watchers \ {w} : registered'[v] /\ viewing'[v] = "c1" THEN newest ELSE 0)
           /\ UNCHANGED << draft, rep, sent, net, stopped, arrived, overtaken, 
                           stale, dropped >>

Network == /\ \E m \in net:
                /\ \/ /\ overtaken' = (overtaken \/ m.seq < arrived)
                      /\ arrived' = Max(arrived, m.seq)
                      /\ stopped' = IF m.typing THEN stopped ELSE Max(stopped, m.seq)
                      /\ IF ~Watched(registered, viewing)
                            THEN /\ TRUE
                                 /\ UNCHANGED << newest, buf, stale, dropped >>
                            ELSE /\ IF Ordered /\ m.seq <= newest
                                       THEN /\ stale' = TRUE
                                            /\ UNCHANGED << newest, buf, 
                                                            dropped >>
                                       ELSE /\ newest' = m.seq
                                            /\ buf' = [w \in Watchers |-> IF Hears(w, registered, viewing) /\ Len(buf[w]) < Buffer
                                                                             THEN Append(buf[w], [seq |-> m.seq, typing |-> m.typing])
                                                                             ELSE buf[w]]
                                            /\ dropped' = (dropped \/ \E w \in Watchers : Hears(w, registered, viewing) /\ Len(buf'[w]) >= Buffer)
                                            /\ stale' = stale
                   \/ /\ TRUE
                      /\ UNCHANGED <<newest, buf, stopped, arrived, overtaken, stale, dropped>>
                /\ net' = net \ {m}
           /\ UNCHANGED << draft, rep, sent, viewing, registered, entry, 
                           falseSeen >>

Watch(self) == /\ \/ /\ ~registered[self[2]]
                     /\ registered' = [registered EXCEPT ![self[2]] = TRUE]
                     /\ UNCHANGED <<buf, entry, falseSeen>>
                  \/ /\ buf[self[2]] # <<>>
                     /\ LET e == Head(buf[self[2]]) IN
                          IF e.typing
                             THEN /\ entry' = [entry EXCEPT ![self[2]] = [seq |-> e.seq, age |-> 0, chat |-> viewing[self[2]]]]
                                  /\ UNCHANGED falseSeen
                             ELSE /\ entry' = [entry EXCEPT ![self[2]] = NoEntry]
                                  /\ falseSeen' = [falseSeen EXCEPT ![self[2]] = Max(falseSeen[self[2]], e.seq)]
                     /\ buf' = [buf EXCEPT ![self[2]] = Tail(buf[self[2]])]
                     /\ UNCHANGED registered
               /\ UNCHANGED << draft, rep, sent, net, newest, viewing, stopped, 
                               arrived, overtaken, stale, dropped >>

Clock == /\ rep' = (IF rep = -1 THEN -1 ELSE Min(rep + 1, Throttle))
         /\ entry' = [w \in Watchers |-> IF entry[w] = NoEntry THEN NoEntry
                                          ELSE [entry[w] EXCEPT !.age = Min(@ + 1, Expiry)]]
         /\ UNCHANGED << draft, sent, net, newest, viewing, registered, buf, 
                         falseSeen, stopped, arrived, overtaken, stale, 
                         dropped >>

Next == Ana \/ Viewers \/ Network \/ Clock
           \/ (\E self \in {<<"watch", w>> : w \in Watchers}: Watch(self))

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Network)
        /\ \A self \in {<<"watch", w>> : w \in Watchers} : WF_vars(Watch(self))
        /\ WF_vars(Clock)

\* END TRANSLATION

---------------------------------------------------------------------------------
\* Requirements.

Reports == [seq : 1..MaxReports, typing : BOOLEAN]

TypeOK ==
    /\ draft \in BOOLEAN
    /\ rep \in {-1} \cup 0..Throttle
    /\ sent \in 0..MaxReports
    /\ net \subseteq Reports
    /\ newest \in 0..MaxReports
    /\ viewing \in [Watchers -> Chats]
    /\ registered \in [Watchers -> BOOLEAN]
    /\ \A w \in Watchers : Len(buf[w]) <= Buffer
    /\ entry \in [Watchers -> {NoEntry} \cup [seq : 1..MaxReports, age : 0..Expiry, chat : Chats]]
    /\ falseSeen \in [Watchers -> 0..MaxReports]
    /\ {stopped, arrived} \subseteq 0..MaxReports
    /\ {overtaken, stale, dropped} \subseteq BOOLEAN

\* The tab has reported typing exactly while the draft holds text.
ReportedWithText == draft = (rep # -1)

\* The server keeps a number only for a chat somebody watches.
NewestOnlyWatched == newest # 0 => Watched(registered, viewing)

\* Nobody sees their own typing, not even in another tab of theirs.
NoSelf == ~Shown("ana2")

\* A pill names someone typing in the chat on screen, never one Bea left.
NoCrossChat == Shown("bea") => entry["bea"].chat = viewing["bea"]

\* Once a watch heard that Ana stopped, it never shows her again from a typing report she made before.
NoStaleReturn == \A w \in Watchers : Shown(w) => entry[w].seq > falseSeen[w]

\* Once Ana's draft stays empty, Bea's pill for her goes, for good: whatever is dropped, late or lost.
PillClears == <>[](~draft) => <>[](~Shown("bea"))

---------------------------------------------------------------------------------
\* What the design does not promise, checked by a run that must find it violated: a watch never shows Ana from a
\* typing report older than a not-typing one the server took, once nothing later is on its way to it. A watch that
\* never hears that report breaks it: its events were full, or it opened after the server took the report. A typing
\* report she made before, arriving that late, shows her to that watch until it expires.
NoStaleAnywhere == \A w \in Watchers :
    (Shown(w) /\ \A i \in 1..Len(buf[w]) : buf[w][i].seq < entry[w].seq) => entry[w].seq > stopped

\* What must happen in at least one state. Each is checked by a run that must find its negation violated, so a
\* check that passes does not pass because the model never shows a pill, reorders reports, or drops anything.
NeverSeen == ~Shown("bea")
NeverOvertaken == ~overtaken
NeverStale == ~stale
NeverDropped == ~dropped
=================================================================================
