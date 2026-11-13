----------------------------- MODULE AgentPause -----------------------------
EXTENDS Integers, Sequences, FiniteSets

(* The Agent Pause of one Web chat as built, with its fixes (Mutation = "none"); each other Mutation turns one
   fix or the gate off. Each label is one step of the code with the lock or transaction it holds; README.md maps
   every label to its function. Items are small integers by kind; the goal's continuation is item 0. A crash
   drops everything a process holds in memory and keeps the database. *)

CONSTANTS Msgs, QItems, Writes, Notes, Scheds, Recurs,
          GoalMode, GoalTurns, MaxToggles, MaxCrashes, Mutation, SplitAdmit

ASSUME GoalMode \in {"none", "bounded", "forever"}
ASSUME Mutation \in {"none", "no-gate", "no-resubmit", "guess-idle", "no-hold", "no-close", "no-take"}
ASSUME SplitAdmit \in BOOLEAN
ASSUME MaxCrashes \in 0..1

Items == Msgs \cup QItems \cup Writes \cup Notes \cup Scheds \cup Recurs
GoalItem == 0
NoItem == -1
NoComp == -1

\* A bridgeRequest: its item, the caller's completion it carries, whether it resumes the active turn (turnID),
\* has a Thread Queue item ID (queueItemID), is a person's input (inbound.Human), and is a recurring schedule.
Req(i, c, q, h, r) == [item |-> i, comp |-> c, turn |-> FALSE, queued |-> q, human |-> h, recurring |-> r]
NoReq == Req(NoItem, NoComp, FALSE, FALSE, FALSE)
GoalReq(c) == Req(GoalItem, c, FALSE, FALSE, FALSE)
\* The request later work builds from item i's durable row: a queue row (submitEnqueuedItem), a one-time
\* schedule's waiting row (submitDueScheduled) or a Completion Note (a wake).
RowReq(i) == Req(i, NoComp, i \in Msgs \cup QItems \cup Writes \cup Recurs, i \in Msgs \cup QItems, FALSE)

