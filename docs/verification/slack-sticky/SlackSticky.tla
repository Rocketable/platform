----------------------------- MODULE SlackSticky -----------------------------
EXTENDS Integers, Sequences, FiniteSets

(* `sticky: slack` agents in one Slack channel, as designed (Mutation = "none"); each other Mutation turns one
   design rule off or keeps a behavior the current code has. README.md maps every step to the code that builds it.

   A channel has one Channel Session per agent. A Root Slack Mention queues into it. A turn there may call
   slack_respond_in_thread any number of times: false changes nothing, and the first true moves the rest of the
   turn into the Thread Session of the mention's thread, where the tool does not exist, so true is terminal.
   A mention inside a thread queues into that thread's Thread Session. A Thread Session starts no new turn while
   the thread's root mention still belongs to a channel turn; that hold is checked when a turn fires, so Web input
   sent to a held Thread Session waits too.

   Items are small integers with fixed roles, so a world is chosen by which of them exist:
     1, 2  Root Slack Mentions (their Slack message ts is the item number)
     3     a mention in the thread of root 1
     4     Web input a person sends into the Channel Session (posted at the top of the channel when it runs)
     5     another session's agent writing into the Channel Session (no Slack message of its own)
     6     a mention in the thread of the bot's top-level reply to root 2
     7     the channel agent writing into the Thread Session root 1 moved into, by the ID its note gave it
     8     Web input a person sends into the Thread Session of root 1's thread
   Slack message ts: a root mention's is its number, a posted Web input or thread root 100 + item, the bot's
   top-level reply to item i 200 + i. 0 is "the top of the channel". *)

CONSTANTS Agents, InitAgent, Roots, ThreadMentions, WebItems, AgentItems, ReplyMentions, ThreadWrites, ThreadWeb,
          Dups, MaxCalls, MaxCrashes, MaxStops, CfgFlips, KindFlips, Mutation

Mutations == {"none", "no-ratchet", "volatile-ratchet", "split-move", "no-hold", "no-root-first", "per-conv-dedup",
              "thread-only-lookup", "legacy-report", "no-start-check", "key-without-agent", "stop-by-conv",
              "settle-after-close", "no-footer-switch", "no-wake-on-release", "short-window"}

ASSUME InitAgent \in Agents
ASSUME Roots \subseteq {1, 2} /\ ThreadMentions \subseteq {3} /\ WebItems \subseteq {4} /\ AgentItems \subseteq {5}
ASSUME ReplyMentions \subseteq {6} /\ ThreadWrites \subseteq {7} /\ ThreadWeb \subseteq {8}
ASSUME ThreadMentions # {} => 1 \in Roots
ASSUME ReplyMentions # {} => 2 \in Roots
ASSUME ThreadWrites # {} => 1 \in Roots
ASSUME ThreadWeb # {} => 1 \in Roots
ASSUME Dups \in 0..2 /\ MaxCalls \in 1..3 /\ MaxCrashes \in 0..2 /\ MaxStops \in 0..2
ASSUME CfgFlips \in 0..2 /\ KindFlips \in 0..2
ASSUME Mutation \in Mutations

ThreadOf == 1
ReplyTo == 2
Mentions == Roots \cup ThreadMentions \cup ReplyMentions
Items == Mentions \cup WebItems \cup AgentItems \cup ThreadWrites \cup ThreadWeb
Top == 0
PostedRoot(i) == 100 + i
BotReply(i) == 200 + i
ThreadTs == Roots \cup {PostedRoot(i) : i \in WebItems \cup AgentItems} \cup {BotReply(ReplyTo) : m \in ReplyMentions}
SlackThread(m) == CASE m \in Roots -> m
                    [] m \in ThreadMentions -> ThreadOf
                    [] m \in ReplyMentions -> BotReply(ReplyTo)

\* A session's key: its kind, its thread (0 for a Channel Session) and its agent.
CS(a) == [kind |-> "ch", ts |-> Top, agent |-> a]
TS(t, a) == [kind |-> "th", ts |-> t, agent |-> a]
Keys == {CS(a) : a \in Agents} \cup {TS(t, a) : t \in ThreadTs, a \in Agents}
NoKey == [kind |-> "none", ts |-> Top, agent |-> "-"]
Proc(n) == [kind |-> n, ts |-> Top, agent |-> "-"]
ChanKey(a) == IF Mutation = "key-without-agent" THEN CS(InitAgent) ELSE CS(a)
KeyFor(m, a) == IF m \in Roots THEN ChanKey(a) ELSE TS(SlackThread(m), a)
Posts == Items \X {"root", "reply"}
Final == {"done", "stopped", "ignored", "dropped"}
FinalPhase(o) == CASE o = "answer" -> "done" [] o = "stopped" -> "stopped" [] o = "ignored" -> "ignored"

