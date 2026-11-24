---------------------- MODULE CronFollowUpUnloadedAgent ----------------------
EXTENDS Integers, FiniteSets

CONSTANTS FollowUps, MaxToggles, MaxRestarts, DropAt, SplitDrop, ChatDropsUnloaded, AllowSwitch

ASSUME DropAt \in {"none", "unrecorded", "unbound"}
ASSUME SplitDrop \in BOOLEAN /\ ChatDropsUnloaded \in BOOLEAN /\ AllowSwitch \in BOOLEAN
ASSUME MaxToggles \in Nat /\ MaxRestarts \in Nat

(* --algorithm CronFollowUpUnloadedAgent {
variables
    loaded \in BOOLEAN,
    toggles = 0,
    restarts = 0,
    chat = FALSE,
    chatAgent = "run",
    bound = FALSE,
    turn = "running",
    reply = FALSE,
    rStage = "none",
    fu = [f \in FollowUps |-> "none"],
    due = [f \in FollowUps |-> FALSE],
    runTimers = {},
    chatTimers = {},
    runPick = FALSE,
    chatPick = FALSE,
    runLock = FALSE,
    hStage = "idle",
    hLoaded = FALSE,
    pendingDel = {},
    chatTurn = "none",
    oStage = "idle",
    everDelivered = {},
    everDropped = {},
    deliveredBy = [f \in FollowUps |-> "none"],
    dropBy = [f \in FollowUps |-> "none"],
    dropFrom = [f \in FollowUps |-> "none"],
    dropDecidedLoaded = [f \in FollowUps |-> FALSE],
    dropCommitLoaded = [f \in FollowUps |-> FALSE],
    deletes = [f \in FollowUps |-> 0],
    boundOnUnloaded = FALSE,
    droppedBesideChat = FALSE;

define {
    RunRows == {f \in FollowUps : fu[f] = "run"}
    ChatRows == {f \in FollowUps : fu[f] = "chat"}
    DueRunRows == {f \in RunRows : due[f]}
    Rehomed == [f \in FollowUps |-> IF fu[f] = "run" THEN "chat" ELSE fu[f]]
    Terminal == {"delivered", "dropped", "failed"}

    TypeOK ==
        /\ fu \in [FollowUps -> {"none", "run", "chat", "running", "delivered", "failed", "dropped"}]
        /\ turn \in {"running", "delivering", "done"}
        /\ rStage \in {"none", "check", "record", "bind", "close"}
        /\ hStage \in {"idle", "act"}
        /\ oStage \in {"idle", "check", "create", "bind", "done"}
        /\ chatAgent \in {"run", "other"}
        /\ chatTurn \in FollowUps \cup {"none"}
        /\ runTimers \subseteq FollowUps /\ chatTimers \subseteq FollowUps /\ pendingDel \subseteq FollowUps
        /\ bound => chat

    NeverDeliveredAndDropped == everDelivered \cap everDropped = {}
    OwnAgentOnly == \A f \in FollowUps : deliveredBy[f] \in {"none", "run"}
    DropOnlyOnUnloadedDecision == \A f \in everDropped : ~dropDecidedLoaded[f]
    DropOnlyOwnRows ==
        \A f \in everDropped :
            /\ dropBy[f] = "handoff" => dropFrom[f] = "run"
            /\ dropBy[f] = "chat" => dropFrom[f] \in {"chat", "running"}
    NoLateOrDoubleDelete ==
        \A f \in FollowUps :
            /\ deletes[f] <= 1
            /\ f \in everDropped => dropFrom[f] \in {"run", "chat"} \/ (dropBy[f] = "chat" /\ ChatDropsUnloaded)
    NoBindOnUnloadedDecision == ~boundOnUnloaded
    NoDropWhileLoadedAtCommit == \A f \in everDropped : ~dropCommitLoaded[f]

    DueSettles == \A f \in FollowUps : due[f] ~> fu[f] \in Terminal
    DueDeliveredOrDropped == \A f \in FollowUps : due[f] ~> fu[f] \in {"delivered", "dropped"}
    TurnCloses == <>(turn = "done")

    NoDropThenDelivery == ~\E f, g \in FollowUps : fu[f] = "dropped" /\ fu[g] = "delivered"
    NoFailureInChat == ~\E f \in FollowUps : fu[f] = "failed"
    NoDropAfterRestart == ~(restarts > 0 /\ \E f \in FollowUps : fu[f] = "dropped" /\ dropBy[f] = "handoff")
    NoDropBesideUnboundChat == ~droppedBesideChat
}

process (Admin = "admin") {
adm:
    while (TRUE) {
        await toggles < MaxToggles;
        loaded := ~loaded;
        toggles := toggles + 1;
    }
}

process (Switcher = "switch") {
sw:
    await AllowSwitch /\ chat /\ chatAgent = "run";
    chatAgent := "other";
}

process (Person = "person") {
click:
    await oStage = "idle";
    oStage := "check";
}

process (Restart = "restart") {
rs:
    while (TRUE) {
        await restarts < MaxRestarts;
        restarts := restarts + 1;
        runTimers := RunRows;
        chatTimers := ChatRows;
        runPick := RunRows # {};
        chatPick := FALSE;
        hStage := "idle";
        runLock := FALSE;
        pendingDel := {};
        if (oStage \in {"check", "create", "bind"}) { oStage := "done" };
        if (turn = "delivering") { rStage := "check" };
    }
}

fair process (Clock = "clock") {
clk:
    while (TRUE) {
        with (f \in {g \in FollowUps : ~due[g] /\ fu[g] # "none"}) {
            due[f] := TRUE;
        }
    }
}

fair process (Timers = "timers") {
tm:
    while (TRUE) {
        either {
            with (f \in {g \in runTimers : due[g]}) {
                runTimers := runTimers \ {f};
                runPick := TRUE;
            }
        } or {
            with (f \in {g \in chatTimers : due[g]}) {
                chatTimers := chatTimers \ {f};
                chatPick := TRUE;
            }
        }
    }
}

fair process (Run = "run") {
rt:
    while (TRUE) {
        either {
            await turn = "running" /\ ~runLock;
            with (r \in BOOLEAN) { reply := r };
            if (bound) {
                fu := [f \in FollowUps |-> "chat"];
                chatTimers := chatTimers \cup FollowUps;
            } else {
                fu := [f \in FollowUps |-> "run"];
                runTimers := runTimers \cup FollowUps;
            };
            turn := "delivering";
            rStage := "check";
        } or {
            await turn = "delivering" /\ rStage = "check";
            if (reply /\ loaded) { rStage := "record" } else { rStage := "close" };
        } or {
            await rStage = "record";
            chat := TRUE;
            rStage := "bind";
        } or {
            await rStage = "bind" /\ ~runLock;
            bound := TRUE;
            chatTimers := chatTimers \cup RunRows;
            fu := Rehomed;
            rStage := "close";
        } or {
            await rStage = "close";
            turn := "done";
            rStage := "none";
            runPick := TRUE;
        }
    }
}

fair process (Handoff = "handoff") {
ho:
    while (TRUE) {
        either {
            await hStage = "idle" /\ runPick /\ ~runLock;
            runPick := FALSE;
            if (turn # "done") {
                skip;
            } else if (bound) {
                chatPick := TRUE;
            } else if (RunRows = {}) {
                skip;
            } else if (DueRunRows = {}) {
                with (e \in RunRows) { runTimers := runTimers \cup {e} };
            } else {
                hLoaded := loaded;
                runLock := TRUE;
                hStage := "act";
            }
        } or {
            await hStage = "act";
            if (~hLoaded /\ (DropAt = "unbound" \/ ~chat)) {
                if (DropAt = "none") {
                    skip;
                } else if (SplitDrop) {
                    pendingDel := pendingDel \cup DueRunRows;
                } else {
                    dropFrom := [f \in FollowUps |-> IF f \in DueRunRows THEN fu[f] ELSE dropFrom[f]];
                    dropBy := [f \in FollowUps |-> IF f \in DueRunRows THEN "handoff" ELSE dropBy[f]];
                    dropDecidedLoaded := [f \in FollowUps |-> IF f \in DueRunRows THEN hLoaded ELSE dropDecidedLoaded[f]];
                    dropCommitLoaded := [f \in FollowUps |-> IF f \in DueRunRows THEN loaded ELSE dropCommitLoaded[f]];
                    deletes := [f \in FollowUps |-> IF f \in DueRunRows THEN deletes[f] + 1 ELSE deletes[f]];
                    everDropped := everDropped \cup DueRunRows;
                    if (chat) { droppedBesideChat := TRUE };
                    fu := [f \in FollowUps |-> IF f \in DueRunRows THEN "dropped" ELSE fu[f]];
                }
            } else {
                if (~hLoaded) { boundOnUnloaded := TRUE };
                chat := TRUE;
                bound := TRUE;
                chatTimers := chatTimers \cup RunRows;
                fu := Rehomed;
            };
            runLock := FALSE;
            hStage := "idle";
        } or {
            with (f \in pendingDel) {
                pendingDel := pendingDel \ {f};
                if (fu[f] \in {"run", "chat", "running"}) {
                    dropFrom[f] := fu[f];
                    dropBy[f] := "handoff";
                    dropDecidedLoaded[f] := FALSE;
                    dropCommitLoaded[f] := loaded;
                    deletes[f] := deletes[f] + 1;
                    everDropped := everDropped \cup {f};
                    fu[f] := "dropped";
                }
            }
        }
    }
}

fair process (Chat = "chat") {
ch:
    while (TRUE) {
        either {
            await chatPick /\ chatTurn = "none";
            if (\E f \in ChatRows : due[f]) {
                with (f \in {g \in ChatRows : due[g]}) {
                    fu[f] := "running";
                    chatTurn := f;
                }
            } else {
                chatPick := FALSE;
            }
        } or {
            await chatTurn # "none";
            if (chatAgent = "other" \/ loaded) {
                fu[chatTurn] := "delivered";
                everDelivered := everDelivered \cup {chatTurn};
                deliveredBy[chatTurn] := chatAgent;
            } else if (ChatDropsUnloaded) {
                dropFrom[chatTurn] := fu[chatTurn];
                dropBy[chatTurn] := "chat";
                dropDecidedLoaded[chatTurn] := loaded;
                dropCommitLoaded[chatTurn] := loaded;
                deletes[chatTurn] := deletes[chatTurn] + 1;
                everDropped := everDropped \cup {chatTurn};
                fu[chatTurn] := "dropped";
            } else {
                fu[chatTurn] := "failed";
            };
            chatTurn := "none";
            chatPick := TRUE;
        }
    }
}

fair process (OpenChat = "open") {
oc:
    while (TRUE) {
        either {
            await oStage = "check";
            if (loaded) { oStage := "create" } else { oStage := "done" };
        } or {
            await oStage = "create";
            chat := TRUE;
            oStage := "bind";
        } or {
            await oStage = "bind" /\ ~runLock;
            bound := TRUE;
            chatTimers := chatTimers \cup RunRows;
            fu := Rehomed;
            oStage := "done";
        }
    }
}
} *)
\* BEGIN TRANSLATION
VARIABLES pc, loaded, toggles, restarts, chat, chatAgent, bound, turn, reply, 
          rStage, fu, due, runTimers, chatTimers, runPick, chatPick, runLock, 
          hStage, hLoaded, pendingDel, chatTurn, oStage, everDelivered, 
          everDropped, deliveredBy, dropBy, dropFrom, dropDecidedLoaded, 
          dropCommitLoaded, deletes, boundOnUnloaded, droppedBesideChat

(* define statement *)
RunRows == {f \in FollowUps : fu[f] = "run"}
ChatRows == {f \in FollowUps : fu[f] = "chat"}
DueRunRows == {f \in RunRows : due[f]}
Rehomed == [f \in FollowUps |-> IF fu[f] = "run" THEN "chat" ELSE fu[f]]
Terminal == {"delivered", "dropped", "failed"}

TypeOK ==
    /\ fu \in [FollowUps -> {"none", "run", "chat", "running", "delivered", "failed", "dropped"}]
    /\ turn \in {"running", "delivering", "done"}
    /\ rStage \in {"none", "check", "record", "bind", "close"}
    /\ hStage \in {"idle", "act"}
    /\ oStage \in {"idle", "check", "create", "bind", "done"}
    /\ chatAgent \in {"run", "other"}
    /\ chatTurn \in FollowUps \cup {"none"}
    /\ runTimers \subseteq FollowUps /\ chatTimers \subseteq FollowUps /\ pendingDel \subseteq FollowUps
    /\ bound => chat

NeverDeliveredAndDropped == everDelivered \cap everDropped = {}
OwnAgentOnly == \A f \in FollowUps : deliveredBy[f] \in {"none", "run"}
DropOnlyOnUnloadedDecision == \A f \in everDropped : ~dropDecidedLoaded[f]
DropOnlyOwnRows ==
    \A f \in everDropped :
        /\ dropBy[f] = "handoff" => dropFrom[f] = "run"
        /\ dropBy[f] = "chat" => dropFrom[f] \in {"chat", "running"}
NoLateOrDoubleDelete ==
    \A f \in FollowUps :
        /\ deletes[f] <= 1
        /\ f \in everDropped => dropFrom[f] \in {"run", "chat"} \/ (dropBy[f] = "chat" /\ ChatDropsUnloaded)
NoBindOnUnloadedDecision == ~boundOnUnloaded
NoDropWhileLoadedAtCommit == \A f \in everDropped : ~dropCommitLoaded[f]

DueSettles == \A f \in FollowUps : due[f] ~> fu[f] \in Terminal
DueDeliveredOrDropped == \A f \in FollowUps : due[f] ~> fu[f] \in {"delivered", "dropped"}
TurnCloses == <>(turn = "done")

NoDropThenDelivery == ~\E f, g \in FollowUps : fu[f] = "dropped" /\ fu[g] = "delivered"
NoFailureInChat == ~\E f \in FollowUps : fu[f] = "failed"
NoDropAfterRestart == ~(restarts > 0 /\ \E f \in FollowUps : fu[f] = "dropped" /\ dropBy[f] = "handoff")
NoDropBesideUnboundChat == ~droppedBesideChat


vars == << pc, loaded, toggles, restarts, chat, chatAgent, bound, turn, reply, 
           rStage, fu, due, runTimers, chatTimers, runPick, chatPick, runLock, 
           hStage, hLoaded, pendingDel, chatTurn, oStage, everDelivered, 
           everDropped, deliveredBy, dropBy, dropFrom, dropDecidedLoaded, 
           dropCommitLoaded, deletes, boundOnUnloaded, droppedBesideChat >>

ProcSet == {"admin"} \cup {"switch"} \cup {"person"} \cup {"restart"} \cup {"clock"} \cup {"timers"} \cup {"run"} \cup {"handoff"} \cup {"chat"} \cup {"open"}

Init == (* Global variables *)
        /\ loaded \in BOOLEAN
        /\ toggles = 0
        /\ restarts = 0
        /\ chat = FALSE
        /\ chatAgent = "run"
        /\ bound = FALSE
        /\ turn = "running"
        /\ reply = FALSE
        /\ rStage = "none"
        /\ fu = [f \in FollowUps |-> "none"]
        /\ due = [f \in FollowUps |-> FALSE]
        /\ runTimers = {}
        /\ chatTimers = {}
        /\ runPick = FALSE
        /\ chatPick = FALSE
        /\ runLock = FALSE
        /\ hStage = "idle"
        /\ hLoaded = FALSE
        /\ pendingDel = {}
        /\ chatTurn = "none"
        /\ oStage = "idle"
        /\ everDelivered = {}
        /\ everDropped = {}
        /\ deliveredBy = [f \in FollowUps |-> "none"]
        /\ dropBy = [f \in FollowUps |-> "none"]
        /\ dropFrom = [f \in FollowUps |-> "none"]
        /\ dropDecidedLoaded = [f \in FollowUps |-> FALSE]
        /\ dropCommitLoaded = [f \in FollowUps |-> FALSE]
        /\ deletes = [f \in FollowUps |-> 0]
        /\ boundOnUnloaded = FALSE
        /\ droppedBesideChat = FALSE
        /\ pc = [self \in ProcSet |-> CASE self = "admin" -> "adm"
                                        [] self = "switch" -> "sw"
                                        [] self = "person" -> "click"
                                        [] self = "restart" -> "rs"
                                        [] self = "clock" -> "clk"
                                        [] self = "timers" -> "tm"
                                        [] self = "run" -> "rt"
                                        [] self = "handoff" -> "ho"
                                        [] self = "chat" -> "ch"
                                        [] self = "open" -> "oc"]

adm == /\ pc["admin"] = "adm"
       /\ toggles < MaxToggles
       /\ loaded' = ~loaded
       /\ toggles' = toggles + 1
       /\ pc' = [pc EXCEPT !["admin"] = "adm"]
       /\ UNCHANGED << restarts, chat, chatAgent, bound, turn, reply, rStage, 
                       fu, due, runTimers, chatTimers, runPick, chatPick, 
                       runLock, hStage, hLoaded, pendingDel, chatTurn, oStage, 
                       everDelivered, everDropped, deliveredBy, dropBy, 
                       dropFrom, dropDecidedLoaded, dropCommitLoaded, deletes, 
                       boundOnUnloaded, droppedBesideChat >>

Admin == adm

sw == /\ pc["switch"] = "sw"
      /\ AllowSwitch /\ chat /\ chatAgent = "run"
      /\ chatAgent' = "other"
      /\ pc' = [pc EXCEPT !["switch"] = "Done"]
      /\ UNCHANGED << loaded, toggles, restarts, chat, bound, turn, reply, 
                      rStage, fu, due, runTimers, chatTimers, runPick, 
                      chatPick, runLock, hStage, hLoaded, pendingDel, chatTurn, 
                      oStage, everDelivered, everDropped, deliveredBy, dropBy, 
                      dropFrom, dropDecidedLoaded, dropCommitLoaded, deletes, 
                      boundOnUnloaded, droppedBesideChat >>

Switcher == sw

click == /\ pc["person"] = "click"
         /\ oStage = "idle"
         /\ oStage' = "check"
         /\ pc' = [pc EXCEPT !["person"] = "Done"]
         /\ UNCHANGED << loaded, toggles, restarts, chat, chatAgent, bound, 
                         turn, reply, rStage, fu, due, runTimers, chatTimers, 
                         runPick, chatPick, runLock, hStage, hLoaded, 
                         pendingDel, chatTurn, everDelivered, everDropped, 
                         deliveredBy, dropBy, dropFrom, dropDecidedLoaded, 
                         dropCommitLoaded, deletes, boundOnUnloaded, 
                         droppedBesideChat >>

Person == click

rs == /\ pc["restart"] = "rs"
      /\ restarts < MaxRestarts
      /\ restarts' = restarts + 1
      /\ runTimers' = RunRows
      /\ chatTimers' = ChatRows
      /\ runPick' = (RunRows # {})
      /\ chatPick' = FALSE
      /\ hStage' = "idle"
      /\ runLock' = FALSE
      /\ pendingDel' = {}
      /\ IF oStage \in {"check", "create", "bind"}
            THEN /\ oStage' = "done"
            ELSE /\ TRUE
                 /\ UNCHANGED oStage
      /\ IF turn = "delivering"
            THEN /\ rStage' = "check"
            ELSE /\ TRUE
                 /\ UNCHANGED rStage
      /\ pc' = [pc EXCEPT !["restart"] = "rs"]
      /\ UNCHANGED << loaded, toggles, chat, chatAgent, bound, turn, reply, fu, 
                      due, hLoaded, chatTurn, everDelivered, everDropped, 
                      deliveredBy, dropBy, dropFrom, dropDecidedLoaded, 
                      dropCommitLoaded, deletes, boundOnUnloaded, 
                      droppedBesideChat >>

Restart == rs

clk == /\ pc["clock"] = "clk"
       /\ \E f \in {g \in FollowUps : ~due[g] /\ fu[g] # "none"}:
            due' = [due EXCEPT ![f] = TRUE]
       /\ pc' = [pc EXCEPT !["clock"] = "clk"]
       /\ UNCHANGED << loaded, toggles, restarts, chat, chatAgent, bound, turn, 
                       reply, rStage, fu, runTimers, chatTimers, runPick, 
                       chatPick, runLock, hStage, hLoaded, pendingDel, 
                       chatTurn, oStage, everDelivered, everDropped, 
                       deliveredBy, dropBy, dropFrom, dropDecidedLoaded, 
                       dropCommitLoaded, deletes, boundOnUnloaded, 
                       droppedBesideChat >>

Clock == clk

tm == /\ pc["timers"] = "tm"
      /\ \/ /\ \E f \in {g \in runTimers : due[g]}:
                 /\ runTimers' = runTimers \ {f}
                 /\ runPick' = TRUE
            /\ UNCHANGED <<chatTimers, chatPick>>
         \/ /\ \E f \in {g \in chatTimers : due[g]}:
                 /\ chatTimers' = chatTimers \ {f}
                 /\ chatPick' = TRUE
            /\ UNCHANGED <<runTimers, runPick>>
      /\ pc' = [pc EXCEPT !["timers"] = "tm"]
      /\ UNCHANGED << loaded, toggles, restarts, chat, chatAgent, bound, turn, 
                      reply, rStage, fu, due, runLock, hStage, hLoaded, 
                      pendingDel, chatTurn, oStage, everDelivered, everDropped, 
                      deliveredBy, dropBy, dropFrom, dropDecidedLoaded, 
                      dropCommitLoaded, deletes, boundOnUnloaded, 
                      droppedBesideChat >>

Timers == tm

rt == /\ pc["run"] = "rt"
      /\ \/ /\ turn = "running" /\ ~runLock
            /\ \E r \in BOOLEAN:
                 reply' = r
            /\ IF bound
                  THEN /\ fu' = [f \in FollowUps |-> "chat"]
                       /\ chatTimers' = (chatTimers \cup FollowUps)
                       /\ UNCHANGED runTimers
                  ELSE /\ fu' = [f \in FollowUps |-> "run"]
                       /\ runTimers' = (runTimers \cup FollowUps)
                       /\ UNCHANGED chatTimers
            /\ turn' = "delivering"
            /\ rStage' = "check"
            /\ UNCHANGED <<chat, bound, runPick>>
         \/ /\ turn = "delivering" /\ rStage = "check"
            /\ IF reply /\ loaded
                  THEN /\ rStage' = "record"
                  ELSE /\ rStage' = "close"
            /\ UNCHANGED <<chat, bound, turn, reply, fu, runTimers, chatTimers, runPick>>
         \/ /\ rStage = "record"
            /\ chat' = TRUE
            /\ rStage' = "bind"
            /\ UNCHANGED <<bound, turn, reply, fu, runTimers, chatTimers, runPick>>
         \/ /\ rStage = "bind" /\ ~runLock
            /\ bound' = TRUE
            /\ chatTimers' = (chatTimers \cup RunRows)
            /\ fu' = Rehomed
            /\ rStage' = "close"
            /\ UNCHANGED <<chat, turn, reply, runTimers, runPick>>
         \/ /\ rStage = "close"
            /\ turn' = "done"
            /\ rStage' = "none"
            /\ runPick' = TRUE
            /\ UNCHANGED <<chat, bound, reply, fu, runTimers, chatTimers>>
      /\ pc' = [pc EXCEPT !["run"] = "rt"]
      /\ UNCHANGED << loaded, toggles, restarts, chatAgent, due, chatPick, 
                      runLock, hStage, hLoaded, pendingDel, chatTurn, oStage, 
                      everDelivered, everDropped, deliveredBy, dropBy, 
                      dropFrom, dropDecidedLoaded, dropCommitLoaded, deletes, 
                      boundOnUnloaded, droppedBesideChat >>

Run == rt

ho == /\ pc["handoff"] = "ho"
      /\ \/ /\ hStage = "idle" /\ runPick /\ ~runLock
            /\ runPick' = FALSE
            /\ IF turn # "done"
                  THEN /\ TRUE
                       /\ UNCHANGED << runTimers, chatPick, runLock, hStage, 
                                       hLoaded >>
                  ELSE /\ IF bound
                             THEN /\ chatPick' = TRUE
                                  /\ UNCHANGED << runTimers, runLock, hStage, 
                                                  hLoaded >>
                             ELSE /\ IF RunRows = {}
                                        THEN /\ TRUE
                                             /\ UNCHANGED << runTimers, 
                                                             runLock, hStage, 
                                                             hLoaded >>
                                        ELSE /\ IF DueRunRows = {}
                                                   THEN /\ \E e \in RunRows:
                                                             runTimers' = (runTimers \cup {e})
                                                        /\ UNCHANGED << runLock, 
                                                                        hStage, 
                                                                        hLoaded >>
                                                   ELSE /\ hLoaded' = loaded
                                                        /\ runLock' = TRUE
                                                        /\ hStage' = "act"
                                                        /\ UNCHANGED runTimers
                                  /\ UNCHANGED chatPick
            /\ UNCHANGED <<chat, bound, fu, chatTimers, pendingDel, everDropped, dropBy, dropFrom, dropDecidedLoaded, dropCommitLoaded, deletes, boundOnUnloaded, droppedBesideChat>>
         \/ /\ hStage = "act"
            /\ IF ~hLoaded /\ (DropAt = "unbound" \/ ~chat)
                  THEN /\ IF DropAt = "none"
                             THEN /\ TRUE
                                  /\ UNCHANGED << fu, pendingDel, everDropped, 
                                                  dropBy, dropFrom, 
                                                  dropDecidedLoaded, 
                                                  dropCommitLoaded, deletes, 
                                                  droppedBesideChat >>
                             ELSE /\ IF SplitDrop
                                        THEN /\ pendingDel' = (pendingDel \cup DueRunRows)
                                             /\ UNCHANGED << fu, everDropped, 
                                                             dropBy, dropFrom, 
                                                             dropDecidedLoaded, 
                                                             dropCommitLoaded, 
                                                             deletes, 
                                                             droppedBesideChat >>
                                        ELSE /\ dropFrom' = [f \in FollowUps |-> IF f \in DueRunRows THEN fu[f] ELSE dropFrom[f]]
                                             /\ dropBy' = [f \in FollowUps |-> IF f \in DueRunRows THEN "handoff" ELSE dropBy[f]]
                                             /\ dropDecidedLoaded' = [f \in FollowUps |-> IF f \in DueRunRows THEN hLoaded ELSE dropDecidedLoaded[f]]
                                             /\ dropCommitLoaded' = [f \in FollowUps |-> IF f \in DueRunRows THEN loaded ELSE dropCommitLoaded[f]]
                                             /\ deletes' = [f \in FollowUps |-> IF f \in DueRunRows THEN deletes[f] + 1 ELSE deletes[f]]
                                             /\ everDropped' = (everDropped \cup DueRunRows)
                                             /\ IF chat
                                                   THEN /\ droppedBesideChat' = TRUE
                                                   ELSE /\ TRUE
                                                        /\ UNCHANGED droppedBesideChat
                                             /\ fu' = [f \in FollowUps |-> IF f \in DueRunRows THEN "dropped" ELSE fu[f]]
                                             /\ UNCHANGED pendingDel
                       /\ UNCHANGED << chat, bound, chatTimers, 
                                       boundOnUnloaded >>
                  ELSE /\ IF ~hLoaded
                             THEN /\ boundOnUnloaded' = TRUE
                             ELSE /\ TRUE
                                  /\ UNCHANGED boundOnUnloaded
                       /\ chat' = TRUE
                       /\ bound' = TRUE
                       /\ chatTimers' = (chatTimers \cup RunRows)
                       /\ fu' = Rehomed
                       /\ UNCHANGED << pendingDel, everDropped, dropBy, 
                                       dropFrom, dropDecidedLoaded, 
                                       dropCommitLoaded, deletes, 
                                       droppedBesideChat >>
            /\ runLock' = FALSE
            /\ hStage' = "idle"
            /\ UNCHANGED <<runTimers, runPick, chatPick, hLoaded>>
         \/ /\ \E f \in pendingDel:
                 /\ pendingDel' = pendingDel \ {f}
                 /\ IF fu[f] \in {"run", "chat", "running"}
                       THEN /\ dropFrom' = [dropFrom EXCEPT ![f] = fu[f]]
                            /\ dropBy' = [dropBy EXCEPT ![f] = "handoff"]
                            /\ dropDecidedLoaded' = [dropDecidedLoaded EXCEPT ![f] = FALSE]
                            /\ dropCommitLoaded' = [dropCommitLoaded EXCEPT ![f] = loaded]
                            /\ deletes' = [deletes EXCEPT ![f] = deletes[f] + 1]
                            /\ everDropped' = (everDropped \cup {f})
                            /\ fu' = [fu EXCEPT ![f] = "dropped"]
                       ELSE /\ TRUE
                            /\ UNCHANGED << fu, everDropped, dropBy, dropFrom, 
                                            dropDecidedLoaded, 
                                            dropCommitLoaded, deletes >>
            /\ UNCHANGED <<chat, bound, runTimers, chatTimers, runPick, chatPick, runLock, hStage, hLoaded, boundOnUnloaded, droppedBesideChat>>
      /\ pc' = [pc EXCEPT !["handoff"] = "ho"]
      /\ UNCHANGED << loaded, toggles, restarts, chatAgent, turn, reply, 
                      rStage, due, chatTurn, oStage, everDelivered, 
                      deliveredBy >>

Handoff == ho

ch == /\ pc["chat"] = "ch"
      /\ \/ /\ chatPick /\ chatTurn = "none"
            /\ IF \E f \in ChatRows : due[f]
                  THEN /\ \E f \in {g \in ChatRows : due[g]}:
                            /\ fu' = [fu EXCEPT ![f] = "running"]
                            /\ chatTurn' = f
                       /\ UNCHANGED chatPick
                  ELSE /\ chatPick' = FALSE
                       /\ UNCHANGED << fu, chatTurn >>
            /\ UNCHANGED <<everDelivered, everDropped, deliveredBy, dropBy, dropFrom, dropDecidedLoaded, dropCommitLoaded, deletes>>
         \/ /\ chatTurn # "none"
            /\ IF chatAgent = "other" \/ loaded
                  THEN /\ fu' = [fu EXCEPT ![chatTurn] = "delivered"]
                       /\ everDelivered' = (everDelivered \cup {chatTurn})
                       /\ deliveredBy' = [deliveredBy EXCEPT ![chatTurn] = chatAgent]
                       /\ UNCHANGED << everDropped, dropBy, dropFrom, 
                                       dropDecidedLoaded, dropCommitLoaded, 
                                       deletes >>
                  ELSE /\ IF ChatDropsUnloaded
                             THEN /\ dropFrom' = [dropFrom EXCEPT ![chatTurn] = fu[chatTurn]]
                                  /\ dropBy' = [dropBy EXCEPT ![chatTurn] = "chat"]
                                  /\ dropDecidedLoaded' = [dropDecidedLoaded EXCEPT ![chatTurn] = loaded]
                                  /\ dropCommitLoaded' = [dropCommitLoaded EXCEPT ![chatTurn] = loaded]
                                  /\ deletes' = [deletes EXCEPT ![chatTurn] = deletes[chatTurn] + 1]
                                  /\ everDropped' = (everDropped \cup {chatTurn})
                                  /\ fu' = [fu EXCEPT ![chatTurn] = "dropped"]
                             ELSE /\ fu' = [fu EXCEPT ![chatTurn] = "failed"]
                                  /\ UNCHANGED << everDropped, dropBy, 
                                                  dropFrom, dropDecidedLoaded, 
                                                  dropCommitLoaded, deletes >>
                       /\ UNCHANGED << everDelivered, deliveredBy >>
            /\ chatTurn' = "none"
            /\ chatPick' = TRUE
      /\ pc' = [pc EXCEPT !["chat"] = "ch"]
      /\ UNCHANGED << loaded, toggles, restarts, chat, chatAgent, bound, turn, 
                      reply, rStage, due, runTimers, chatTimers, runPick, 
                      runLock, hStage, hLoaded, pendingDel, oStage, 
                      boundOnUnloaded, droppedBesideChat >>

Chat == ch

oc == /\ pc["open"] = "oc"
      /\ \/ /\ oStage = "check"
            /\ IF loaded
                  THEN /\ oStage' = "create"
                  ELSE /\ oStage' = "done"
            /\ UNCHANGED <<chat, bound, fu, chatTimers>>
         \/ /\ oStage = "create"
            /\ chat' = TRUE
            /\ oStage' = "bind"
            /\ UNCHANGED <<bound, fu, chatTimers>>
         \/ /\ oStage = "bind" /\ ~runLock
            /\ bound' = TRUE
            /\ chatTimers' = (chatTimers \cup RunRows)
            /\ fu' = Rehomed
            /\ oStage' = "done"
            /\ chat' = chat
      /\ pc' = [pc EXCEPT !["open"] = "oc"]
      /\ UNCHANGED << loaded, toggles, restarts, chatAgent, turn, reply, 
                      rStage, due, runTimers, runPick, chatPick, runLock, 
                      hStage, hLoaded, pendingDel, chatTurn, everDelivered, 
                      everDropped, deliveredBy, dropBy, dropFrom, 
                      dropDecidedLoaded, dropCommitLoaded, deletes, 
                      boundOnUnloaded, droppedBesideChat >>

OpenChat == oc

Next == Admin \/ Switcher \/ Person \/ Restart \/ Clock \/ Timers \/ Run
           \/ Handoff \/ Chat \/ OpenChat

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Clock)
        /\ WF_vars(Timers)
        /\ WF_vars(Run)
        /\ WF_vars(Handoff)
        /\ WF_vars(Chat)
        /\ WF_vars(OpenChat)

\* END TRANSLATION
=============================================================================