(* --algorithm AgentPause
variables
    \* Durable: rows in the database, kept by a crash.
    paused = FALSE,                                   \* conversations.paused
    st = [i \in Items |-> "new"],                     \* new | row | fired | turn | ran | posted | missed
    goalLeft = IF GoalMode = "none" THEN 0 ELSE GoalTurns,
    activeTurn = NoItem,                              \* the running turn's row: an item, the goal, or none
    steers = {},                                      \* inputs that joined the running turn (journaled)
    \* In memory: lost by a crash.
    requestCh = IF GoalMode = "none" THEN <<>> ELSE <<GoalReq(NoComp)>>,
    waiting = {},                                     \* b.waiting: held requests and their callers
    handling = FALSE,                                 \* b.handling
    inputOpen = FALSE,                                \* b.inputOpen
    hand = NoReq,                                     \* the request the live loop holds
    comp = [m \in Msgs |-> "none"],                   \* a sender's completion: none | open | closed | dead
    steerComps = {},                                  \* completions of steers in the running turn
    goalHeld = FALSE,                                 \* b.goalHeld: the gate refused the goal's continuation
    boot = 0, crashes = 0, toggles = 0,
    \* Ghosts for the properties.
    gatePaused = FALSE, badStart = FALSE, parity = FALSE;

define
    GoalActive == GoalMode # "none" /\ goalLeft > 0
    Pickable == {i \in Items \ Notes : st[i] = "row"} \cup {r \in Recurs : st[r] = "new"}
    InFlight(i) == (\E j \in 1..Len(requestCh) : requestCh[j].item = i)
                   \/ (\E w \in waiting : w.item = i) \/ hand.item = i
    Held(m) == \E w \in waiting : w.comp = m
    Owned(m) == (\E j \in 1..Len(requestCh) : requestCh[j].comp = m) \/ Held(m)
                \/ hand.comp = m \/ m \in steerComps
    GoalQueued == Cardinality({j \in 1..Len(requestCh) : requestCh[j].item = GoalItem})

    \* Safety.
    \* A paused chat's running reply takes no steer: its input stays closed.
    InputClosedWhilePaused == paused => ~inputOpen
    \* The pause is forward-looking: no request that reaches the gate while the chat is paused starts a turn.
    \* Only one that passed the gate before the pause committed may still start (the allowed race).
    ForwardLookingPause == ~badStart
    \* A message posted to the people in a paused chat is chat: no turn holds it, runs it or waits for it.
    PausedMessageNeverTurns == \A m \in Msgs : st[m] = "posted" =>
        comp[m] = "none" /\ m \notin steers /\ activeTurn # m /\ ~InFlight(m)
    \* Nothing accepted is lost: a recurring firing the pause catches becomes a row, and a turn's item is its row.
    NothingLost == \A i \in Items : (st[i] = "fired" => InFlight(i)) /\ (st[i] = "turn" <=> activeTurn = i)
    \* No caller waits on a completion nobody holds: every open one rides a request, a hold, or a steer.
    CompletionOwned == \A m \in Msgs : comp[m] = "open" => Owned(m)
    \* At most one goal continuation waits for the loop (a finding check, not a requirement).
    AtMostOneContinuation == GoalQueued + (IF hand.item = GoalItem THEN 1 ELSE 0) <= 1

    \* Liveness, under the fairness in the Spec: every process but a person's choice to act is weakly fair.
    \* A chat that stays paused eventually starts no new turn: turn starts stop flipping parity (which only the
    \* checks with a goal that never finishes flip, the only source of endless turns).
    StopsTurning == (<>[]paused) => (<>[]parity \/ <>[]~parity)
    \* Once the chat stays live, the goal the pause held eventually finishes its turns.
    GoalCompletes == (<>[]~paused) => <>~GoalActive
    \* Once the chat stays live, every held row eventually runs.
    HeldRunsAfterResume == \A i \in Items : (<>[]~paused) => [](st[i] = "row" => <>(st[i] = "ran"))
    \* A caller's completion closes, or its request is held for resume (or the process died with it).
    CallerReleased == \A m \in Msgs : [](comp[m] = "open" => <>(comp[m] # "open" \/ Held(m)))
    \* Once the chat stays live, every caller's completion closes.
    CallerReleasedWhenLive == \A m \in Msgs : (<>[]~paused) => [](comp[m] = "open" => <>(comp[m] # "open"))
end define;

\* Bridge.pickLaterWork: the cutoff read (no lock), then one step that hands the loop the head of later work.
procedure PickLaterWork(pb = 0)
begin
PL1:
    await boot = pb;
    if paused then return; end if;
PL2:
    await boot = pb;
    if requestCh = <<>> /\ ~GoalActive then
        if \E n \in Notes : st[n] = "row" then
            with n \in {x \in Notes : st[x] = "row"} do
                requestCh := Append(requestCh, RowReq(n));
            end with;
        elsif Pickable # {} then
            with i \in Pickable do
                if i \in Recurs /\ st[i] = "new" then
                    \* submitDueScheduled: ClaimScheduledMessage moves a recurring rule to its next time.
                    st[i] := "fired";
                    requestCh := Append(requestCh, Req(i, NoComp, FALSE, FALSE, TRUE));
                elsif i \in Scheds then
                    requestCh := Append(requestCh, RowReq(i));
                elsif \E w \in waiting : w.item = i then
                    \* submitEnqueuedItem hands a held request back with its caller.
                    with w \in {x \in waiting : x.item = i} do
                        waiting := waiting \ {w};
                        if i \in Msgs /\ inputOpen then
                            steers := steers \cup {i};
                            if w.comp # NoComp then steerComps := steerComps \cup {w.comp}; end if;
                        else
                            requestCh := Append(requestCh, w);
                        end if;
                    end with;
                elsif i \in Msgs /\ inputOpen then
                    steers := steers \cup {i};
                else
                    requestCh := Append(requestCh, RowReq(i));
                end if;
            end with;
        end if;
    end if;
    return;
end procedure;

\* The chat's bridge loop, one instance per boot: Run, then loop and handle.
fair process Loop \in {100 + k : k \in 0..MaxCrashes}
begin
LBoot:
    await boot = self - 100;
    \* Run: headRequest resumes the turn a crash interrupted.
    if activeTurn # NoItem then
        hand := [NoReq EXCEPT !.item = activeTurn, !.turn = TRUE];
        goto LGate;
    end if;
LDequeue:
    await boot = self - 100 /\ requestCh # <<>>;
    hand := Head(requestCh);
    requestCh := Tail(requestCh);
LGate:
    \* handle: under b.mu, read the pause; a request resuming the active turn passes.
    await boot = self - 100;
    if paused /\ ~hand.turn /\ Mutation # "no-gate" then
        if hand.human \/ hand.recurring then
            \* preserveRevertRequestLocked: the input keeps a Thread Queue row and its caller waits in b.waiting.
            if \A w \in waiting : w.item # hand.item then
                waiting := waiting \cup {[hand EXCEPT !.queued = TRUE]};
            elsif Mutation # "no-hold" /\ hand.comp # NoComp then
                \* The request already held for this row takes over this one's caller.
                with w \in {x \in waiting : x.item = hand.item} do
                    waiting := (waiting \ {w}) \cup {[w EXCEPT !.comp = hand.comp]};
                end with;
            end if;
            if hand.item \in Recurs then st[hand.item] := "row"; end if;
        else
            \* AgentPausedError closes its completion; its row, note or goal stays.
            if hand.comp # NoComp then comp[hand.comp] := "closed"; end if;
            if hand.item = GoalItem /\ Mutation # "guess-idle" then goalHeld := TRUE; end if;
        end if;
        hand := NoReq;
        goto LDequeue;
    else
        gatePaused := paused /\ ~hand.turn;
        handling := TRUE;
    end if;
LActivate:
    \* activateInbound: an active goal holds a queued request (Goal read, no lock).
    await boot = self - 100;
    if hand.turn then
        goto LOpen;
    elsif hand.queued /\ GoalActive then
        goto LNotAdmitted;
    end if;
LStart:
    \* activateInbound's transaction claims the row and records the turn. It does not read the pause.
    await boot = self - 100;
    if \/ hand.item = GoalItem
       \/ hand.queued /\ st[hand.item] = "row"
       \/ ~hand.queued /\ hand.item \in Notes \cup Scheds /\ st[hand.item] = "row"
       \/ ~hand.queued /\ hand.recurring /\ st[hand.item] = "fired" then
        if hand.item # GoalItem then st[hand.item] := "turn"; end if;
        \* takeHeldCaller: a claim drops the request b.waiting holds for the same row; the claiming request takes its
        \* caller, or, having its own, the held caller returns now.
        if hand.queued /\ Mutation # "no-take" /\ \E w \in waiting : w.item = hand.item then
            with w \in {x \in waiting : x.item = hand.item} do
                waiting := waiting \ {w};
                if w.comp # NoComp /\ hand.comp = NoComp then
                    hand.comp := w.comp;
                elsif w.comp # NoComp then
                    comp[w.comp] := "closed";
                end if;
            end with;
        end if;
        activeTurn := hand.item;
        if GoalMode = "forever" then parity := ~parity; end if;
        badStart := badStart \/ gatePaused;
    else
        goto LNotAdmitted;
    end if;
LOpen:
    \* openInputWindow: under b.mu, read the pause; a paused chat's turn takes no steer.
    await boot = self - 100;
    inputOpen := ~paused;
LEnd:
    \* finish: finishTurn closes the turn's row; the steers it took are done; a goal turn counts.
    await boot = self - 100;
    st := [i \in Items |-> IF i = activeTurn \/ i \in steers THEN "ran" ELSE st[i]];
    if activeTurn = GoalItem /\ GoalMode = "bounded" then goalLeft := goalLeft - 1; end if;
    steers := {};
    activeTurn := NoItem;
    hand.item := NoItem;
LGoalNext:
    \* finishGoalTurn: an active goal queues its next turn, handing it the request's caller.
    await boot = self - 100;
    if GoalActive then
        requestCh := Append(requestCh, GoalReq(hand.comp));
        hand.comp := NoComp;
    end if;
LSettle:
    \* settleSteers closes the input and releases steer callers; handle then releases the request's caller.
    await boot = self - 100;
    inputOpen := FALSE;
    comp := [m \in Msgs |-> IF m \in steerComps \/ m = hand.comp THEN "closed" ELSE comp[m]];
    steerComps := {};
    steers := {};
    call PickLaterWork(self - 100);
LIdle:
    \* handle's deferred cleanup.
    await boot = self - 100;
    handling := FALSE;
    hand := NoReq;
    gatePaused := FALSE;
    goto LDequeue;
LNotAdmitted:
    \* handle: a request activateInbound did not take releases its caller, then picks later work.
    if hand.comp # NoComp /\ Mutation # "no-close" then comp[hand.comp] := "closed"; end if;
    call PickLaterWork(self - 100);
    goto LIdle;
end process;

\* People pause and resume, in turn: odd toggles pause, even ones resume, each committing in its first step. A
\* check's toggle count says how the chat ends: an even count leaves it live, an odd one paused.
fair process Toggle \in {200 + t : t \in 1..MaxToggles}
variables tb = 0, gFound = FALSE, gRunning = FALSE, gIdle = FALSE;
begin
TStart:
    await toggles = self - 201;
    toggles := self - 200;
    tb := boot;
    \* SetAgentPaused / recordAgentPauseLocked: under b.mu, one transaction sets the flag and adds the line.
    if (self - 200) % 2 = 1 then
        if ~paused then
            paused := TRUE;
            inputOpen := FALSE;
        end if;
        goto TDone;
    elsif paused then
        paused := FALSE;
    else
        goto TDone;
    end if;
TGoal:
    \* Resume: Sessions.Goal.
    await boot = tb;
    gFound := GoalActive;
TRunning:
    \* Resume: Sessions.HasActiveTurn.
    await boot = tb;
    gRunning := activeTurn # NoItem;
TIdle:
    \* Resume: under b.mu, take the gate's record that it refused the goal's continuation (guess-idle: the former
    \* guess that the bridge holds no request).
    await boot = tb;
    if Mutation = "guess-idle" then
        gIdle := ~handling /\ requestCh = <<>>;
    else
        gIdle := goalHeld;
        goalHeld := FALSE;
    end if;
TSubmit:
    \* Resume: Submit the goal's continuation, then pickLaterWork.
    await boot = tb;
    if gFound /\ ~gRunning /\ gIdle /\ Mutation # "no-resubmit" then
        requestCh := Append(requestCh, GoalReq(NoComp));
    end if;
    call PickLaterWork(tb);
TDone:
    tb := 0;
    gFound := FALSE;
    gRunning := FALSE;
    gIdle := FALSE;
end process;

\* A person's plain message (Server.prompt). A process whose boot crashed is dead: its steps stay disabled.
fair process Sender \in Msgs
variables sb = 0;
begin
SRoute:
    \* pausedSend reads the flag without the lock.
    sb := boot;
    if ~paused then goto SAdmit; end if;
SPost:
    \* PostPausedMessage: under b.mu, one transaction re-reads the flag and posts the line.
    await boot = sb;
    if paused then
        st[self] := "posted";
        goto SDone;
    end if;
SAdmit:
    \* RunTurn: admitWeb records the Thread Queue row; enqueue hands it to the open turn or the loop.
    await boot = sb;
    st[self] := "row";
    if ~SplitAdmit then
        comp[self] := "open";
        if inputOpen then
            steers := steers \cup {self};
            steerComps := steerComps \cup {self};
        else
            requestCh := Append(requestCh, Req(self, self, TRUE, TRUE, FALSE));
        end if;
        goto SWait;
    end if;
SEnqueue:
    await boot = sb;
    comp[self] := "open";
    if inputOpen then
        steers := steers \cup {self};
        steerComps := steerComps \cup {self};
    else
        requestCh := Append(requestCh, Req(self, self, TRUE, TRUE, FALSE));
    end if;
SWait:
    \* RunTurn waits for its completion.
    await boot = sb /\ comp[self] = "closed";
SDone:
    sb := 0;
end process;

\* A person's $enqueue (StashQueueItem): admitWeb's row, then straight to the loop.
fair process Queuer \in QItems
variables qb = 0;
begin
QAdmit:
    qb := boot;
    st[self] := "row";
QEnqueue:
    await boot = qb;
    requestCh := Append(requestCh, Req(self, NoComp, TRUE, TRUE, FALSE));
    qb := 0;
end process;

\* Another agent's session write (writeSession / admitAgentWrite): a row, a steer while input is open.
fair process Writer \in Writes
variables wb = 0;
begin
WAdmit:
    wb := boot;
    st[self] := "row";
    if inputOpen then
        steers := steers \cup {self};
        goto WDone;
    end if;
WPick:
    call PickLaterWork(wb);
WDone:
    wb := 0;
end process;

\* A Completion Note (noteReady) or a one-time Scheduled Message (its timer): a row, then later work.
fair process Source \in Notes \cup Scheds
variables nb = 0;
begin
NCreate:
    nb := boot;
    st[self] := "row";
NPick:
    call PickLaterWork(nb);
NDone:
    nb := 0;
end process;

\* A crash may come at any step: it keeps the database and drops memory; startup then runs StartActiveGoals and
\* StartQueuedConversations. The new boot's loop runs headRequest (StartActiveTurns) concurrently.
fair process Lifecycle = 300
variables lb = 0, lRunning = FALSE, lGoal = FALSE;
begin
LCrash:
    either
        goto LCDone;
    or
        await crashes < MaxCrashes;
        crashes := crashes + 1;
        boot := boot + 1;
        lb := boot;
        requestCh := <<>>;
        waiting := {};
        handling := FALSE;
        inputOpen := FALSE;
        hand := NoReq;
        goalHeld := FALSE;
        gatePaused := FALSE;
        comp := [m \in Msgs |-> IF comp[m] = "open" THEN "dead" ELSE comp[m]];
        steerComps := {};
        st := [i \in Items |-> IF st[i] = "fired" THEN "missed" ELSE st[i]];
        steers := IF activeTurn # NoItem THEN steers ELSE {};
    end either;
LBootGoals:
    \* StartActiveGoals: HasActiveTurn and Goal.
    await boot = lb;
    lRunning := activeTurn # NoItem;
    lGoal := GoalActive;
LBootSubmit:
    await boot = lb;
    if lGoal /\ ~lRunning then
        requestCh := Append(requestCh, GoalReq(NoComp));
    end if;
    call PickLaterWork(lb);
LBootDone:
    lb := 0;
    lRunning := FALSE;
    lGoal := FALSE;
LCDone:
    skip;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, paused, st, goalLeft, activeTurn, steers, requestCh, waiting, 
          handling, inputOpen, hand, comp, steerComps, goalHeld, boot, 
          crashes, toggles, gatePaused, badStart, parity, stack

(* define statement *)
GoalActive == GoalMode # "none" /\ goalLeft > 0
Pickable == {i \in Items \ Notes : st[i] = "row"} \cup {r \in Recurs : st[r] = "new"}
InFlight(i) == (\E j \in 1..Len(requestCh) : requestCh[j].item = i)
               \/ (\E w \in waiting : w.item = i) \/ hand.item = i
Held(m) == \E w \in waiting : w.comp = m
Owned(m) == (\E j \in 1..Len(requestCh) : requestCh[j].comp = m) \/ Held(m)
            \/ hand.comp = m \/ m \in steerComps
GoalQueued == Cardinality({j \in 1..Len(requestCh) : requestCh[j].item = GoalItem})



InputClosedWhilePaused == paused => ~inputOpen


ForwardLookingPause == ~badStart

PausedMessageNeverTurns == \A m \in Msgs : st[m] = "posted" =>
    comp[m] = "none" /\ m \notin steers /\ activeTurn # m /\ ~InFlight(m)

NothingLost == \A i \in Items : (st[i] = "fired" => InFlight(i)) /\ (st[i] = "turn" <=> activeTurn = i)

CompletionOwned == \A m \in Msgs : comp[m] = "open" => Owned(m)

AtMostOneContinuation == GoalQueued + (IF hand.item = GoalItem THEN 1 ELSE 0) <= 1




StopsTurning == (<>[]paused) => (<>[]parity \/ <>[]~parity)

GoalCompletes == (<>[]~paused) => <>~GoalActive

HeldRunsAfterResume == \A i \in Items : (<>[]~paused) => [](st[i] = "row" => <>(st[i] = "ran"))

CallerReleased == \A m \in Msgs : [](comp[m] = "open" => <>(comp[m] # "open" \/ Held(m)))

CallerReleasedWhenLive == \A m \in Msgs : (<>[]~paused) => [](comp[m] = "open" => <>(comp[m] # "open"))

VARIABLES pb, tb, gFound, gRunning, gIdle, sb, qb, wb, nb, lb, lRunning, 
          lGoal

vars == << pc, paused, st, goalLeft, activeTurn, steers, requestCh, waiting, 
           handling, inputOpen, hand, comp, steerComps, goalHeld, boot, 
           crashes, toggles, gatePaused, badStart, parity, stack, pb, tb, 
           gFound, gRunning, gIdle, sb, qb, wb, nb, lb, lRunning, lGoal >>

ProcSet == ({100 + k : k \in 0..MaxCrashes}) \cup ({200 + t : t \in 1..MaxToggles}) \cup (Msgs) \cup (QItems) \cup (Writes) \cup (Notes \cup Scheds) \cup {300}

Init == (* Global variables *)
        /\ paused = FALSE
        /\ st = [i \in Items |-> "new"]
        /\ goalLeft = (IF GoalMode = "none" THEN 0 ELSE GoalTurns)
        /\ activeTurn = NoItem
        /\ steers = {}
        /\ requestCh = (IF GoalMode = "none" THEN <<>> ELSE <<GoalReq(NoComp)>>)
        /\ waiting = {}
        /\ handling = FALSE
        /\ inputOpen = FALSE
        /\ hand = NoReq
        /\ comp = [m \in Msgs |-> "none"]
        /\ steerComps = {}
        /\ goalHeld = FALSE
        /\ boot = 0
        /\ crashes = 0
        /\ toggles = 0
        /\ gatePaused = FALSE
        /\ badStart = FALSE
        /\ parity = FALSE
        (* Procedure PickLaterWork *)
        /\ pb = [ self \in ProcSet |-> 0]
        (* Process Toggle *)
        /\ tb = [self \in {200 + t : t \in 1..MaxToggles} |-> 0]
        /\ gFound = [self \in {200 + t : t \in 1..MaxToggles} |-> FALSE]
        /\ gRunning = [self \in {200 + t : t \in 1..MaxToggles} |-> FALSE]
        /\ gIdle = [self \in {200 + t : t \in 1..MaxToggles} |-> FALSE]
        (* Process Sender *)
        /\ sb = [self \in Msgs |-> 0]
        (* Process Queuer *)
        /\ qb = [self \in QItems |-> 0]
        (* Process Writer *)
        /\ wb = [self \in Writes |-> 0]
        (* Process Source *)
        /\ nb = [self \in Notes \cup Scheds |-> 0]
        (* Process Lifecycle *)
        /\ lb = 0
        /\ lRunning = FALSE
        /\ lGoal = FALSE
        /\ stack = [self \in ProcSet |-> << >>]
        /\ pc = [self \in ProcSet |-> CASE self \in {100 + k : k \in 0..MaxCrashes} -> "LBoot"
                                        [] self \in {200 + t : t \in 1..MaxToggles} -> "TStart"
                                        [] self \in Msgs -> "SRoute"
                                        [] self \in QItems -> "QAdmit"
                                        [] self \in Writes -> "WAdmit"
                                        [] self \in Notes \cup Scheds -> "NCreate"
                                        [] self = 300 -> "LCrash"]

PL1(self) == /\ pc[self] = "PL1"
             /\ boot = pb[self]
             /\ IF paused
                   THEN /\ pc' = [pc EXCEPT ![self] = Head(stack[self]).pc]
                        /\ pb' = [pb EXCEPT ![self] = Head(stack[self]).pb]
                        /\ stack' = [stack EXCEPT ![self] = Tail(stack[self])]
                   ELSE /\ pc' = [pc EXCEPT ![self] = "PL2"]
                        /\ UNCHANGED << stack, pb >>
             /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                             requestCh, waiting, handling, inputOpen, hand, 
                             comp, steerComps, goalHeld, boot, crashes, 
                             toggles, gatePaused, badStart, parity, tb, gFound, 
                             gRunning, gIdle, sb, qb, wb, nb, lb, lRunning, 
                             lGoal >>

PL2(self) == /\ pc[self] = "PL2"
             /\ boot = pb[self]
             /\ IF requestCh = <<>> /\ ~GoalActive
                   THEN /\ IF \E n \in Notes : st[n] = "row"
                              THEN /\ \E n \in {x \in Notes : st[x] = "row"}:
                                        requestCh' = Append(requestCh, RowReq(n))
                                   /\ UNCHANGED << st, steers, waiting, 
                                                   steerComps >>
                              ELSE /\ IF Pickable # {}
                                         THEN /\ \E i \in Pickable:
                                                   IF i \in Recurs /\ st[i] = "new"
                                                      THEN /\ st' = [st EXCEPT ![i] = "fired"]
                                                           /\ requestCh' = Append(requestCh, Req(i, NoComp, FALSE, FALSE, TRUE))
                                                           /\ UNCHANGED << steers, 
                                                                           waiting, 
                                                                           steerComps >>
                                                      ELSE /\ IF i \in Scheds
                                                                 THEN /\ requestCh' = Append(requestCh, RowReq(i))
                                                                      /\ UNCHANGED << steers, 
                                                                                      waiting, 
                                                                                      steerComps >>
                                                                 ELSE /\ IF \E w \in waiting : w.item = i
                                                                            THEN /\ \E w \in {x \in waiting : x.item = i}:
                                                                                      /\ waiting' = waiting \ {w}
                                                                                      /\ IF i \in Msgs /\ inputOpen
                                                                                            THEN /\ steers' = (steers \cup {i})
                                                                                                 /\ IF w.comp # NoComp
                                                                                                       THEN /\ steerComps' = (steerComps \cup {w.comp})
                                                                                                       ELSE /\ TRUE
                                                                                                            /\ UNCHANGED steerComps
                                                                                                 /\ UNCHANGED requestCh
                                                                                            ELSE /\ requestCh' = Append(requestCh, w)
                                                                                                 /\ UNCHANGED << steers, 
                                                                                                                 steerComps >>
                                                                            ELSE /\ IF i \in Msgs /\ inputOpen
                                                                                       THEN /\ steers' = (steers \cup {i})
                                                                                            /\ UNCHANGED requestCh
                                                                                       ELSE /\ requestCh' = Append(requestCh, RowReq(i))
                                                                                            /\ UNCHANGED steers
                                                                                 /\ UNCHANGED << waiting, 
                                                                                                 steerComps >>
                                                           /\ st' = st
                                         ELSE /\ TRUE
                                              /\ UNCHANGED << st, steers, 
                                                              requestCh, 
                                                              waiting, 
                                                              steerComps >>
                   ELSE /\ TRUE
                        /\ UNCHANGED << st, steers, requestCh, waiting, 
                                        steerComps >>
             /\ pc' = [pc EXCEPT ![self] = Head(stack[self]).pc]
             /\ pb' = [pb EXCEPT ![self] = Head(stack[self]).pb]
             /\ stack' = [stack EXCEPT ![self] = Tail(stack[self])]
             /\ UNCHANGED << paused, goalLeft, activeTurn, handling, inputOpen, 
                             hand, comp, goalHeld, boot, crashes, toggles, 
                             gatePaused, badStart, parity, tb, gFound, 
                             gRunning, gIdle, sb, qb, wb, nb, lb, lRunning, 
                             lGoal >>

PickLaterWork(self) == PL1(self) \/ PL2(self)

LBoot(self) == /\ pc[self] = "LBoot"
               /\ boot = self - 100
               /\ IF activeTurn # NoItem
                     THEN /\ hand' = [NoReq EXCEPT !.item = activeTurn, !.turn = TRUE]
                          /\ pc' = [pc EXCEPT ![self] = "LGate"]
                     ELSE /\ pc' = [pc EXCEPT ![self] = "LDequeue"]
                          /\ hand' = hand
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, inputOpen, comp, 
                               steerComps, goalHeld, boot, crashes, toggles, 
                               gatePaused, badStart, parity, stack, pb, tb, 
                               gFound, gRunning, gIdle, sb, qb, wb, nb, lb, 
                               lRunning, lGoal >>

LDequeue(self) == /\ pc[self] = "LDequeue"
                  /\ boot = self - 100 /\ requestCh # <<>>
                  /\ hand' = Head(requestCh)
                  /\ requestCh' = Tail(requestCh)
                  /\ pc' = [pc EXCEPT ![self] = "LGate"]
                  /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                                  waiting, handling, inputOpen, comp, 
                                  steerComps, goalHeld, boot, crashes, toggles, 
                                  gatePaused, badStart, parity, stack, pb, tb, 
                                  gFound, gRunning, gIdle, sb, qb, wb, nb, lb, 
                                  lRunning, lGoal >>

LGate(self) == /\ pc[self] = "LGate"
               /\ boot = self - 100
               /\ IF paused /\ ~hand.turn /\ Mutation # "no-gate"
                     THEN /\ IF hand.human \/ hand.recurring
                                THEN /\ IF \A w \in waiting : w.item # hand.item
                                           THEN /\ waiting' = (waiting \cup {[hand EXCEPT !.queued = TRUE]})
                                           ELSE /\ IF Mutation # "no-hold" /\ hand.comp # NoComp
                                                      THEN /\ \E w \in {x \in waiting : x.item = hand.item}:
                                                                waiting' = ((waiting \ {w}) \cup {[w EXCEPT !.comp = hand.comp]})
                                                      ELSE /\ TRUE
                                                           /\ UNCHANGED waiting
                                     /\ IF hand.item \in Recurs
                                           THEN /\ st' = [st EXCEPT ![hand.item] = "row"]
                                           ELSE /\ TRUE
                                                /\ st' = st
                                     /\ UNCHANGED << comp, goalHeld >>
                                ELSE /\ IF hand.comp # NoComp
                                           THEN /\ comp' = [comp EXCEPT ![hand.comp] = "closed"]
                                           ELSE /\ TRUE
                                                /\ comp' = comp
                                     /\ IF hand.item = GoalItem /\ Mutation # "guess-idle"
                                           THEN /\ goalHeld' = TRUE
                                           ELSE /\ TRUE
                                                /\ UNCHANGED goalHeld
                                     /\ UNCHANGED << st, waiting >>
                          /\ hand' = NoReq
                          /\ pc' = [pc EXCEPT ![self] = "LDequeue"]
                          /\ UNCHANGED << handling, gatePaused >>
                     ELSE /\ gatePaused' = (paused /\ ~hand.turn)
                          /\ handling' = TRUE
                          /\ pc' = [pc EXCEPT ![self] = "LActivate"]
                          /\ UNCHANGED << st, waiting, hand, comp, goalHeld >>
               /\ UNCHANGED << paused, goalLeft, activeTurn, steers, requestCh, 
                               inputOpen, steerComps, boot, crashes, toggles, 
                               badStart, parity, stack, pb, tb, gFound, 
                               gRunning, gIdle, sb, qb, wb, nb, lb, lRunning, 
                               lGoal >>

LActivate(self) == /\ pc[self] = "LActivate"
                   /\ boot = self - 100
                   /\ IF hand.turn
                         THEN /\ pc' = [pc EXCEPT ![self] = "LOpen"]
                         ELSE /\ IF hand.queued /\ GoalActive
                                    THEN /\ pc' = [pc EXCEPT ![self] = "LNotAdmitted"]
                                    ELSE /\ pc' = [pc EXCEPT ![self] = "LStart"]
                   /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                                   requestCh, waiting, handling, inputOpen, 
                                   hand, comp, steerComps, goalHeld, boot, 
                                   crashes, toggles, gatePaused, badStart, 
                                   parity, stack, pb, tb, gFound, gRunning, 
                                   gIdle, sb, qb, wb, nb, lb, lRunning, lGoal >>

LStart(self) == /\ pc[self] = "LStart"
                /\ boot = self - 100
                /\ IF \/ hand.item = GoalItem
                      \/ hand.queued /\ st[hand.item] = "row"
                      \/ ~hand.queued /\ hand.item \in Notes \cup Scheds /\ st[hand.item] = "row"
                      \/ ~hand.queued /\ hand.recurring /\ st[hand.item] = "fired"
                      THEN /\ IF hand.item # GoalItem
                                 THEN /\ st' = [st EXCEPT ![hand.item] = "turn"]
                                 ELSE /\ TRUE
                                      /\ st' = st
                           /\ IF hand.queued /\ Mutation # "no-take" /\ \E w \in waiting : w.item = hand.item
                                 THEN /\ \E w \in {x \in waiting : x.item = hand.item}:
                                           /\ waiting' = waiting \ {w}
                                           /\ IF w.comp # NoComp /\ hand.comp = NoComp
                                                 THEN /\ hand' = [hand EXCEPT !.comp = w.comp]
                                                      /\ comp' = comp
                                                 ELSE /\ IF w.comp # NoComp
                                                            THEN /\ comp' = [comp EXCEPT ![w.comp] = "closed"]
                                                            ELSE /\ TRUE
                                                                 /\ comp' = comp
                                                      /\ hand' = hand
                                 ELSE /\ TRUE
                                      /\ UNCHANGED << waiting, hand, comp >>
                           /\ activeTurn' = hand'.item
                           /\ IF GoalMode = "forever"
                                 THEN /\ parity' = ~parity
                                 ELSE /\ TRUE
                                      /\ UNCHANGED parity
                           /\ badStart' = (badStart \/ gatePaused)
                           /\ pc' = [pc EXCEPT ![self] = "LOpen"]
                      ELSE /\ pc' = [pc EXCEPT ![self] = "LNotAdmitted"]
                           /\ UNCHANGED << st, activeTurn, waiting, hand, comp, 
                                           badStart, parity >>
                /\ UNCHANGED << paused, goalLeft, steers, requestCh, handling, 
                                inputOpen, steerComps, goalHeld, boot, crashes, 
                                toggles, gatePaused, stack, pb, tb, gFound, 
                                gRunning, gIdle, sb, qb, wb, nb, lb, lRunning, 
                                lGoal >>

LOpen(self) == /\ pc[self] = "LOpen"
               /\ boot = self - 100
               /\ inputOpen' = ~paused
               /\ pc' = [pc EXCEPT ![self] = "LEnd"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, hand, comp, 
                               steerComps, goalHeld, boot, crashes, toggles, 
                               gatePaused, badStart, parity, stack, pb, tb, 
                               gFound, gRunning, gIdle, sb, qb, wb, nb, lb, 
                               lRunning, lGoal >>

LEnd(self) == /\ pc[self] = "LEnd"
              /\ boot = self - 100
              /\ st' = [i \in Items |-> IF i = activeTurn \/ i \in steers THEN "ran" ELSE st[i]]
              /\ IF activeTurn = GoalItem /\ GoalMode = "bounded"
                    THEN /\ goalLeft' = goalLeft - 1
                    ELSE /\ TRUE
                         /\ UNCHANGED goalLeft
              /\ steers' = {}
              /\ activeTurn' = NoItem
              /\ hand' = [hand EXCEPT !.item = NoItem]
              /\ pc' = [pc EXCEPT ![self] = "LGoalNext"]
              /\ UNCHANGED << paused, requestCh, waiting, handling, inputOpen, 
                              comp, steerComps, goalHeld, boot, crashes, 
                              toggles, gatePaused, badStart, parity, stack, pb, 
                              tb, gFound, gRunning, gIdle, sb, qb, wb, nb, lb, 
                              lRunning, lGoal >>

LGoalNext(self) == /\ pc[self] = "LGoalNext"
                   /\ boot = self - 100
                   /\ IF GoalActive
                         THEN /\ requestCh' = Append(requestCh, GoalReq(hand.comp))
                              /\ hand' = [hand EXCEPT !.comp = NoComp]
                         ELSE /\ TRUE
                              /\ UNCHANGED << requestCh, hand >>
                   /\ pc' = [pc EXCEPT ![self] = "LSettle"]
                   /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                                   waiting, handling, inputOpen, comp, 
                                   steerComps, goalHeld, boot, crashes, 
                                   toggles, gatePaused, badStart, parity, 
                                   stack, pb, tb, gFound, gRunning, gIdle, sb, 
                                   qb, wb, nb, lb, lRunning, lGoal >>

LSettle(self) == /\ pc[self] = "LSettle"
                 /\ boot = self - 100
                 /\ inputOpen' = FALSE
                 /\ comp' = [m \in Msgs |-> IF m \in steerComps \/ m = hand.comp THEN "closed" ELSE comp[m]]
                 /\ steerComps' = {}
                 /\ steers' = {}
                 /\ /\ pb' = [pb EXCEPT ![self] = self - 100]
                    /\ stack' = [stack EXCEPT ![self] = << [ procedure |->  "PickLaterWork",
                                                             pc        |->  "LIdle",
                                                             pb        |->  pb[self] ] >>
                                                         \o stack[self]]
                 /\ pc' = [pc EXCEPT ![self] = "PL1"]
                 /\ UNCHANGED << paused, st, goalLeft, activeTurn, requestCh, 
                                 waiting, handling, hand, goalHeld, boot, 
                                 crashes, toggles, gatePaused, badStart, 
                                 parity, tb, gFound, gRunning, gIdle, sb, qb, 
                                 wb, nb, lb, lRunning, lGoal >>

LIdle(self) == /\ pc[self] = "LIdle"
               /\ boot = self - 100
               /\ handling' = FALSE
               /\ hand' = NoReq
               /\ gatePaused' = FALSE
               /\ pc' = [pc EXCEPT ![self] = "LDequeue"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, inputOpen, comp, steerComps, 
                               goalHeld, boot, crashes, toggles, badStart, 
                               parity, stack, pb, tb, gFound, gRunning, gIdle, 
                               sb, qb, wb, nb, lb, lRunning, lGoal >>

LNotAdmitted(self) == /\ pc[self] = "LNotAdmitted"
                      /\ IF hand.comp # NoComp /\ Mutation # "no-close"
                            THEN /\ comp' = [comp EXCEPT ![hand.comp] = "closed"]
                            ELSE /\ TRUE
                                 /\ comp' = comp
                      /\ /\ pb' = [pb EXCEPT ![self] = self - 100]
                         /\ stack' = [stack EXCEPT ![self] = << [ procedure |->  "PickLaterWork",
                                                                  pc        |->  "LIdle",
                                                                  pb        |->  pb[self] ] >>
                                                              \o stack[self]]
                      /\ pc' = [pc EXCEPT ![self] = "PL1"]
                      /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                                      requestCh, waiting, handling, inputOpen, 
                                      hand, steerComps, goalHeld, boot, 
                                      crashes, toggles, gatePaused, badStart, 
                                      parity, tb, gFound, gRunning, gIdle, sb, 
                                      qb, wb, nb, lb, lRunning, lGoal >>

Loop(self) == LBoot(self) \/ LDequeue(self) \/ LGate(self)
                 \/ LActivate(self) \/ LStart(self) \/ LOpen(self)
                 \/ LEnd(self) \/ LGoalNext(self) \/ LSettle(self)
                 \/ LIdle(self) \/ LNotAdmitted(self)

TStart(self) == /\ pc[self] = "TStart"
                /\ toggles = self - 201
                /\ toggles' = self - 200
                /\ tb' = [tb EXCEPT ![self] = boot]
                /\ IF (self - 200) % 2 = 1
                      THEN /\ IF ~paused
                                 THEN /\ paused' = TRUE
                                      /\ inputOpen' = FALSE
                                 ELSE /\ TRUE
                                      /\ UNCHANGED << paused, inputOpen >>
                           /\ pc' = [pc EXCEPT ![self] = "TDone"]
                      ELSE /\ IF paused
                                 THEN /\ paused' = FALSE
                                      /\ pc' = [pc EXCEPT ![self] = "TGoal"]
                                 ELSE /\ pc' = [pc EXCEPT ![self] = "TDone"]
                                      /\ UNCHANGED paused
                           /\ UNCHANGED inputOpen
                /\ UNCHANGED << st, goalLeft, activeTurn, steers, requestCh, 
                                waiting, handling, hand, comp, steerComps, 
                                goalHeld, boot, crashes, gatePaused, badStart, 
                                parity, stack, pb, gFound, gRunning, gIdle, sb, 
                                qb, wb, nb, lb, lRunning, lGoal >>

TGoal(self) == /\ pc[self] = "TGoal"
               /\ boot = tb[self]
               /\ gFound' = [gFound EXCEPT ![self] = GoalActive]
               /\ pc' = [pc EXCEPT ![self] = "TRunning"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, inputOpen, hand, 
                               comp, steerComps, goalHeld, boot, crashes, 
                               toggles, gatePaused, badStart, parity, stack, 
                               pb, tb, gRunning, gIdle, sb, qb, wb, nb, lb, 
                               lRunning, lGoal >>

TRunning(self) == /\ pc[self] = "TRunning"
                  /\ boot = tb[self]
                  /\ gRunning' = [gRunning EXCEPT ![self] = activeTurn # NoItem]
                  /\ pc' = [pc EXCEPT ![self] = "TIdle"]
                  /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                                  requestCh, waiting, handling, inputOpen, 
                                  hand, comp, steerComps, goalHeld, boot, 
                                  crashes, toggles, gatePaused, badStart, 
                                  parity, stack, pb, tb, gFound, gIdle, sb, qb, 
                                  wb, nb, lb, lRunning, lGoal >>

TIdle(self) == /\ pc[self] = "TIdle"
               /\ boot = tb[self]
               /\ IF Mutation = "guess-idle"
                     THEN /\ gIdle' = [gIdle EXCEPT ![self] = ~handling /\ requestCh = <<>>]
                          /\ UNCHANGED goalHeld
                     ELSE /\ gIdle' = [gIdle EXCEPT ![self] = goalHeld]
                          /\ goalHeld' = FALSE
               /\ pc' = [pc EXCEPT ![self] = "TSubmit"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, inputOpen, hand, 
                               comp, steerComps, boot, crashes, toggles, 
                               gatePaused, badStart, parity, stack, pb, tb, 
                               gFound, gRunning, sb, qb, wb, nb, lb, lRunning, 
                               lGoal >>

TSubmit(self) == /\ pc[self] = "TSubmit"
                 /\ boot = tb[self]
                 /\ IF gFound[self] /\ ~gRunning[self] /\ gIdle[self] /\ Mutation # "no-resubmit"
                       THEN /\ requestCh' = Append(requestCh, GoalReq(NoComp))
                       ELSE /\ TRUE
                            /\ UNCHANGED requestCh
                 /\ /\ pb' = [pb EXCEPT ![self] = tb[self]]
                    /\ stack' = [stack EXCEPT ![self] = << [ procedure |->  "PickLaterWork",
                                                             pc        |->  "TDone",
                                                             pb        |->  pb[self] ] >>
                                                         \o stack[self]]
                 /\ pc' = [pc EXCEPT ![self] = "PL1"]
                 /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                                 waiting, handling, inputOpen, hand, comp, 
                                 steerComps, goalHeld, boot, crashes, toggles, 
                                 gatePaused, badStart, parity, tb, gFound, 
                                 gRunning, gIdle, sb, qb, wb, nb, lb, lRunning, 
                                 lGoal >>

TDone(self) == /\ pc[self] = "TDone"
               /\ tb' = [tb EXCEPT ![self] = 0]
               /\ gFound' = [gFound EXCEPT ![self] = FALSE]
               /\ gRunning' = [gRunning EXCEPT ![self] = FALSE]
               /\ gIdle' = [gIdle EXCEPT ![self] = FALSE]
               /\ pc' = [pc EXCEPT ![self] = "Done"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, inputOpen, hand, 
                               comp, steerComps, goalHeld, boot, crashes, 
                               toggles, gatePaused, badStart, parity, stack, 
                               pb, sb, qb, wb, nb, lb, lRunning, lGoal >>

Toggle(self) == TStart(self) \/ TGoal(self) \/ TRunning(self)
                   \/ TIdle(self) \/ TSubmit(self) \/ TDone(self)

SRoute(self) == /\ pc[self] = "SRoute"
                /\ sb' = [sb EXCEPT ![self] = boot]
                /\ IF ~paused
                      THEN /\ pc' = [pc EXCEPT ![self] = "SAdmit"]
                      ELSE /\ pc' = [pc EXCEPT ![self] = "SPost"]
                /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                                requestCh, waiting, handling, inputOpen, hand, 
                                comp, steerComps, goalHeld, boot, crashes, 
                                toggles, gatePaused, badStart, parity, stack, 
                                pb, tb, gFound, gRunning, gIdle, qb, wb, nb, 
                                lb, lRunning, lGoal >>

SPost(self) == /\ pc[self] = "SPost"
               /\ boot = sb[self]
               /\ IF paused
                     THEN /\ st' = [st EXCEPT ![self] = "posted"]
                          /\ pc' = [pc EXCEPT ![self] = "SDone"]
                     ELSE /\ pc' = [pc EXCEPT ![self] = "SAdmit"]
                          /\ st' = st
               /\ UNCHANGED << paused, goalLeft, activeTurn, steers, requestCh, 
                               waiting, handling, inputOpen, hand, comp, 
                               steerComps, goalHeld, boot, crashes, toggles, 
                               gatePaused, badStart, parity, stack, pb, tb, 
                               gFound, gRunning, gIdle, sb, qb, wb, nb, lb, 
                               lRunning, lGoal >>

SAdmit(self) == /\ pc[self] = "SAdmit"
                /\ boot = sb[self]
                /\ st' = [st EXCEPT ![self] = "row"]
                /\ IF ~SplitAdmit
                      THEN /\ comp' = [comp EXCEPT ![self] = "open"]
                           /\ IF inputOpen
                                 THEN /\ steers' = (steers \cup {self})
                                      /\ steerComps' = (steerComps \cup {self})
                                      /\ UNCHANGED requestCh
                                 ELSE /\ requestCh' = Append(requestCh, Req(self, self, TRUE, TRUE, FALSE))
                                      /\ UNCHANGED << steers, steerComps >>
                           /\ pc' = [pc EXCEPT ![self] = "SWait"]
                      ELSE /\ pc' = [pc EXCEPT ![self] = "SEnqueue"]
                           /\ UNCHANGED << steers, requestCh, comp, steerComps >>
                /\ UNCHANGED << paused, goalLeft, activeTurn, waiting, 
                                handling, inputOpen, hand, goalHeld, boot, 
                                crashes, toggles, gatePaused, badStart, parity, 
                                stack, pb, tb, gFound, gRunning, gIdle, sb, qb, 
                                wb, nb, lb, lRunning, lGoal >>

SEnqueue(self) == /\ pc[self] = "SEnqueue"
                  /\ boot = sb[self]
                  /\ comp' = [comp EXCEPT ![self] = "open"]
                  /\ IF inputOpen
                        THEN /\ steers' = (steers \cup {self})
                             /\ steerComps' = (steerComps \cup {self})
                             /\ UNCHANGED requestCh
                        ELSE /\ requestCh' = Append(requestCh, Req(self, self, TRUE, TRUE, FALSE))
                             /\ UNCHANGED << steers, steerComps >>
                  /\ pc' = [pc EXCEPT ![self] = "SWait"]
                  /\ UNCHANGED << paused, st, goalLeft, activeTurn, waiting, 
                                  handling, inputOpen, hand, goalHeld, boot, 
                                  crashes, toggles, gatePaused, badStart, 
                                  parity, stack, pb, tb, gFound, gRunning, 
                                  gIdle, sb, qb, wb, nb, lb, lRunning, lGoal >>

SWait(self) == /\ pc[self] = "SWait"
               /\ boot = sb[self] /\ comp[self] = "closed"
               /\ pc' = [pc EXCEPT ![self] = "SDone"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, inputOpen, hand, 
                               comp, steerComps, goalHeld, boot, crashes, 
                               toggles, gatePaused, badStart, parity, stack, 
                               pb, tb, gFound, gRunning, gIdle, sb, qb, wb, nb, 
                               lb, lRunning, lGoal >>

SDone(self) == /\ pc[self] = "SDone"
               /\ sb' = [sb EXCEPT ![self] = 0]
               /\ pc' = [pc EXCEPT ![self] = "Done"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, inputOpen, hand, 
                               comp, steerComps, goalHeld, boot, crashes, 
                               toggles, gatePaused, badStart, parity, stack, 
                               pb, tb, gFound, gRunning, gIdle, qb, wb, nb, lb, 
                               lRunning, lGoal >>

Sender(self) == SRoute(self) \/ SPost(self) \/ SAdmit(self)
                   \/ SEnqueue(self) \/ SWait(self) \/ SDone(self)

QAdmit(self) == /\ pc[self] = "QAdmit"
                /\ qb' = [qb EXCEPT ![self] = boot]
                /\ st' = [st EXCEPT ![self] = "row"]
                /\ pc' = [pc EXCEPT ![self] = "QEnqueue"]
                /\ UNCHANGED << paused, goalLeft, activeTurn, steers, 
                                requestCh, waiting, handling, inputOpen, hand, 
                                comp, steerComps, goalHeld, boot, crashes, 
                                toggles, gatePaused, badStart, parity, stack, 
                                pb, tb, gFound, gRunning, gIdle, sb, wb, nb, 
                                lb, lRunning, lGoal >>

QEnqueue(self) == /\ pc[self] = "QEnqueue"
                  /\ boot = qb[self]
                  /\ requestCh' = Append(requestCh, Req(self, NoComp, TRUE, TRUE, FALSE))
                  /\ qb' = [qb EXCEPT ![self] = 0]
                  /\ pc' = [pc EXCEPT ![self] = "Done"]
                  /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                                  waiting, handling, inputOpen, hand, comp, 
                                  steerComps, goalHeld, boot, crashes, toggles, 
                                  gatePaused, badStart, parity, stack, pb, tb, 
                                  gFound, gRunning, gIdle, sb, wb, nb, lb, 
                                  lRunning, lGoal >>

Queuer(self) == QAdmit(self) \/ QEnqueue(self)

WAdmit(self) == /\ pc[self] = "WAdmit"
                /\ wb' = [wb EXCEPT ![self] = boot]
                /\ st' = [st EXCEPT ![self] = "row"]
                /\ IF inputOpen
                      THEN /\ steers' = (steers \cup {self})
                           /\ pc' = [pc EXCEPT ![self] = "WDone"]
                      ELSE /\ pc' = [pc EXCEPT ![self] = "WPick"]
                           /\ UNCHANGED steers
                /\ UNCHANGED << paused, goalLeft, activeTurn, requestCh, 
                                waiting, handling, inputOpen, hand, comp, 
                                steerComps, goalHeld, boot, crashes, toggles, 
                                gatePaused, badStart, parity, stack, pb, tb, 
                                gFound, gRunning, gIdle, sb, qb, nb, lb, 
                                lRunning, lGoal >>

WPick(self) == /\ pc[self] = "WPick"
               /\ /\ pb' = [pb EXCEPT ![self] = wb[self]]
                  /\ stack' = [stack EXCEPT ![self] = << [ procedure |->  "PickLaterWork",
                                                           pc        |->  "WDone",
                                                           pb        |->  pb[self] ] >>
                                                       \o stack[self]]
               /\ pc' = [pc EXCEPT ![self] = "PL1"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, inputOpen, hand, 
                               comp, steerComps, goalHeld, boot, crashes, 
                               toggles, gatePaused, badStart, parity, tb, 
                               gFound, gRunning, gIdle, sb, qb, wb, nb, lb, 
                               lRunning, lGoal >>

WDone(self) == /\ pc[self] = "WDone"
               /\ wb' = [wb EXCEPT ![self] = 0]
               /\ pc' = [pc EXCEPT ![self] = "Done"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, inputOpen, hand, 
                               comp, steerComps, goalHeld, boot, crashes, 
                               toggles, gatePaused, badStart, parity, stack, 
                               pb, tb, gFound, gRunning, gIdle, sb, qb, nb, lb, 
                               lRunning, lGoal >>

Writer(self) == WAdmit(self) \/ WPick(self) \/ WDone(self)

NCreate(self) == /\ pc[self] = "NCreate"
                 /\ nb' = [nb EXCEPT ![self] = boot]
                 /\ st' = [st EXCEPT ![self] = "row"]
                 /\ pc' = [pc EXCEPT ![self] = "NPick"]
                 /\ UNCHANGED << paused, goalLeft, activeTurn, steers, 
                                 requestCh, waiting, handling, inputOpen, hand, 
                                 comp, steerComps, goalHeld, boot, crashes, 
                                 toggles, gatePaused, badStart, parity, stack, 
                                 pb, tb, gFound, gRunning, gIdle, sb, qb, wb, 
                                 lb, lRunning, lGoal >>

NPick(self) == /\ pc[self] = "NPick"
               /\ /\ pb' = [pb EXCEPT ![self] = nb[self]]
                  /\ stack' = [stack EXCEPT ![self] = << [ procedure |->  "PickLaterWork",
                                                           pc        |->  "NDone",
                                                           pb        |->  pb[self] ] >>
                                                       \o stack[self]]
               /\ pc' = [pc EXCEPT ![self] = "PL1"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, inputOpen, hand, 
                               comp, steerComps, goalHeld, boot, crashes, 
                               toggles, gatePaused, badStart, parity, tb, 
                               gFound, gRunning, gIdle, sb, qb, wb, nb, lb, 
                               lRunning, lGoal >>