(* --algorithm SlackSticky
variables
    \* Configuration, live: the channel's agent and whether each agent is sticky: slack.
    cfg = InitAgent,
    slack = [a \in Agents |-> TRUE],
    cfgLeft = CfgFlips, kindLeft = KindFlips,
    \* Durable: conversation rows, Thread Queue and turn rows, the Step Journal, channel history notes.
    exists = [k \in Keys |-> FALSE],
    convAgent = [k \in Keys |-> k.agent],
    queue = [k \in Keys |-> <<>>],
    active = [k \in Keys |-> 0],
    phase = [i \in Items |-> "new"],
    outcome = [i \in Items |-> "answer"],
    agentOf = [i \in Items |-> InitAgent],
    home = [i \in Items |-> NoKey],
    rootTs = [i \in Items |-> Top],
    thread = [i \in Items |-> FALSE],
    calls = [i \in Items |-> 0],
    movedTo = [i \in Items |-> NoKey],
    notes = [k \in Keys |-> {}],
    seen = {},
    admits = [i \in Items |-> 0],
    rec = [p \in Posts |-> "none"],
    \* Slack: messages posted, statuses shown, events in flight.
    delivered = [m \in Mentions |-> 0],
    sent = [p \in Posts |-> 0],
    replyLoc = [i \in Items |-> -1],
    replyFooter = [i \in Items |-> NoKey],
    status = [t \in ThreadTs |-> FALSE],
    stopsLeft = MaxStops,
    \* In memory: lost by a crash.
    wake = [k \in Keys |-> FALSE],
    shown = [i \in Items |-> FALSE],
    inflight = [p \in Posts |-> FALSE],
    settled = [i \in Items |-> FALSE],
    noteDue = {},
    settleDue = {},
    stopEv = {},
    crashes = 0,
    \* Ghosts for the properties.
    asked = [i \in Items |-> FALSE],
    badStart = FALSE, stoppedWrong = FALSE, clobber = FALSE, earlyThread = FALSE, webHeld = FALSE, expired = FALSE;

define
    Seen(i, k) == IF Mutation = "per-conv-dedup" THEN <<i, k>> \in seen ELSE \E x \in seen : x[1] = i
    \* No delivery can name mention i again: Slack is done with it, and with every mention that admits it as a root.
    Quiet(i) == delivered[i] = 1 + Dups /\ (i = ThreadOf => \A m \in ThreadMentions : delivered[m] = 1 + Dups)
    \* The root mention t still belongs to a channel turn: admitted, not finished, not moved.
    RootUnsettled(t) == t \in Roots /\ phase[t] \in {"waiting", "running", "delivering"} /\ movedTo[t] = NoKey
    \* For the ghost: the same, or not yet admitted at all.
    RootPending(t) == t \in Roots /\ (phase[t] = "new" \/ RootUnsettled(t))
    \* A thread mention also admits its unadmitted root mention, first, in the same step.
    AdmitSet(m) == IF m \in ThreadMentions /\ Mutation # "no-root-first" /\ ~Seen(ThreadOf, ChanKey(cfg))
                   THEN {<<ThreadOf, ChanKey(cfg)>>, <<m, TS(ThreadOf, cfg)>>}
                   ELSE {<<m, KeyFor(m, cfg)>>}
    Visible(m) == m \in ReplyMentions => (sent[<<ReplyTo, "reply">>] > 0 /\ replyLoc[ReplyTo] = Top)
    NeedRoot(i, k) == rootTs[i] = Top /\ k.kind = "ch" /\ i \in WebItems
    \* The fire-time hold: a Thread Session starts nothing while its root mention belongs to a channel turn.
    Held(k) == k.kind = "th" /\ Mutation # "no-hold" /\ RootUnsettled(k.ts)
    Found(p) == sent[p] > 0 /\ (Mutation # "thread-only-lookup" \/ (p[2] = "reply" /\ replyLoc[p[1]] # Top))
    Loc(k) == IF k.kind = "th" THEN k.ts ELSE Top
    Footer(i, k) == IF Mutation = "no-footer-switch" THEN home[i] ELSE k
    OtherLive(t, i) == \E k \in Keys : active[k] \notin {0, i} /\ rootTs[active[k]] = t /\ phase[active[k]] = "running"
    Live(t) == \E k \in Keys : active[k] # 0 /\ rootTs[active[k]] = t /\ phase[active[k]] = "running"
    \* Waking the thread sessions a root held, and the given keys.
    Woken(T, K) == [k \in Keys |-> IF k \in K \/ (Mutation # "no-wake-on-release" /\ k.kind = "th" /\ k.ts \in T /\ exists[k])
                                   THEN TRUE ELSE wake[k]]
    InQ(i, k) == \E n \in 1..Len(queue[k]) : queue[k][n] = i
    ExpectedLoc(i) == IF movedTo[i] # NoKey THEN rootTs[i] ELSE Loc(home[i])
    ExpectedFooter(i) == IF movedTo[i] # NoKey THEN movedTo[i] ELSE home[i]

    \* Safety.
    \* An item is in at most one place: one session's running turn or one session's queue.
    OneOwner == \A i \in Items : \A k1, k2 \in Keys :
                    /\ (active[k1] = i /\ active[k2] = i) => k1 = k2
                    /\ active[k1] = i => ~InQ(i, k2)
                    /\ (InQ(i, k1) /\ InQ(i, k2)) => k1 = k2
    \* A Slack message is admitted at most once, whatever the configuration did between its deliveries.
    OneAdmission == \A i \in Items : admits[i] <= 1
    \* A move never lands on a thread session that is running another turn.
    NoClobber == ~clobber
    \* A thread session runs nothing while the thread's root mention still belongs to a channel turn.
    HeldUntilRootSettles == ~earlyThread
    \* Once the agent said true, its reply is never at the top of the channel.
    RatchetHonored == \A i \in Items : (sent[<<i, "reply">>] > 0 /\ asked[i]) => replyLoc[i] # Top
    \* A reply goes where its turn lives: the top of the channel, or the thread its session owns.
    ReplyWhereTurnLives == \A i \in Items : sent[<<i, "reply">>] > 0 => replyLoc[i] = ExpectedLoc(i)
    \* Nothing is posted twice, across crashes.
    OneReply == \A i \in Items : sent[<<i, "reply">>] <= 1
    OneRoot == \A i \in Items : sent[<<i, "root">>] <= 1
    \* A reply's footer opens the session that answered: the Thread Session after a move.
    FooterMatchesSession == \A i \in Items : sent[<<i, "reply">>] > 0 => replyFooter[i] = ExpectedFooter(i)
    \* Only a sticky: slack agent starts a turn; a turn resumed after a crash is not a new firing.
    OnlySlackAgentsRun == ~badStart
    \* A Slack Stop on a thread stops the turn that thread showed, and nothing else.
    StopPrecise == ~stoppedWrong
    \* A session's turns all run as the agent the session is for.
    SessionAgentFixed == \A k \in Keys : active[k] # 0 => agentOf[active[k]] = convAgent[k]

    \* Witnesses: each claims a path never happens; a witness check must find it, so the passes are not vacuous.
    \* A turn moved into its thread and finished there after a crash, and the held thread mention ran after it.
    NoMoveThenHeld == ~(movedTo[1] # NoKey /\ phase[1] = "done" /\ phase[3] = "done" /\ crashes > 0)
    \* A reply went to the top of the channel and a mention in that reply's thread was answered.
    NoReplyThread == ~(phase[6] = "done")
    \* Web input sent to a held Thread Session waited, and ran after the root's turn moved into that session.
    NoWebHeldAfterMove == ~(webHeld /\ movedTo[1] # NoKey /\ phase[8] = "done")
    \* A Stop ended a turn that had already moved into its thread.
    NoStopAfterMove == ~(\E i \in Items : phase[i] = "stopped" /\ movedTo[i] # NoKey)
    \* A turn fired while its agent was not sticky: slack, and was ignored.
    NoIgnoredAtPick == ~(\E i \in Items : phase[i] = "ignored" /\ admits[i] = 1)
    \* An admission record dropped out of the duplicate check's window.
    NoExpiry == ~expired

    \* Liveness, under the fairness in the Spec: everything but people, configuration and crashes is weakly fair.
    \* Every queued item finishes.
    Settles == \A i \in Items : phase[i] = "waiting" ~> phase[i] \in Final
    \* Every mention Slack delivered is answered, stopped, or ignored when its turn fired (never dropped).
    MentionsAnswered == \A m \in Mentions : delivered[m] > 0 ~> phase[m] \in {"done", "stopped", "ignored"}
    \* No thread is left showing working without a running turn.
    StatusClears == <>[](\A t \in ThreadTs : status[t] => Live(t))
    \* A Channel Session learns the ID of every Thread Session its turns moved into.
    ChannelKnowsThread == \A i \in Items : movedTo[i] # NoKey ~> (home[i] \in Keys /\ movedTo[i] \in notes[home[i]])
end define;

\* One session's Bridge: its loop, its turn, its delivery. One label: each branch is one atomic step.
fair process Lane \in Keys
begin
Run:
    while TRUE do
        either
            \* pickLaterWork and activateInbound: take the head of the queue, consume the wake when held or empty.
            await exists[self] /\ wake[self] /\ active[self] = 0;
            if Held(self) \/ queue[self] = <<>> then
                wake[self] := FALSE;
            else
                with i = Head(queue[self]) do
                    queue[self] := Tail(queue[self]);
                    active[self] := i;
                    if ~slack[convAgent[self]] /\ Mutation # "no-start-check" then
                        \* The one sticky: slack check, when the turn fires: warn and move on; the status still settles.
                        phase[i] := "delivering";
                        outcome[i] := "ignored";
                    else
                        phase[i] := "running";
                        badStart := badStart \/ ~slack[convAgent[self]];
                        earlyThread := earlyThread \/ (self.kind = "th" /\ RootPending(self.ts));
                    end if;
                end with;
            end if;
        or
            \* postOnce for the turn's own root message: a Web input, or an agent item's thread root.
            await active[self] # 0;
            with i = active[self], p = <<active[self], "root">> do
                await phase[i] \in {"running", "delivering"} /\ NeedRoot(i, self) /\ rec[p] = "none";
                rec[p] := "intent";
            end with;
        or
            await active[self] # 0;
            with i = active[self], p = <<active[self], "root">> do
                await phase[i] \in {"running", "delivering"} /\ NeedRoot(i, self) /\ rec[p] = "intent" /\ ~inflight[p];
                if Found(p) then
                    rec[p] := "done";
                    rootTs[i] := PostedRoot(i);
                else
                    sent[p] := sent[p] + 1;
                    inflight[p] := TRUE;
                end if;
            end with;
        or
            await active[self] # 0;
            with i = active[self], p = <<active[self], "root">> do
                await phase[i] \in {"running", "delivering"} /\ NeedRoot(i, self) /\ rec[p] = "intent" /\ inflight[p];
                rec[p] := "done";
                rootTs[i] := PostedRoot(i);
            end with;
        or
            \* startSession: the native working status on the turn's thread.
            await active[self] # 0;
            with i = active[self] do
                await phase[i] = "running" /\ rootTs[i] # Top /\ ~shown[i];
                status[rootTs[i]] := TRUE;
                shown[i] := TRUE;
            end with;
        or
            \* slack_respond_in_thread, only on a Channel Session turn a Slack mention started: one model step's calls.
            await self.kind = "ch" /\ active[self] # 0;
            with i = active[self], s \in {{FALSE}, {TRUE}, {TRUE, FALSE}} do
                await phase[i] = "running" /\ i \in Roots /\ calls[i] < MaxCalls /\ ~thread[i];
                calls[i] := calls[i] + 1;
                asked[i] := asked[i] \/ TRUE \in s;
                if Mutation = "no-ratchet" then
                    with last \in s do thread[i] := last; end with;
                else
                    thread[i] := TRUE \in s;
                end if;
            end with;
        or
            \* The move: one transaction gives the turn to the Thread Session, frees the channel, notes the ID.
            await self.kind = "ch" /\ active[self] # 0;
            with i = active[self], tk = TS(rootTs[active[self]], agentOf[active[self]]) do
                await phase[i] \in {"running", "delivering"} /\ thread[i] /\ rootTs[i] # Top;
                clobber := clobber \/ active[tk] # 0;
                active := [active EXCEPT ![self] = 0, ![tk] = i];
                exists[tk] := TRUE;
                wake := Woken({rootTs[i]}, {self});
                movedTo[i] := tk;
                if Mutation = "split-move" then
                    noteDue := noteDue \cup {<<self, tk>>};
                else
                    notes[self] := notes[self] \cup {tk};
                end if;
            end with;
        or
            \* split-move only: the note written after the move, from memory.
            await \E n \in noteDue : n[1] = self;
            with n \in {x \in noteDue : x[1] = self} do
                notes[self] := notes[self] \cup {n[2]};
                noteDue := noteDue \ {n};
            end with;
        or
            \* finish: the answer is ready.
            await active[self] # 0;
            with i = active[self] do
                await /\ phase[i] = "running"
                      /\ (self.kind = "th" \/ ~thread[i])
                      /\ ~NeedRoot(i, self)
                      /\ (shown[i] \/ rootTs[i] = Top);
                phase[i] := "delivering";
                outcome[i] := "answer";
            end with;
        or
            \* postOnce for the reply (or a stopped turn's footer): marker, post, record.
            await active[self] # 0;
            with i = active[self], p = <<active[self], "reply">> do
                await /\ phase[i] = "delivering" /\ outcome[i] # "ignored" /\ rec[p] = "none"
                      /\ (self.kind = "th" \/ ~thread[i]);
                rec[p] := "intent";
            end with;
        or
            await active[self] # 0;
            with i = active[self], p = <<active[self], "reply">> do
                await phase[i] = "delivering" /\ rec[p] = "intent" /\ ~inflight[p];
                if Found(p) then
                    rec[p] := "done";
                else
                    sent[p] := sent[p] + 1;
                    inflight[p] := TRUE;
                    replyLoc[i] := Loc(self);
                    replyFooter[i] := Footer(i, self);
                end if;
            end with;
        or
            await active[self] # 0;
            with i = active[self], p = <<active[self], "reply">> do
                await phase[i] = "delivering" /\ rec[p] = "intent" /\ inflight[p];
                rec[p] := "done";
            end with;
        or
            \* finishResponse: settle the thread's status unless another turn there is running.
            await active[self] # 0 /\ Mutation # "settle-after-close";
            with i = active[self] do
                await /\ phase[i] = "delivering" /\ ~settled[i]
                      /\ (outcome[i] = "ignored" \/ rec[<<i, "reply">>] = "done");
                if rootTs[i] # Top /\ ~OtherLive(rootTs[i], i) then
                    status[rootTs[i]] := FALSE;
                end if;
                settled[i] := TRUE;
            end with;
        or
            \* closeTurn: the row is done; a channel turn's root releases its threads.
            await active[self] # 0;
            with i = active[self] do
                await /\ phase[i] = "delivering"
                      /\ (outcome[i] = "ignored" \/ rec[<<i, "reply">>] = "done")
                      /\ (settled[i] \/ Mutation = "settle-after-close");
                phase[i] := FinalPhase(outcome[i]);
                active[self] := 0;
                if self.kind = "ch" then
                    wake := Woken({rootTs[i]}, {self});
                else
                    wake[self] := TRUE;
                end if;
                if Mutation = "settle-after-close" /\ rootTs[i] # Top then
                    settleDue := settleDue \cup {rootTs[i]};
                end if;
            end with;
        end either;
    end while;
end process;

\* settle-after-close only: the status settles from memory after the row closed.
fair process Settler = Proc("settler")
begin
Settle:
    while TRUE do
        await settleDue # {};
        with t \in settleDue do
            if ~Live(t) then status[t] := FALSE; end if;
            settleDue := settleDue \ {t};
        end with;
    end while;
end process;

\* The socket handler: Slack delivers each mention at least once, maybe again, in any order.
fair process Slack = Proc("slack")
begin
Deliver:
    while TRUE do
        with m \in {x \in Mentions : delivered[x] < 1 + Dups /\ Visible(x)} do
            delivered[m] := delivered[m] + 1;
            if m \in ReplyMentions /\ Mutation = "legacy-report" then
                \* A thread whose root the bot posted is read as a Report Thread.
                if phase[m] = "new" then phase[m] := "dropped"; end if;
            elsif Seen(m, KeyFor(m, cfg)) then
                skip;
            else
                with A = AdmitSet(m), its = {x[1] : x \in AdmitSet(m)}, ks = {x[2] : x \in AdmitSet(m)} do
                    seen := seen \cup A;
                    admits := [i \in Items |-> IF i \in its THEN admits[i] + 1 ELSE admits[i]];
                    phase := [i \in Items |-> IF i \in its THEN "waiting" ELSE phase[i]];
                    agentOf := [i \in Items |-> IF i \in its THEN cfg ELSE agentOf[i]];
                    home := [i \in Items |-> IF i \in its THEN (CHOOSE x \in A : x[1] = i)[2] ELSE home[i]];
                    rootTs := [i \in Items |-> IF i \in its THEN SlackThread(i) ELSE rootTs[i]];
                    queue := [k \in Keys |-> IF k \in ks THEN Append(queue[k], (CHOOSE x \in A : x[2] = k)[1])
                                             ELSE queue[k]];
                    convAgent := [k \in Keys |-> IF k \in ks /\ ~exists[k] THEN cfg ELSE convAgent[k]];
                    wake := [k \in Keys |-> wake[k] \/ k \in ks];
                    exists := [k \in Keys |-> exists[k] \/ k \in ks];
                    status := [t \in ThreadTs |-> status[t] \/ t \in {SlackThread(i) : i \in its}];
                end with;
            end if;
        end with;
    end while;
end process;

\* The duplicate check reads only the last day of saved history, so an admission record drops out of it. A day
\* outlasts Slack's redeliveries and the root-first window, so a record drops out only once its mention is Quiet.
process Window = Proc("window")
begin
Expire:
    while TRUE do
        with x \in {r \in seen : Mutation = "short-window" \/ Quiet(r[1])} do
            seen := seen \ {x};
            expired := TRUE;
        end with;
    end while;
end process;

\* Web input and other sessions' agents write into sessions they can see.
fair process Submit = Proc("submit")
begin
Write:
    while TRUE do
        with i \in {x \in WebItems \cup AgentItems \cup ThreadWrites \cup ThreadWeb : phase[x] = "new"},
             k \in CASE i \in ThreadWrites -> UNION {notes[c] : c \in Keys}
                     [] i \in ThreadWeb -> {c \in Keys : c.kind = "th" /\ c.ts = ThreadOf /\ exists[c]}
                     [] OTHER -> {c \in Keys : c.kind = "ch" /\ exists[c]} do
            webHeld := webHeld \/ (i \in ThreadWeb /\ RootUnsettled(ThreadOf));
            queue[k] := Append(queue[k], i);
            phase[i] := "waiting";
            agentOf[i] := convAgent[k];
            home[i] := k;
            rootTs[i] := Loc(k);
            wake[k] := TRUE;
        end with;
    end while;
end process;

\* A person presses Stop on a thread showing working; the handler finds the session's turn.
process StopPress = Proc("stoppress")
begin
Press:
    while TRUE do
        await stopsLeft > 0;
        with e \in {x \in Items \X Keys : active[x[2]] = x[1] /\ phase[x[1]] = "running" /\ shown[x[1]]
                                          /\ status[rootTs[x[1]]]} do
            stopEv := stopEv \cup {e};
            stopsLeft := stopsLeft - 1;
        end with;
    end while;
end process;

\* InterruptConversation, later: stop the turn the press found, wherever it now runs.
fair process StopDeliver = Proc("stopdeliver")
begin
Interrupt:
    while TRUE do
        with e \in stopEv do
            with tk \in IF Mutation = "stop-by-conv" THEN {e[2]}
                        ELSE IF \E k \in Keys : active[k] = e[1] THEN {k \in Keys : active[k] = e[1]} ELSE {NoKey} do
                if tk # NoKey /\ active[tk] # 0 /\ phase[active[tk]] = "running" then
                    phase[active[tk]] := "delivering";
                    outcome[active[tk]] := "stopped";
                    stoppedWrong := stoppedWrong \/ active[tk] # e[1];
                end if;
            end with;
            stopEv := stopEv \ {e};
        end with;
    end while;
end process;

\* The Config page and agent files change between any two steps.
process Config = Proc("config")
begin
Change:
    while TRUE do
        either
            await cfgLeft > 0;
            with a \in Agents \ {cfg} do cfg := a; end with;
            cfgLeft := cfgLeft - 1;
        or
            await kindLeft > 0;
            with a \in Agents do slack[a] := ~slack[a]; end with;
            kindLeft := kindLeft - 1;
        end either;
    end while;
end process;

\* A crash and the restart: memory is lost; StartActiveTurns resumes running rows, StartQueuedConversations wakes.
process Lifecycle = Proc("lifecycle")
begin
Crash:
    while TRUE do
        await crashes < MaxCrashes;
        crashes := crashes + 1;
        wake := [k \in Keys |-> exists[k] /\ queue[k] # <<>>];
        shown := [i \in Items |-> FALSE];
        inflight := [p \in Posts |-> FALSE];
        settled := [i \in Items |-> FALSE];
        noteDue := {};
        settleDue := {};
        stopEv := {};
        if Mutation = "volatile-ratchet" then
            thread := [i \in Items |-> thread[i] /\ movedTo[i] # NoKey];
        end if;
    end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES cfg, slack, cfgLeft, kindLeft, exists, convAgent, queue, active, 
          phase, outcome, agentOf, home, rootTs, thread, calls, movedTo, 
          notes, seen, admits, rec, delivered, sent, replyLoc, replyFooter, 
          status, stopsLeft, wake, shown, inflight, settled, noteDue, 
          settleDue, stopEv, crashes, asked, badStart, stoppedWrong, clobber, 
          earlyThread, webHeld, expired

(* define statement *)
Seen(i, k) == IF Mutation = "per-conv-dedup" THEN <<i, k>> \in seen ELSE \E x \in seen : x[1] = i

Quiet(i) == delivered[i] = 1 + Dups /\ (i = ThreadOf => \A m \in ThreadMentions : delivered[m] = 1 + Dups)

RootUnsettled(t) == t \in Roots /\ phase[t] \in {"waiting", "running", "delivering"} /\ movedTo[t] = NoKey

RootPending(t) == t \in Roots /\ (phase[t] = "new" \/ RootUnsettled(t))

AdmitSet(m) == IF m \in ThreadMentions /\ Mutation # "no-root-first" /\ ~Seen(ThreadOf, ChanKey(cfg))
               THEN {<<ThreadOf, ChanKey(cfg)>>, <<m, TS(ThreadOf, cfg)>>}
               ELSE {<<m, KeyFor(m, cfg)>>}
Visible(m) == m \in ReplyMentions => (sent[<<ReplyTo, "reply">>] > 0 /\ replyLoc[ReplyTo] = Top)
NeedRoot(i, k) == rootTs[i] = Top /\ k.kind = "ch" /\ i \in WebItems

Held(k) == k.kind = "th" /\ Mutation # "no-hold" /\ RootUnsettled(k.ts)
Found(p) == sent[p] > 0 /\ (Mutation # "thread-only-lookup" \/ (p[2] = "reply" /\ replyLoc[p[1]] # Top))
Loc(k) == IF k.kind = "th" THEN k.ts ELSE Top
Footer(i, k) == IF Mutation = "no-footer-switch" THEN home[i] ELSE k
OtherLive(t, i) == \E k \in Keys : active[k] \notin {0, i} /\ rootTs[active[k]] = t /\ phase[active[k]] = "running"
Live(t) == \E k \in Keys : active[k] # 0 /\ rootTs[active[k]] = t /\ phase[active[k]] = "running"

Woken(T, K) == [k \in Keys |-> IF k \in K \/ (Mutation # "no-wake-on-release" /\ k.kind = "th" /\ k.ts \in T /\ exists[k])
                               THEN TRUE ELSE wake[k]]
InQ(i, k) == \E n \in 1..Len(queue[k]) : queue[k][n] = i
ExpectedLoc(i) == IF movedTo[i] # NoKey THEN rootTs[i] ELSE Loc(home[i])
ExpectedFooter(i) == IF movedTo[i] # NoKey THEN movedTo[i] ELSE home[i]



OneOwner == \A i \in Items : \A k1, k2 \in Keys :
                /\ (active[k1] = i /\ active[k2] = i) => k1 = k2
                /\ active[k1] = i => ~InQ(i, k2)
                /\ (InQ(i, k1) /\ InQ(i, k2)) => k1 = k2

OneAdmission == \A i \in Items : admits[i] <= 1

NoClobber == ~clobber

HeldUntilRootSettles == ~earlyThread

RatchetHonored == \A i \in Items : (sent[<<i, "reply">>] > 0 /\ asked[i]) => replyLoc[i] # Top

ReplyWhereTurnLives == \A i \in Items : sent[<<i, "reply">>] > 0 => replyLoc[i] = ExpectedLoc(i)

OneReply == \A i \in Items : sent[<<i, "reply">>] <= 1
OneRoot == \A i \in Items : sent[<<i, "root">>] <= 1

FooterMatchesSession == \A i \in Items : sent[<<i, "reply">>] > 0 => replyFooter[i] = ExpectedFooter(i)

OnlySlackAgentsRun == ~badStart

StopPrecise == ~stoppedWrong

SessionAgentFixed == \A k \in Keys : active[k] # 0 => agentOf[active[k]] = convAgent[k]



NoMoveThenHeld == ~(movedTo[1] # NoKey /\ phase[1] = "done" /\ phase[3] = "done" /\ crashes > 0)

NoReplyThread == ~(phase[6] = "done")

NoWebHeldAfterMove == ~(webHeld /\ movedTo[1] # NoKey /\ phase[8] = "done")

NoStopAfterMove == ~(\E i \in Items : phase[i] = "stopped" /\ movedTo[i] # NoKey)

NoIgnoredAtPick == ~(\E i \in Items : phase[i] = "ignored" /\ admits[i] = 1)

NoExpiry == ~expired



Settles == \A i \in Items : phase[i] = "waiting" ~> phase[i] \in Final

MentionsAnswered == \A m \in Mentions : delivered[m] > 0 ~> phase[m] \in {"done", "stopped", "ignored"}

StatusClears == <>[](\A t \in ThreadTs : status[t] => Live(t))

ChannelKnowsThread == \A i \in Items : movedTo[i] # NoKey ~> (home[i] \in Keys /\ movedTo[i] \in notes[home[i]])


vars == << cfg, slack, cfgLeft, kindLeft, exists, convAgent, queue, active, 
           phase, outcome, agentOf, home, rootTs, thread, calls, movedTo, 
           notes, seen, admits, rec, delivered, sent, replyLoc, replyFooter, 
           status, stopsLeft, wake, shown, inflight, settled, noteDue, 
           settleDue, stopEv, crashes, asked, badStart, stoppedWrong, clobber, 
           earlyThread, webHeld, expired >>

ProcSet == (Keys) \cup {Proc("settler")} \cup {Proc("slack")} \cup {Proc("window")} \cup {Proc("submit")} \cup {Proc("stoppress")} \cup {Proc("stopdeliver")} \cup {Proc("config")} \cup {Proc("lifecycle")}

Init == (* Global variables *)
        /\ cfg = InitAgent
        /\ slack = [a \in Agents |-> TRUE]
        /\ cfgLeft = CfgFlips
        /\ kindLeft = KindFlips
        /\ exists = [k \in Keys |-> FALSE]
        /\ convAgent = [k \in Keys |-> k.agent]
        /\ queue = [k \in Keys |-> <<>>]
        /\ active = [k \in Keys |-> 0]
        /\ phase = [i \in Items |-> "new"]
        /\ outcome = [i \in Items |-> "answer"]
        /\ agentOf = [i \in Items |-> InitAgent]
        /\ home = [i \in Items |-> NoKey]
        /\ rootTs = [i \in Items |-> Top]
        /\ thread = [i \in Items |-> FALSE]
        /\ calls = [i \in Items |-> 0]
        /\ movedTo = [i \in Items |-> NoKey]
        /\ notes = [k \in Keys |-> {}]
        /\ seen = {}
        /\ admits = [i \in Items |-> 0]
        /\ rec = [p \in Posts |-> "none"]
        /\ delivered = [m \in Mentions |-> 0]
        /\ sent = [p \in Posts |-> 0]
        /\ replyLoc = [i \in Items |-> -1]
        /\ replyFooter = [i \in Items |-> NoKey]
        /\ status = [t \in ThreadTs |-> FALSE]
        /\ stopsLeft = MaxStops
        /\ wake = [k \in Keys |-> FALSE]
        /\ shown = [i \in Items |-> FALSE]
        /\ inflight = [p \in Posts |-> FALSE]
        /\ settled = [i \in Items |-> FALSE]
        /\ noteDue = {}
        /\ settleDue = {}
        /\ stopEv = {}
        /\ crashes = 0
        /\ asked = [i \in Items |-> FALSE]
        /\ badStart = FALSE
        /\ stoppedWrong = FALSE
        /\ clobber = FALSE
        /\ earlyThread = FALSE
        /\ webHeld = FALSE
        /\ expired = FALSE

Lane(self) == /\ \/ /\ exists[self] /\ wake[self] /\ active[self] = 0
                    /\ IF Held(self) \/ queue[self] = <<>>
                          THEN /\ wake' = [wake EXCEPT ![self] = FALSE]
                               /\ UNCHANGED << queue, active, phase, outcome, 
                                               badStart, earlyThread >>
                          ELSE /\ LET i == Head(queue[self]) IN
                                    /\ queue' = [queue EXCEPT ![self] = Tail(queue[self])]
                                    /\ active' = [active EXCEPT ![self] = i]
                                    /\ IF ~slack[convAgent[self]] /\ Mutation # "no-start-check"
                                          THEN /\ phase' = [phase EXCEPT ![i] = "delivering"]
                                               /\ outcome' = [outcome EXCEPT ![i] = "ignored"]
                                               /\ UNCHANGED << badStart, 
                                                               earlyThread >>
                                          ELSE /\ phase' = [phase EXCEPT ![i] = "running"]
                                               /\ badStart' = (badStart \/ ~slack[convAgent[self]])
                                               /\ earlyThread' = (earlyThread \/ (self.kind = "th" /\ RootPending(self.ts)))
                                               /\ UNCHANGED outcome
                               /\ wake' = wake
                    /\ UNCHANGED <<exists, rootTs, thread, calls, movedTo, notes, rec, sent, replyLoc, replyFooter, status, shown, inflight, settled, noteDue, settleDue, asked, clobber>>
                 \/ /\ active[self] # 0
                    /\ LET i == active[self] IN
                         LET p == <<active[self], "root">> IN
                           /\ phase[i] \in {"running", "delivering"} /\ NeedRoot(i, self) /\ rec[p] = "none"
                           /\ rec' = [rec EXCEPT ![p] = "intent"]
                    /\ UNCHANGED <<exists, queue, active, phase, outcome, rootTs, thread, calls, movedTo, notes, sent, replyLoc, replyFooter, status, wake, shown, inflight, settled, noteDue, settleDue, asked, badStart, clobber, earlyThread>>
                 \/ /\ active[self] # 0
                    /\ LET i == active[self] IN
                         LET p == <<active[self], "root">> IN
                           /\ phase[i] \in {"running", "delivering"} /\ NeedRoot(i, self) /\ rec[p] = "intent" /\ ~inflight[p]
                           /\ IF Found(p)
                                 THEN /\ rec' = [rec EXCEPT ![p] = "done"]
                                      /\ rootTs' = [rootTs EXCEPT ![i] = PostedRoot(i)]
                                      /\ UNCHANGED << sent, inflight >>
                                 ELSE /\ sent' = [sent EXCEPT ![p] = sent[p] + 1]
                                      /\ inflight' = [inflight EXCEPT ![p] = TRUE]
                                      /\ UNCHANGED << rootTs, rec >>
                    /\ UNCHANGED <<exists, queue, active, phase, outcome, thread, calls, movedTo, notes, replyLoc, replyFooter, status, wake, shown, settled, noteDue, settleDue, asked, badStart, clobber, earlyThread>>
                 \/ /\ active[self] # 0
                    /\ LET i == active[self] IN
                         LET p == <<active[self], "root">> IN
                           /\ phase[i] \in {"running", "delivering"} /\ NeedRoot(i, self) /\ rec[p] = "intent" /\ inflight[p]
                           /\ rec' = [rec EXCEPT ![p] = "done"]
                           /\ rootTs' = [rootTs EXCEPT ![i] = PostedRoot(i)]
                    /\ UNCHANGED <<exists, queue, active, phase, outcome, thread, calls, movedTo, notes, sent, replyLoc, replyFooter, status, wake, shown, inflight, settled, noteDue, settleDue, asked, badStart, clobber, earlyThread>>
                 \/ /\ active[self] # 0
                    /\ LET i == active[self] IN
                         /\ phase[i] = "running" /\ rootTs[i] # Top /\ ~shown[i]
                         /\ status' = [status EXCEPT ![rootTs[i]] = TRUE]
                         /\ shown' = [shown EXCEPT ![i] = TRUE]
                    /\ UNCHANGED <<exists, queue, active, phase, outcome, rootTs, thread, calls, movedTo, notes, rec, sent, replyLoc, replyFooter, wake, inflight, settled, noteDue, settleDue, asked, badStart, clobber, earlyThread>>
                 \/ /\ self.kind = "ch" /\ active[self] # 0
                    /\ LET i == active[self] IN
                         \E s \in {{FALSE}, {TRUE}, {TRUE, FALSE}}:
                           /\ phase[i] = "running" /\ i \in Roots /\ calls[i] < MaxCalls /\ ~thread[i]
                           /\ calls' = [calls EXCEPT ![i] = calls[i] + 1]
                           /\ asked' = [asked EXCEPT ![i] = asked[i] \/ TRUE \in s]
                           /\ IF Mutation = "no-ratchet"
                                 THEN /\ \E last \in s:
                                           thread' = [thread EXCEPT ![i] = last]
                                 ELSE /\ thread' = [thread EXCEPT ![i] = TRUE \in s]
                    /\ UNCHANGED <<exists, queue, active, phase, outcome, rootTs, movedTo, notes, rec, sent, replyLoc, replyFooter, status, wake, shown, inflight, settled, noteDue, settleDue, badStart, clobber, earlyThread>>
                 \/ /\ self.kind = "ch" /\ active[self] # 0
                    /\ LET i == active[self] IN
                         LET tk == TS(rootTs[active[self]], agentOf[active[self]]) IN
                           /\ phase[i] \in {"running", "delivering"} /\ thread[i] /\ rootTs[i] # Top
                           /\ clobber' = (clobber \/ active[tk] # 0)
                           /\ active' = [active EXCEPT ![self] = 0, ![tk] = i]
                           /\ exists' = [exists EXCEPT ![tk] = TRUE]
                           /\ wake' = Woken({rootTs[i]}, {self})
                           /\ movedTo' = [movedTo EXCEPT ![i] = tk]
                           /\ IF Mutation = "split-move"
                                 THEN /\ noteDue' = (noteDue \cup {<<self, tk>>})
                                      /\ notes' = notes
                                 ELSE /\ notes' = [notes EXCEPT ![self] = notes[self] \cup {tk}]
                                      /\ UNCHANGED noteDue
                    /\ UNCHANGED <<queue, phase, outcome, rootTs, thread, calls, rec, sent, replyLoc, replyFooter, status, shown, inflight, settled, settleDue, asked, badStart, earlyThread>>
                 \/ /\ \E n \in noteDue : n[1] = self
                    /\ \E n \in {x \in noteDue : x[1] = self}:
                         /\ notes' = [notes EXCEPT ![self] = notes[self] \cup {n[2]}]
                         /\ noteDue' = noteDue \ {n}
                    /\ UNCHANGED <<exists, queue, active, phase, outcome, rootTs, thread, calls, movedTo, rec, sent, replyLoc, replyFooter, status, wake, shown, inflight, settled, settleDue, asked, badStart, clobber, earlyThread>>
                 \/ /\ active[self] # 0
                    /\ LET i == active[self] IN
                         /\ /\ phase[i] = "running"
                            /\ (self.kind = "th" \/ ~thread[i])
                            /\ ~NeedRoot(i, self)
                            /\ (shown[i] \/ rootTs[i] = Top)
                         /\ phase' = [phase EXCEPT ![i] = "delivering"]
                         /\ outcome' = [outcome EXCEPT ![i] = "answer"]
                    /\ UNCHANGED <<exists, queue, active, rootTs, thread, calls, movedTo, notes, rec, sent, replyLoc, replyFooter, status, wake, shown, inflight, settled, noteDue, settleDue, asked, badStart, clobber, earlyThread>>
                 \/ /\ active[self] # 0
                    /\ LET i == active[self] IN
                         LET p == <<active[self], "reply">> IN
                           /\ /\ phase[i] = "delivering" /\ outcome[i] # "ignored" /\ rec[p] = "none"
                              /\ (self.kind = "th" \/ ~thread[i])
                           /\ rec' = [rec EXCEPT ![p] = "intent"]
                    /\ UNCHANGED <<exists, queue, active, phase, outcome, rootTs, thread, calls, movedTo, notes, sent, replyLoc, replyFooter, status, wake, shown, inflight, settled, noteDue, settleDue, asked, badStart, clobber, earlyThread>>
                 \/ /\ active[self] # 0
                    /\ LET i == active[self] IN
                         LET p == <<active[self], "reply">> IN
                           /\ phase[i] = "delivering" /\ rec[p] = "intent" /\ ~inflight[p]
                           /\ IF Found(p)
                                 THEN /\ rec' = [rec EXCEPT ![p] = "done"]
                                      /\ UNCHANGED << sent, replyLoc, 
                                                      replyFooter, inflight >>
                                 ELSE /\ sent' = [sent EXCEPT ![p] = sent[p] + 1]
                                      /\ inflight' = [inflight EXCEPT ![p] = TRUE]
                                      /\ replyLoc' = [replyLoc EXCEPT ![i] = Loc(self)]
                                      /\ replyFooter' = [replyFooter EXCEPT ![i] = Footer(i, self)]
                                      /\ rec' = rec
                    /\ UNCHANGED <<exists, queue, active, phase, outcome, rootTs, thread, calls, movedTo, notes, status, wake, shown, settled, noteDue, settleDue, asked, badStart, clobber, earlyThread>>
                 \/ /\ active[self] # 0
                    /\ LET i == active[self] IN
                         LET p == <<active[self], "reply">> IN
                           /\ phase[i] = "delivering" /\ rec[p] = "intent" /\ inflight[p]
                           /\ rec' = [rec EXCEPT ![p] = "done"]
                    /\ UNCHANGED <<exists, queue, active, phase, outcome, rootTs, thread, calls, movedTo, notes, sent, replyLoc, replyFooter, status, wake, shown, inflight, settled, noteDue, settleDue, asked, badStart, clobber, earlyThread>>
                 \/ /\ active[self] # 0 /\ Mutation # "settle-after-close"
                    /\ LET i == active[self] IN
                         /\ /\ phase[i] = "delivering" /\ ~settled[i]
                            /\ (outcome[i] = "ignored" \/ rec[<<i, "reply">>] = "done")
                         /\ IF rootTs[i] # Top /\ ~OtherLive(rootTs[i], i)
                               THEN /\ status' = [status EXCEPT ![rootTs[i]] = FALSE]
                               ELSE /\ TRUE
                                    /\ UNCHANGED status
                         /\ settled' = [settled EXCEPT ![i] = TRUE]
                    /\ UNCHANGED <<exists, queue, active, phase, outcome, rootTs, thread, calls, movedTo, notes, rec, sent, replyLoc, replyFooter, wake, shown, inflight, noteDue, settleDue, asked, badStart, clobber, earlyThread>>
                 \/ /\ active[self] # 0
                    /\ LET i == active[self] IN
                         /\ /\ phase[i] = "delivering"
                            /\ (outcome[i] = "ignored" \/ rec[<<i, "reply">>] = "done")
                            /\ (settled[i] \/ Mutation = "settle-after-close")
                         /\ phase' = [phase EXCEPT ![i] = FinalPhase(outcome[i])]
                         /\ active' = [active EXCEPT ![self] = 0]
                         /\ IF self.kind = "ch"
                               THEN /\ wake' = Woken({rootTs[i]}, {self})
                               ELSE /\ wake' = [wake EXCEPT ![self] = TRUE]
                         /\ IF Mutation = "settle-after-close" /\ rootTs[i] # Top
                               THEN /\ settleDue' = (settleDue \cup {rootTs[i]})
                               ELSE /\ TRUE
                                    /\ UNCHANGED settleDue
                    /\ UNCHANGED <<exists, queue, outcome, rootTs, thread, calls, movedTo, notes, rec, sent, replyLoc, replyFooter, status, shown, inflight, settled, noteDue, asked, badStart, clobber, earlyThread>>
              /\ UNCHANGED << cfg, slack, cfgLeft, kindLeft, convAgent, 
                              agentOf, home, seen, admits, delivered, 
                              stopsLeft, stopEv, crashes, stoppedWrong, 
                              webHeld, expired >>

Settler == /\ settleDue # {}
           /\ \E t \in settleDue:
                /\ IF ~Live(t)
                      THEN /\ status' = [status EXCEPT ![t] = FALSE]
                      ELSE /\ TRUE
                           /\ UNCHANGED status
                /\ settleDue' = settleDue \ {t}
           /\ UNCHANGED << cfg, slack, cfgLeft, kindLeft, exists, convAgent, 
                           queue, active, phase, outcome, agentOf, home, 
                           rootTs, thread, calls, movedTo, notes, seen, admits, 
                           rec, delivered, sent, replyLoc, replyFooter, 
                           stopsLeft, wake, shown, inflight, settled, noteDue, 
                           stopEv, crashes, asked, badStart, stoppedWrong, 
                           clobber, earlyThread, webHeld, expired >>

Slack == /\ \E m \in {x \in Mentions : delivered[x] < 1 + Dups /\ Visible(x)}:
              /\ delivered' = [delivered EXCEPT ![m] = delivered[m] + 1]
              /\ IF m \in ReplyMentions /\ Mutation = "legacy-report"
                    THEN /\ IF phase[m] = "new"
                               THEN /\ phase' = [phase EXCEPT ![m] = "dropped"]
                               ELSE /\ TRUE
                                    /\ phase' = phase
                         /\ UNCHANGED << exists, convAgent, queue, agentOf, 
                                         home, rootTs, seen, admits, status, 
                                         wake >>
                    ELSE /\ IF Seen(m, KeyFor(m, cfg))
                               THEN /\ TRUE
                                    /\ UNCHANGED << exists, convAgent, queue, 
                                                    phase, agentOf, home, 
                                                    rootTs, seen, admits, 
                                                    status, wake >>
                               ELSE /\ LET A == AdmitSet(m) IN
                                         LET its == {x[1] : x \in AdmitSet(m)} IN
                                           LET ks == {x[2] : x \in AdmitSet(m)} IN
                                             /\ seen' = (seen \cup A)
                                             /\ admits' = [i \in Items |-> IF i \in its THEN admits[i] + 1 ELSE admits[i]]
                                             /\ phase' = [i \in Items |-> IF i \in its THEN "waiting" ELSE phase[i]]
                                             /\ agentOf' = [i \in Items |-> IF i \in its THEN cfg ELSE agentOf[i]]
                                             /\ home' = [i \in Items |-> IF i \in its THEN (CHOOSE x \in A : x[1] = i)[2] ELSE home[i]]
                                             /\ rootTs' = [i \in Items |-> IF i \in its THEN SlackThread(i) ELSE rootTs[i]]
                                             /\ queue' = [k \in Keys |-> IF k \in ks THEN Append(queue[k], (CHOOSE x \in A : x[2] = k)[1])
                                                                         ELSE queue[k]]
                                             /\ convAgent' = [k \in Keys |-> IF k \in ks /\ ~exists[k] THEN cfg ELSE convAgent[k]]
                                             /\ wake' = [k \in Keys |-> wake[k] \/ k \in ks]
                                             /\ exists' = [k \in Keys |-> exists[k] \/ k \in ks]
                                             /\ status' = [t \in ThreadTs |-> status[t] \/ t \in {SlackThread(i) : i \in its}]
         /\ UNCHANGED << cfg, slack, cfgLeft, kindLeft, active, outcome, 
                         thread, calls, movedTo, notes, rec, sent, replyLoc, 
                         replyFooter, stopsLeft, shown, inflight, settled, 
                         noteDue, settleDue, stopEv, crashes, asked, badStart, 
                         stoppedWrong, clobber, earlyThread, webHeld, expired >>

Window == /\ \E x \in {r \in seen : Mutation = "short-window" \/ Quiet(r[1])}:
               /\ seen' = seen \ {x}
               /\ expired' = TRUE
          /\ UNCHANGED << cfg, slack, cfgLeft, kindLeft, exists, convAgent, 
                          queue, active, phase, outcome, agentOf, home, rootTs, 
                          thread, calls, movedTo, notes, admits, rec, 
                          delivered, sent, replyLoc, replyFooter, status, 
                          stopsLeft, wake, shown, inflight, settled, noteDue, 
                          settleDue, stopEv, crashes, asked, badStart, 
                          stoppedWrong, clobber, earlyThread, webHeld >>

Submit == /\ \E i \in {x \in WebItems \cup AgentItems \cup ThreadWrites \cup ThreadWeb : phase[x] = "new"}:
               \E k \in CASE i \in ThreadWrites -> UNION {notes[c] : c \in Keys}
                          [] i \in ThreadWeb -> {c \in Keys : c.kind = "th" /\ c.ts = ThreadOf /\ exists[c]}
                          [] OTHER -> {c \in Keys : c.kind = "ch" /\ exists[c]}:
                 /\ webHeld' = (webHeld \/ (i \in ThreadWeb /\ RootUnsettled(ThreadOf)))
                 /\ queue' = [queue EXCEPT ![k] = Append(queue[k], i)]
                 /\ phase' = [phase EXCEPT ![i] = "waiting"]
                 /\ agentOf' = [agentOf EXCEPT ![i] = convAgent[k]]
                 /\ home' = [home EXCEPT ![i] = k]
                 /\ rootTs' = [rootTs EXCEPT ![i] = Loc(k)]
                 /\ wake' = [wake EXCEPT ![k] = TRUE]
          /\ UNCHANGED << cfg, slack, cfgLeft, kindLeft, exists, convAgent, 
                          active, outcome, thread, calls, movedTo, notes, seen, 
                          admits, rec, delivered, sent, replyLoc, replyFooter, 
                          status, stopsLeft, shown, inflight, settled, noteDue, 
                          settleDue, stopEv, crashes, asked, badStart, 
                          stoppedWrong, clobber, earlyThread, expired >>

StopPress == /\ stopsLeft > 0
             /\ \E e \in {x \in Items \X Keys : active[x[2]] = x[1] /\ phase[x[1]] = "running" /\ shown[x[1]]
                                                /\ status[rootTs[x[1]]]}:
                  /\ stopEv' = (stopEv \cup {e})
                  /\ stopsLeft' = stopsLeft - 1
             /\ UNCHANGED << cfg, slack, cfgLeft, kindLeft, exists, convAgent, 
                             queue, active, phase, outcome, agentOf, home, 
                             rootTs, thread, calls, movedTo, notes, seen, 
                             admits, rec, delivered, sent, replyLoc, 
                             replyFooter, status, wake, shown, inflight, 
                             settled, noteDue, settleDue, crashes, asked, 
                             badStart, stoppedWrong, clobber, earlyThread, 
                             webHeld, expired >>

StopDeliver == /\ \E e \in stopEv:
                    /\ \E tk \in IF Mutation = "stop-by-conv" THEN {e[2]}
                                 ELSE IF \E k \in Keys : active[k] = e[1] THEN {k \in Keys : active[k] = e[1]} ELSE {NoKey}:
                         IF tk # NoKey /\ active[tk] # 0 /\ phase[active[tk]] = "running"
                            THEN /\ phase' = [phase EXCEPT ![active[tk]] = "delivering"]
                                 /\ outcome' = [outcome EXCEPT ![active[tk]] = "stopped"]
                                 /\ stoppedWrong' = (stoppedWrong \/ active[tk] # e[1])
                            ELSE /\ TRUE
                                 /\ UNCHANGED << phase, outcome, stoppedWrong >>
                    /\ stopEv' = stopEv \ {e}
               /\ UNCHANGED << cfg, slack, cfgLeft, kindLeft, exists, 
                               convAgent, queue, active, agentOf, home, rootTs, 
                               thread, calls, movedTo, notes, seen, admits, 
                               rec, delivered, sent, replyLoc, replyFooter, 
                               status, stopsLeft, wake, shown, inflight, 
                               settled, noteDue, settleDue, crashes, asked, 
                               badStart, clobber, earlyThread, webHeld, 
                               expired >>

Config == /\ \/ /\ cfgLeft > 0
                /\ \E a \in Agents \ {cfg}:
                     cfg' = a
                /\ cfgLeft' = cfgLeft - 1
                /\ UNCHANGED <<slack, kindLeft>>
             \/ /\ kindLeft > 0
                /\ \E a \in Agents:
                     slack' = [slack EXCEPT ![a] = ~slack[a]]
                /\ kindLeft' = kindLeft - 1
                /\ UNCHANGED <<cfg, cfgLeft>>
          /\ UNCHANGED << exists, convAgent, queue, active, phase, outcome, 
                          agentOf, home, rootTs, thread, calls, movedTo, notes, 
                          seen, admits, rec, delivered, sent, replyLoc, 
                          replyFooter, status, stopsLeft, wake, shown, 
                          inflight, settled, noteDue, settleDue, stopEv, 
                          crashes, asked, badStart, stoppedWrong, clobber, 
                          earlyThread, webHeld, expired >>

Lifecycle == /\ crashes < MaxCrashes
             /\ crashes' = crashes + 1
             /\ wake' = [k \in Keys |-> exists[k] /\ queue[k] # <<>>]
             /\ shown' = [i \in Items |-> FALSE]
             /\ inflight' = [p \in Posts |-> FALSE]
             /\ settled' = [i \in Items |-> FALSE]
             /\ noteDue' = {}
             /\ settleDue' = {}
             /\ stopEv' = {}
             /\ IF Mutation = "volatile-ratchet"
                   THEN /\ thread' = [i \in Items |-> thread[i] /\ movedTo[i] # NoKey]
                   ELSE /\ TRUE
                        /\ UNCHANGED thread
             /\ UNCHANGED << cfg, slack, cfgLeft, kindLeft, exists, convAgent, 
                             queue, active, phase, outcome, agentOf, home, 
                             rootTs, calls, movedTo, notes, seen, admits, rec, 
                             delivered, sent, replyLoc, replyFooter, status, 
                             stopsLeft, asked, badStart, stoppedWrong, clobber, 
                             earlyThread, webHeld, expired >>

Next == Settler \/ Slack \/ Window \/ Submit \/ StopPress \/ StopDeliver
           \/ Config \/ Lifecycle
           \/ (\E self \in Keys: Lane(self))

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in Keys : WF_vars(Lane(self))
        /\ WF_vars(Settler)
        /\ WF_vars(Slack)
        /\ WF_vars(Submit)
        /\ WF_vars(StopDeliver)

\* END TRANSLATION

=============================================================================