NDone(self) == /\ pc[self] = "NDone"
               /\ nb' = [nb EXCEPT ![self] = 0]
               /\ pc' = [pc EXCEPT ![self] = "Done"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               requestCh, waiting, handling, inputOpen, hand, 
                               comp, steerComps, goalHeld, boot, crashes, 
                               toggles, gatePaused, badStart, parity, stack, 
                               pb, tb, gFound, gRunning, gIdle, sb, qb, wb, lb, 
                               lRunning, lGoal >>

Source(self) == NCreate(self) \/ NPick(self) \/ NDone(self)

LCrash == /\ pc[300] = "LCrash"
          /\ \/ /\ pc' = [pc EXCEPT ![300] = "LCDone"]
                /\ UNCHANGED <<st, steers, requestCh, waiting, handling, inputOpen, hand, comp, steerComps, goalHeld, boot, crashes, gatePaused, lb>>
             \/ /\ crashes < MaxCrashes
                /\ crashes' = crashes + 1
                /\ boot' = boot + 1
                /\ lb' = boot'
                /\ requestCh' = <<>>
                /\ waiting' = {}
                /\ handling' = FALSE
                /\ inputOpen' = FALSE
                /\ hand' = NoReq
                /\ goalHeld' = FALSE
                /\ gatePaused' = FALSE
                /\ comp' = [m \in Msgs |-> IF comp[m] = "open" THEN "dead" ELSE comp[m]]
                /\ steerComps' = {}
                /\ st' = [i \in Items |-> IF st[i] = "fired" THEN "missed" ELSE st[i]]
                /\ steers' = (IF activeTurn # NoItem THEN steers ELSE {})
                /\ pc' = [pc EXCEPT ![300] = "LBootGoals"]
          /\ UNCHANGED << paused, goalLeft, activeTurn, toggles, badStart, 
                          parity, stack, pb, tb, gFound, gRunning, gIdle, sb, 
                          qb, wb, nb, lRunning, lGoal >>

LBootGoals == /\ pc[300] = "LBootGoals"
              /\ boot = lb
              /\ lRunning' = (activeTurn # NoItem)
              /\ lGoal' = GoalActive
              /\ pc' = [pc EXCEPT ![300] = "LBootSubmit"]
              /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                              requestCh, waiting, handling, inputOpen, hand, 
                              comp, steerComps, goalHeld, boot, crashes, 
                              toggles, gatePaused, badStart, parity, stack, pb, 
                              tb, gFound, gRunning, gIdle, sb, qb, wb, nb, lb >>

LBootSubmit == /\ pc[300] = "LBootSubmit"
               /\ boot = lb
               /\ IF lGoal /\ ~lRunning
                     THEN /\ requestCh' = Append(requestCh, GoalReq(NoComp))
                     ELSE /\ TRUE
                          /\ UNCHANGED requestCh
               /\ /\ pb' = [pb EXCEPT ![300] = lb]
                  /\ stack' = [stack EXCEPT ![300] = << [ procedure |->  "PickLaterWork",
                                                          pc        |->  "LBootDone",
                                                          pb        |->  pb[300] ] >>
                                                      \o stack[300]]
               /\ pc' = [pc EXCEPT ![300] = "PL1"]
               /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                               waiting, handling, inputOpen, hand, comp, 
                               steerComps, goalHeld, boot, crashes, toggles, 
                               gatePaused, badStart, parity, tb, gFound, 
                               gRunning, gIdle, sb, qb, wb, nb, lb, lRunning, 
                               lGoal >>

LBootDone == /\ pc[300] = "LBootDone"
             /\ lb' = 0
             /\ lRunning' = FALSE
             /\ lGoal' = FALSE
             /\ pc' = [pc EXCEPT ![300] = "LCDone"]
             /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, 
                             requestCh, waiting, handling, inputOpen, hand, 
                             comp, steerComps, goalHeld, boot, crashes, 
                             toggles, gatePaused, badStart, parity, stack, pb, 
                             tb, gFound, gRunning, gIdle, sb, qb, wb, nb >>

LCDone == /\ pc[300] = "LCDone"
          /\ TRUE
          /\ pc' = [pc EXCEPT ![300] = "Done"]
          /\ UNCHANGED << paused, st, goalLeft, activeTurn, steers, requestCh, 
                          waiting, handling, inputOpen, hand, comp, steerComps, 
                          goalHeld, boot, crashes, toggles, gatePaused, 
                          badStart, parity, stack, pb, tb, gFound, gRunning, 
                          gIdle, sb, qb, wb, nb, lb, lRunning, lGoal >>

Lifecycle == LCrash \/ LBootGoals \/ LBootSubmit \/ LBootDone \/ LCDone

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == Lifecycle
           \/ (\E self \in ProcSet: PickLaterWork(self))
           \/ (\E self \in {100 + k : k \in 0..MaxCrashes}: Loop(self))
           \/ (\E self \in {200 + t : t \in 1..MaxToggles}: Toggle(self))
           \/ (\E self \in Msgs: Sender(self))
           \/ (\E self \in QItems: Queuer(self))
           \/ (\E self \in Writes: Writer(self))
           \/ (\E self \in Notes \cup Scheds: Source(self))
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in {100 + k : k \in 0..MaxCrashes} : WF_vars(Loop(self)) /\ WF_vars(PickLaterWork(self))
        /\ \A self \in {200 + t : t \in 1..MaxToggles} : WF_vars(Toggle(self)) /\ WF_vars(PickLaterWork(self))
        /\ \A self \in Msgs : WF_vars(Sender(self))
        /\ \A self \in QItems : WF_vars(Queuer(self))
        /\ \A self \in Writes : WF_vars(Writer(self)) /\ WF_vars(PickLaterWork(self))
        /\ \A self \in Notes \cup Scheds : WF_vars(Source(self)) /\ WF_vars(PickLaterWork(self))
        /\ WF_vars(Lifecycle) /\ WF_vars(PickLaterWork(300))

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION
=============================================================================
