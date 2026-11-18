----------------------------- MODULE ThreadPR -----------------------------
EXTENDS Naturals, FiniteSets

CONSTANTS Case, Fault

DiscoveryCase == Case = "discovery"
ReaderCase == Case \in {"reader", "source-change", "aba"}
ShellCase == Case \in {"shell", "browser-loss", "delete-failure"}
RevisionLimit == IF Case = "aba" THEN 2 ELSE 1
ZeroRevision == [base |-> 0, head |-> 0, count |-> 0]
EmptyRequest == [phase |-> "idle", threadID |-> 1, origin |-> 1,
                 generation |-> 0, revision |-> ZeroRevision,
                 payload |-> ZeroRevision, aborted |-> FALSE]
Checkout(t) == <<t, "checkout">>
HistoryThread == 1
Root == "thread"
Sessions == {"thread", "subagent", "shadow", "nested"}
Pages(s) == IF s = Root THEN 2 ELSE 1
ChildLinks == {<<"thread", 1, "subagent">>, <<"thread", 2, "shadow">>,
               <<"subagent", 1, "nested">>, <<"shadow", 1, "nested">>}
Command(s, p, r, k, name) == [session |-> s, page |-> p, round |-> r, kind |-> k,
                              pr |-> <<HistoryThread, name>>]
Commands == {Command("thread", 1, 1, "create", "thread-create"),
             Command("thread", 2, 1, "listing", "listed"),
             Command("subagent", 1, 1, "failed", "failed"),
             Command("shadow", 1, 2, "mention", "shadow-mention"),
             Command("nested", 1, 2, "create", "nested-create")}
CreatedEvidence == {c.pr : c \in {d \in Commands : d.kind = "create"}}
AcceptedEvidence(r) == {c.pr : c \in {d \in Commands :
    d.kind \in {"create", "mention"} /\ d.round <= r}}

(* Each label is one await-free transition, not an entire async function.
   World actions are optional and finite. Worker actions are weakly fair.
   See README.md for the abstraction, source mapping, and failing contracts.
   Parser evidence is an INPUT abstraction, not a proof about command strings.
*)
(* --algorithm ThreadPR
variables
    alive = TRUE,
    thread = 1, source = 1, epoch = 0, moved = FALSE,
    serverRevision = ZeroRevision, revisionMoves = 0,
    historyEpoch = 0, look = 0,
    frontier = {}, visited = {}, cursor = [s \in Sessions |-> 1],
    reads = [s \in Sessions |-> 0], pending = {}, readFailed = FALSE,
    created = {}, mentioned = {}, walkSettled = FALSE,
    history = {}, everHistory = {}, listedCreated = {},
    seenCreated = {}, looked = FALSE, opened = {},
    checkoutPhase = "pending", checkoutURL = {}, checkoutFailed = FALSE,
    metadata = ReaderCase, metadataThread = 1, metadataSource = 1,
    metadataRevision = ZeroRevision,
    patch = FALSE, patchSource = 1, patchRevision = ZeroRevision,
    filesStarted = FALSE, reloadStarted = FALSE, readerCancelled = FALSE,
    loading = FALSE, staleCommit = FALSE, cancelledCommit = FALSE,
    requests = [i \in 1..2 |-> EmptyRequest],
    shellPhase = "pending", shellCancelled = FALSE,
    shellCreated = FALSE, shellKnown = FALSE, shellDeleted = FALSE,
    shellOutputReady = FALSE, shellCommitted = FALSE;

define
    LevelCommands == {c \in Commands : c.session \in frontier
        /\ c.page = cursor[c.session] /\ c.round <= look}
    NamedChildren == {link[3] : link \in {l \in ChildLinks : l[1] \in frontier
        /\ l[2] = cursor[l[1]] /\ (Fault # "skip-child" \/ l[1] = Root)}}
    Enqueued == IF Fault = "no-visited" THEN NamedChildren ELSE NamedChildren \ visited
    WatchInput == CASE Fault = "mention-opens" -> history
                    [] Fault = "own-checkout" -> listedCreated \cup checkoutURL
                    [] OTHER -> listedCreated
end define;

process World = "world"
begin
NavigateOrRefresh:
    while TRUE do
        either
            await (DiscoveryCase \/ ReaderCase) /\ ~moved;
            moved := TRUE;
            epoch := epoch + 1;
            if Case = "source-change" then
                \* The reader key includes the source. retain-source models the former URL-only key.
                source := 2;
                if Fault # "retain-source" then metadata := FALSE; patch := FALSE; end if;
            else
                \* ui.tsx ThreadPRContent key={id}: remount, not placeholder reuse.
                thread := 2; source := 2;
                history := {}; everHistory := {}; listedCreated := {}; checkoutURL := {};
                metadata := FALSE; patch := FALSE;
            end if;
            if ReaderCase /\ ~reloadStarted then
                reloadStarted := TRUE;
                readerCancelled := FALSE;
                loading := IF Case = "source-change" /\ Fault = "retain-source" THEN loading ELSE TRUE;
                requests := [requests EXCEPT ![1].aborted = TRUE,
                    ![2] = [EmptyRequest EXCEPT !.phase = "metadata",
                        !.threadID = thread, !.origin = source, !.generation = epoch]];
                \* A source-keyed remount starts with no result and loading metadata.
            else
                requests := [i \in 1..2 |-> [requests[i] EXCEPT !.aborted = TRUE]];
            end if;
        or
            await ReaderCase /\ metadata /\ ~filesStarted /\ ~readerCancelled;
            filesStarted := TRUE; loading := TRUE;
            \* load(true) aborts the controller too, including effect metadata work.
            requests := [requests EXCEPT ![2].aborted = TRUE,
                ![1] = [EmptyRequest EXCEPT !.phase = "before",
                    !.threadID = thread, !.origin = source, !.generation = epoch,
                    !.revision = metadataRevision]];
        or
            await ReaderCase /\ ~reloadStarted /\ ~loading;
            \* Manual load(false): abort prior controller and clear files BEFORE await.
            reloadStarted := TRUE; readerCancelled := FALSE;
            metadata := FALSE; patch := FALSE; loading := TRUE;
            requests := [requests EXCEPT ![1].aborted = TRUE,
                ![2] = [EmptyRequest EXCEPT !.phase = "metadata",
                    !.threadID = thread, !.origin = source, !.generation = epoch]];
        or
            await ReaderCase /\ loading /\ ~readerCancelled;
            readerCancelled := TRUE; loading := FALSE;
            requests := [i \in 1..2 |-> [requests[i] EXCEPT !.aborted = TRUE]];
        or
            await ReaderCase /\ revisionMoves < RevisionLimit;
            with coordinate \in {"base", "head", "count"} do
                serverRevision[coordinate] := 1 - serverRevision[coordinate];
            end with;
            revisionMoves := revisionMoves + 1;
        or
            await ShellCase /\ ~shellCancelled /\ shellPhase # "done";
            shellCancelled := TRUE;
        or
            await Case = "browser-loss" /\ alive /\ shellKnown /\ ~shellDeleted;
            \* Process death is NOT a React cleanup and cannot run finally.
            alive := FALSE;
        or
            skip;
        end either;
    end while;
end process;

fair process Discovery = "discovery-worker"
begin
DiscoveryStart:
    await DiscoveryCase;
Looks:
    while look < 2 do
        look := look + 1;
        frontier := {Root}; visited := {Root};
        cursor := [s \in Sessions |-> 1]; reads := [s \in Sessions |-> 0];
        created := {}; mentioned := {}; readFailed := FALSE; walkSettled := FALSE;
ReadLevel:
        while frontier # {} do
            either
                pending := LevelCommands
                || reads := [s \in Sessions |-> IF s \in frontier THEN reads[s] + 1 ELSE reads[s]]
                || cursor := [s \in Sessions |-> IF s \in Enqueued THEN 1
                       ELSE IF s \in frontier THEN cursor[s] + 1 ELSE cursor[s]]
                || visited := visited \cup Enqueued
                || frontier := {s \in frontier : cursor[s] < Pages(s)} \cup Enqueued;
            or
                readFailed := TRUE || frontier := {};
            end either;
Classify:
            while pending # {} do
                with c \in pending do
                    if c.kind = "create" then
                        created := created \cup {c.pr};
                    elsif c.kind = "mention" \/ Fault = "listing-listed" then
                        mentioned := mentioned \cup {c.pr};
                    end if;
                    pending := pending \ {c};
                end with;
            end while;
        end while;
Commit:
        if ~readFailed /\ historyEpoch = epoch then
            history := created \cup mentioned;
            everHistory := created \cup mentioned;
            listedCreated := created;
            walkSettled := TRUE;
        end if;
Watch:
        if ~readFailed /\ historyEpoch = epoch then
            if looked /\ WatchInput \ seenCreated # {} then
                with url \in WatchInput \ seenCreated do
                    opened := opened \cup {url};
                end with;
            end if;
            seenCreated := seenCreated \cup WatchInput;
            looked := TRUE;
        end if;
    end while;
end process;

fair process CheckoutWorker = "checkout-worker"
begin
CheckoutLookup:
    while TRUE do
        await DiscoveryCase;
        either
            await checkoutPhase = "pending";
            \* Resolving session.location and gh view are independent of history.
            either
                checkoutPhase := "success";
            or
                checkoutPhase := "failure";
            or
                checkoutPhase := "none";
            end either;
        or
            await checkoutPhase \in {"success", "failure", "none"};
            if historyEpoch = epoch then
                if checkoutPhase = "success" then
                    checkoutURL := {Checkout(HistoryThread)};
                elsif checkoutPhase = "failure" then
                    checkoutFailed := TRUE;
                    if Fault = "drop-history" then history := {}; end if;
                end if;
            end if;
            checkoutPhase := "done";
        end either;
    end while;
end process;

fair process Reader = "reader-worker"
begin
RequestCompletion:
    while TRUE do
        await ReaderCase /\ alive;
        with i \in 1..2 do
            await requests[i].phase \notin {"idle", "done"};
            if requests[i].phase = "metadata" then
                \* Metadata fetch+JSON completion; JS .then checks its own signal.
                if ~requests[i].aborted \/ Fault = "ignore-abort" then
                    staleCommit := staleCommit \/ requests[i].generation # epoch
                        \/ requests[i].threadID # thread \/ requests[i].origin # source;
                    cancelledCommit := cancelledCommit \/ requests[i].aborted;
                    metadata := TRUE; patch := FALSE;
                    metadataThread := requests[i].threadID;
                    metadataSource := requests[i].origin;
                    metadataRevision := serverRevision;
                    loading := FALSE;
                end if;
                requests[i].phase := "done";
            elsif requests[i].phase = "before" then
                \* First await checkRevision(); failure ends without a patch commit.
                if serverRevision # requests[i].revision then
                    requests[i].phase := "done";
                    if ~requests[i].aborted then loading := FALSE; end if;
                else
                    requests[i].phase := "files";
                end if;
            elsif requests[i].phase = "files" then
                \* GitHub serves a page at this revision, not an immutable-SHA URL.
                requests[i].payload := serverRevision || requests[i].phase := "after";
            elsif requests[i].phase = "after" then
                \* Second await checkRevision(), separately interleaved from files.
                if Fault # "skip-post" /\ serverRevision # requests[i].revision then
                    requests[i].phase := "done";
                    if ~requests[i].aborted then loading := FALSE; end if;
                else
                    requests[i].phase := "commit";
                end if;
            else
                \* load(true)'s .then/continuation, separate from network completion.
                if ~requests[i].aborted \/ Fault = "ignore-abort" then
                    staleCommit := staleCommit \/ requests[i].generation # epoch
                        \/ requests[i].threadID # thread \/ requests[i].origin # source;
                    cancelledCommit := cancelledCommit \/ requests[i].aborted;
                    patch := TRUE; patchSource := requests[i].origin;
                    patchRevision := requests[i].payload; loading := FALSE;
                end if;
                requests[i].phase := "done";
            end if;
        end with;
    end while;
end process;

fair process Shell = "shell-worker"
begin
ShellAwaitBoundary:
    while TRUE do
        await ShellCase /\ alive /\ shellPhase # "done";
        if shellPhase = "pending" then
            \* run's initial throwIfAborted precedes POST dispatch.
            shellPhase := IF shellCancelled THEN "done" ELSE "sent";
        elsif shellPhase = "sent" then
            \* Remote server allocation is NOT atomic with browser response delivery.
            shellCreated := TRUE; shellPhase := "allocated";
        elsif shellPhase = "allocated" then
            if Fault = "abort-create" /\ shellCancelled then
                shellPhase := "done";
            else
                \* POST has no signal, so late cancellation cannot lose the ID.
                shellKnown := TRUE; shellPhase := "known";
            end if;
        elsif shellPhase = "known" then
            shellPhase := IF shellCancelled THEN "finally" ELSE "poll";
        elsif shellPhase = "poll" then
            \* Finite terminal poll or request error; no guarantee for a hung server.
            if shellCancelled then shellPhase := "finally";
            else either shellPhase := "output1"; or shellPhase := "finally"; end either;
            end if;
        elsif shellPhase \in {"output1", "output2"} then
            if shellCancelled then shellPhase := "finally";
            elsif shellPhase = "output1" then shellPhase := "output2";
            else shellOutputReady := TRUE; shellPhase := "finally";
            end if;
        elsif shellPhase = "finally" then
            shellPhase := IF Fault = "skip-delete" THEN "done" ELSE "deleting";
        else
            \* DELETE has no signal; successful response removes the server shell.
            shellDeleted := Case # "delete-failure";
            \* Publication means the reader's guarded continuation, not run's return.
            shellCommitted := shellOutputReady /\ ~shellCancelled /\ shellDeleted;
            shellPhase := "done";
        end if;
    end while;
end process;
end algorithm; *)

\* BEGIN TRANSLATION
VARIABLES pc, alive, thread, source, epoch, moved, serverRevision, 
          revisionMoves, historyEpoch, look, frontier, visited, cursor, reads, 
          pending, readFailed, created, mentioned, walkSettled, history, 
          everHistory, listedCreated, seenCreated, looked, opened, 
          checkoutPhase, checkoutURL, checkoutFailed, metadata, 
          metadataThread, metadataSource, metadataRevision, patch, 
          patchSource, patchRevision, filesStarted, reloadStarted, 
          readerCancelled, loading, staleCommit, cancelledCommit, requests, 
          shellPhase, shellCancelled, shellCreated, shellKnown, shellDeleted, 
          shellOutputReady, shellCommitted

(* define statement *)
LevelCommands == {c \in Commands : c.session \in frontier
    /\ c.page = cursor[c.session] /\ c.round <= look}
NamedChildren == {link[3] : link \in {l \in ChildLinks : l[1] \in frontier
    /\ l[2] = cursor[l[1]] /\ (Fault # "skip-child" \/ l[1] = Root)}}
Enqueued == IF Fault = "no-visited" THEN NamedChildren ELSE NamedChildren \ visited
WatchInput == CASE Fault = "mention-opens" -> history
                [] Fault = "own-checkout" -> listedCreated \cup checkoutURL
                [] OTHER -> listedCreated


vars == << pc, alive, thread, source, epoch, moved, serverRevision, 
           revisionMoves, historyEpoch, look, frontier, visited, cursor, 
           reads, pending, readFailed, created, mentioned, walkSettled, 
           history, everHistory, listedCreated, seenCreated, looked, opened, 
           checkoutPhase, checkoutURL, checkoutFailed, metadata, 
           metadataThread, metadataSource, metadataRevision, patch, 
           patchSource, patchRevision, filesStarted, reloadStarted, 
           readerCancelled, loading, staleCommit, cancelledCommit, requests, 
           shellPhase, shellCancelled, shellCreated, shellKnown, shellDeleted, 
           shellOutputReady, shellCommitted >>

ProcSet == {"world"} \cup {"discovery-worker"} \cup {"checkout-worker"} \cup {"reader-worker"} \cup {"shell-worker"}

Init == (* Global variables *)
        /\ alive = TRUE
        /\ thread = 1
        /\ source = 1
        /\ epoch = 0
        /\ moved = FALSE
        /\ serverRevision = ZeroRevision
        /\ revisionMoves = 0
        /\ historyEpoch = 0
        /\ look = 0
        /\ frontier = {}
        /\ visited = {}
        /\ cursor = [s \in Sessions |-> 1]
        /\ reads = [s \in Sessions |-> 0]
        /\ pending = {}
        /\ readFailed = FALSE
        /\ created = {}
        /\ mentioned = {}
        /\ walkSettled = FALSE
        /\ history = {}
        /\ everHistory = {}
        /\ listedCreated = {}
        /\ seenCreated = {}
        /\ looked = FALSE
        /\ opened = {}
        /\ checkoutPhase = "pending"
        /\ checkoutURL = {}
        /\ checkoutFailed = FALSE
        /\ metadata = ReaderCase
        /\ metadataThread = 1
        /\ metadataSource = 1
        /\ metadataRevision = ZeroRevision
        /\ patch = FALSE
        /\ patchSource = 1
        /\ patchRevision = ZeroRevision
        /\ filesStarted = FALSE
        /\ reloadStarted = FALSE
        /\ readerCancelled = FALSE
        /\ loading = FALSE
        /\ staleCommit = FALSE
        /\ cancelledCommit = FALSE
        /\ requests = [i \in 1..2 |-> EmptyRequest]
        /\ shellPhase = "pending"
        /\ shellCancelled = FALSE
        /\ shellCreated = FALSE
        /\ shellKnown = FALSE
        /\ shellDeleted = FALSE
        /\ shellOutputReady = FALSE
        /\ shellCommitted = FALSE
        /\ pc = [self \in ProcSet |-> CASE self = "world" -> "NavigateOrRefresh"
                                        [] self = "discovery-worker" -> "DiscoveryStart"
                                        [] self = "checkout-worker" -> "CheckoutLookup"
                                        [] self = "reader-worker" -> "RequestCompletion"
                                        [] self = "shell-worker" -> "ShellAwaitBoundary"]

NavigateOrRefresh == /\ pc["world"] = "NavigateOrRefresh"
                     /\ \/ /\ (DiscoveryCase \/ ReaderCase) /\ ~moved
                           /\ moved' = TRUE
                           /\ epoch' = epoch + 1
                           /\ IF Case = "source-change"
                                 THEN /\ source' = 2
                                      /\ IF Fault # "retain-source"
                                            THEN /\ metadata' = FALSE
                                                 /\ patch' = FALSE
                                            ELSE /\ TRUE
                                                 /\ UNCHANGED << metadata, 
                                                                 patch >>
                                      /\ UNCHANGED << thread, history, 
                                                      everHistory, 
                                                      listedCreated, 
                                                      checkoutURL >>
                                 ELSE /\ thread' = 2
                                      /\ source' = 2
                                      /\ history' = {}
                                      /\ everHistory' = {}
                                      /\ listedCreated' = {}
                                      /\ checkoutURL' = {}
                                      /\ metadata' = FALSE
                                      /\ patch' = FALSE
                           /\ IF ReaderCase /\ ~reloadStarted
                                 THEN /\ reloadStarted' = TRUE
                                      /\ readerCancelled' = FALSE
                                      /\ loading' = (IF Case = "source-change" /\ Fault = "retain-source" THEN loading ELSE TRUE)
                                      /\ requests' =         [requests EXCEPT ![1].aborted = TRUE,
                                                     ![2] = [EmptyRequest EXCEPT !.phase = "metadata",
                                                         !.threadID = thread', !.origin = source', !.generation = epoch']]
                                 ELSE /\ requests' = [i \in 1..2 |-> [requests[i] EXCEPT !.aborted = TRUE]]
                                      /\ UNCHANGED << reloadStarted, 
                                                      readerCancelled, loading >>
                           /\ UNCHANGED <<alive, serverRevision, revisionMoves, filesStarted, shellCancelled>>
                        \/ /\ ReaderCase /\ metadata /\ ~filesStarted /\ ~readerCancelled
                           /\ filesStarted' = TRUE
                           /\ loading' = TRUE
                           /\ requests' =         [requests EXCEPT ![2].aborted = TRUE,
                                          ![1] = [EmptyRequest EXCEPT !.phase = "before",
                                              !.threadID = thread, !.origin = source, !.generation = epoch,
                                              !.revision = metadataRevision]]
                           /\ UNCHANGED <<alive, thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, listedCreated, checkoutURL, metadata, patch, reloadStarted, readerCancelled, shellCancelled>>
                        \/ /\ ReaderCase /\ ~reloadStarted /\ ~loading
                           /\ reloadStarted' = TRUE
                           /\ readerCancelled' = FALSE
                           /\ metadata' = FALSE
                           /\ patch' = FALSE
                           /\ loading' = TRUE
                           /\ requests' =         [requests EXCEPT ![1].aborted = TRUE,
                                          ![2] = [EmptyRequest EXCEPT !.phase = "metadata",
                                              !.threadID = thread, !.origin = source, !.generation = epoch]]
                           /\ UNCHANGED <<alive, thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, listedCreated, checkoutURL, filesStarted, shellCancelled>>
                        \/ /\ ReaderCase /\ loading /\ ~readerCancelled
                           /\ readerCancelled' = TRUE
                           /\ loading' = FALSE
                           /\ requests' = [i \in 1..2 |-> [requests[i] EXCEPT !.aborted = TRUE]]
                           /\ UNCHANGED <<alive, thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, listedCreated, checkoutURL, metadata, patch, filesStarted, reloadStarted, shellCancelled>>
                        \/ /\ ReaderCase /\ revisionMoves < RevisionLimit
                           /\ \E coordinate \in {"base", "head", "count"}:
                                serverRevision' = [serverRevision EXCEPT ![coordinate] = 1 - serverRevision[coordinate]]
                           /\ revisionMoves' = revisionMoves + 1
                           /\ UNCHANGED <<alive, thread, source, epoch, moved, history, everHistory, listedCreated, checkoutURL, metadata, patch, filesStarted, reloadStarted, readerCancelled, loading, requests, shellCancelled>>
                        \/ /\ ShellCase /\ ~shellCancelled /\ shellPhase # "done"
                           /\ shellCancelled' = TRUE
                           /\ UNCHANGED <<alive, thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, listedCreated, checkoutURL, metadata, patch, filesStarted, reloadStarted, readerCancelled, loading, requests>>
                        \/ /\ Case = "browser-loss" /\ alive /\ shellKnown /\ ~shellDeleted
                           /\ alive' = FALSE
                           /\ UNCHANGED <<thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, listedCreated, checkoutURL, metadata, patch, filesStarted, reloadStarted, readerCancelled, loading, requests, shellCancelled>>
                        \/ /\ TRUE
                           /\ UNCHANGED <<alive, thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, listedCreated, checkoutURL, metadata, patch, filesStarted, reloadStarted, readerCancelled, loading, requests, shellCancelled>>
                     /\ pc' = [pc EXCEPT !["world"] = "NavigateOrRefresh"]
                     /\ UNCHANGED << historyEpoch, look, frontier, visited, 
                                     cursor, reads, pending, readFailed, 
                                     created, mentioned, walkSettled, 
                                     seenCreated, looked, opened, 
                                     checkoutPhase, checkoutFailed, 
                                     metadataThread, metadataSource, 
                                     metadataRevision, patchSource, 
                                     patchRevision, staleCommit, 
                                     cancelledCommit, shellPhase, shellCreated, 
                                     shellKnown, shellDeleted, 
                                     shellOutputReady, shellCommitted >>

World == NavigateOrRefresh

DiscoveryStart == /\ pc["discovery-worker"] = "DiscoveryStart"
                  /\ DiscoveryCase
                  /\ pc' = [pc EXCEPT !["discovery-worker"] = "Looks"]
                  /\ UNCHANGED << alive, thread, source, epoch, moved, 
                                  serverRevision, revisionMoves, historyEpoch, 
                                  look, frontier, visited, cursor, reads, 
                                  pending, readFailed, created, mentioned, 
                                  walkSettled, history, everHistory, 
                                  listedCreated, seenCreated, looked, opened, 
                                  checkoutPhase, checkoutURL, checkoutFailed, 
                                  metadata, metadataThread, metadataSource, 
                                  metadataRevision, patch, patchSource, 
                                  patchRevision, filesStarted, reloadStarted, 
                                  readerCancelled, loading, staleCommit, 
                                  cancelledCommit, requests, shellPhase, 
                                  shellCancelled, shellCreated, shellKnown, 
                                  shellDeleted, shellOutputReady, 
                                  shellCommitted >>

Looks == /\ pc["discovery-worker"] = "Looks"
         /\ IF look < 2
               THEN /\ look' = look + 1
                    /\ frontier' = {Root}
                    /\ visited' = {Root}
                    /\ cursor' = [s \in Sessions |-> 1]
                    /\ reads' = [s \in Sessions |-> 0]
                    /\ created' = {}
                    /\ mentioned' = {}
                    /\ readFailed' = FALSE
                    /\ walkSettled' = FALSE
                    /\ pc' = [pc EXCEPT !["discovery-worker"] = "ReadLevel"]
               ELSE /\ pc' = [pc EXCEPT !["discovery-worker"] = "Done"]
                    /\ UNCHANGED << look, frontier, visited, cursor, reads, 
                                    readFailed, created, mentioned, 
                                    walkSettled >>
         /\ UNCHANGED << alive, thread, source, epoch, moved, serverRevision, 
                         revisionMoves, historyEpoch, pending, history, 
                         everHistory, listedCreated, seenCreated, looked, 
                         opened, checkoutPhase, checkoutURL, checkoutFailed, 
                         metadata, metadataThread, metadataSource, 
                         metadataRevision, patch, patchSource, patchRevision, 
                         filesStarted, reloadStarted, readerCancelled, loading, 
                         staleCommit, cancelledCommit, requests, shellPhase, 
                         shellCancelled, shellCreated, shellKnown, 
                         shellDeleted, shellOutputReady, shellCommitted >>

ReadLevel == /\ pc["discovery-worker"] = "ReadLevel"
             /\ IF frontier # {}
                   THEN /\ \/ /\ /\ cursor' =       [s \in Sessions |-> IF s \in Enqueued THEN 1
                                              ELSE IF s \in frontier THEN cursor[s] + 1 ELSE cursor[s]]
                                 /\ frontier' = ({s \in frontier : cursor[s] < Pages(s)} \cup Enqueued)
                                 /\ pending' = LevelCommands
                                 /\ reads' = [s \in Sessions |-> IF s \in frontier THEN reads[s] + 1 ELSE reads[s]]
                                 /\ visited' = (visited \cup Enqueued)
                              /\ UNCHANGED readFailed
                           \/ /\ /\ frontier' = {}
                                 /\ readFailed' = TRUE
                              /\ UNCHANGED <<visited, cursor, reads, pending>>
                        /\ pc' = [pc EXCEPT !["discovery-worker"] = "Classify"]
                   ELSE /\ pc' = [pc EXCEPT !["discovery-worker"] = "Commit"]
                        /\ UNCHANGED << frontier, visited, cursor, reads, 
                                        pending, readFailed >>
             /\ UNCHANGED << alive, thread, source, epoch, moved, 
                             serverRevision, revisionMoves, historyEpoch, look, 
                             created, mentioned, walkSettled, history, 
                             everHistory, listedCreated, seenCreated, looked, 
                             opened, checkoutPhase, checkoutURL, 
                             checkoutFailed, metadata, metadataThread, 
                             metadataSource, metadataRevision, patch, 
                             patchSource, patchRevision, filesStarted, 
                             reloadStarted, readerCancelled, loading, 
                             staleCommit, cancelledCommit, requests, 
                             shellPhase, shellCancelled, shellCreated, 
                             shellKnown, shellDeleted, shellOutputReady, 
                             shellCommitted >>

Classify == /\ pc["discovery-worker"] = "Classify"
            /\ IF pending # {}
                  THEN /\ \E c \in pending:
                            /\ IF c.kind = "create"
                                  THEN /\ created' = (created \cup {c.pr})
                                       /\ UNCHANGED mentioned
                                  ELSE /\ IF c.kind = "mention" \/ Fault = "listing-listed"
                                             THEN /\ mentioned' = (mentioned \cup {c.pr})
                                             ELSE /\ TRUE
                                                  /\ UNCHANGED mentioned
                                       /\ UNCHANGED created
                            /\ pending' = pending \ {c}
                       /\ pc' = [pc EXCEPT !["discovery-worker"] = "Classify"]
                  ELSE /\ pc' = [pc EXCEPT !["discovery-worker"] = "ReadLevel"]
                       /\ UNCHANGED << pending, created, mentioned >>
            /\ UNCHANGED << alive, thread, source, epoch, moved, 
                            serverRevision, revisionMoves, historyEpoch, look, 
                            frontier, visited, cursor, reads, readFailed, 
                            walkSettled, history, everHistory, listedCreated, 
                            seenCreated, looked, opened, checkoutPhase, 
                            checkoutURL, checkoutFailed, metadata, 
                            metadataThread, metadataSource, metadataRevision, 
                            patch, patchSource, patchRevision, filesStarted, 
                            reloadStarted, readerCancelled, loading, 
                            staleCommit, cancelledCommit, requests, shellPhase, 
                            shellCancelled, shellCreated, shellKnown, 
                            shellDeleted, shellOutputReady, shellCommitted >>

Commit == /\ pc["discovery-worker"] = "Commit"
          /\ IF ~readFailed /\ historyEpoch = epoch
                THEN /\ history' = (created \cup mentioned)
                     /\ everHistory' = (created \cup mentioned)
                     /\ listedCreated' = created
                     /\ walkSettled' = TRUE
                ELSE /\ TRUE
                     /\ UNCHANGED << walkSettled, history, everHistory, 
                                     listedCreated >>
          /\ pc' = [pc EXCEPT !["discovery-worker"] = "Watch"]
          /\ UNCHANGED << alive, thread, source, epoch, moved, serverRevision, 
                          revisionMoves, historyEpoch, look, frontier, visited, 
                          cursor, reads, pending, readFailed, created, 
                          mentioned, seenCreated, looked, opened, 
                          checkoutPhase, checkoutURL, checkoutFailed, metadata, 
                          metadataThread, metadataSource, metadataRevision, 
                          patch, patchSource, patchRevision, filesStarted, 
                          reloadStarted, readerCancelled, loading, staleCommit, 
                          cancelledCommit, requests, shellPhase, 
                          shellCancelled, shellCreated, shellKnown, 
                          shellDeleted, shellOutputReady, shellCommitted >>

Watch == /\ pc["discovery-worker"] = "Watch"
         /\ IF ~readFailed /\ historyEpoch = epoch
               THEN /\ IF looked /\ WatchInput \ seenCreated # {}
                          THEN /\ \E url \in WatchInput \ seenCreated:
                                    opened' = (opened \cup {url})
                          ELSE /\ TRUE
                               /\ UNCHANGED opened
                    /\ seenCreated' = (seenCreated \cup WatchInput)
                    /\ looked' = TRUE
               ELSE /\ TRUE
                    /\ UNCHANGED << seenCreated, looked, opened >>
         /\ pc' = [pc EXCEPT !["discovery-worker"] = "Looks"]
         /\ UNCHANGED << alive, thread, source, epoch, moved, serverRevision, 
                         revisionMoves, historyEpoch, look, frontier, visited, 
                         cursor, reads, pending, readFailed, created, 
                         mentioned, walkSettled, history, everHistory, 
                         listedCreated, checkoutPhase, checkoutURL, 
                         checkoutFailed, metadata, metadataThread, 
                         metadataSource, metadataRevision, patch, patchSource, 
                         patchRevision, filesStarted, reloadStarted, 
                         readerCancelled, loading, staleCommit, 
                         cancelledCommit, requests, shellPhase, shellCancelled, 
                         shellCreated, shellKnown, shellDeleted, 
                         shellOutputReady, shellCommitted >>

Discovery == DiscoveryStart \/ Looks \/ ReadLevel \/ Classify \/ Commit
                \/ Watch

CheckoutLookup == /\ pc["checkout-worker"] = "CheckoutLookup"
                  /\ DiscoveryCase
                  /\ \/ /\ checkoutPhase = "pending"
                        /\ \/ /\ checkoutPhase' = "success"
                           \/ /\ checkoutPhase' = "failure"
                           \/ /\ checkoutPhase' = "none"
                        /\ UNCHANGED <<history, checkoutURL, checkoutFailed>>
                     \/ /\ checkoutPhase \in {"success", "failure", "none"}
                        /\ IF historyEpoch = epoch
                              THEN /\ IF checkoutPhase = "success"
                                         THEN /\ checkoutURL' = {Checkout(HistoryThread)}
                                              /\ UNCHANGED << history, 
                                                              checkoutFailed >>
                                         ELSE /\ IF checkoutPhase = "failure"
                                                    THEN /\ checkoutFailed' = TRUE
                                                         /\ IF Fault = "drop-history"
                                                               THEN /\ history' = {}
                                                               ELSE /\ TRUE
                                                                    /\ UNCHANGED history
                                                    ELSE /\ TRUE
                                                         /\ UNCHANGED << history, 
                                                                         checkoutFailed >>
                                              /\ UNCHANGED checkoutURL
                              ELSE /\ TRUE
                                   /\ UNCHANGED << history, checkoutURL, 
                                                   checkoutFailed >>
                        /\ checkoutPhase' = "done"
                  /\ pc' = [pc EXCEPT !["checkout-worker"] = "CheckoutLookup"]
                  /\ UNCHANGED << alive, thread, source, epoch, moved, 
                                  serverRevision, revisionMoves, historyEpoch, 
                                  look, frontier, visited, cursor, reads, 
                                  pending, readFailed, created, mentioned, 
                                  walkSettled, everHistory, listedCreated, 
                                  seenCreated, looked, opened, metadata, 
                                  metadataThread, metadataSource, 
                                  metadataRevision, patch, patchSource, 
                                  patchRevision, filesStarted, reloadStarted, 
                                  readerCancelled, loading, staleCommit, 
                                  cancelledCommit, requests, shellPhase, 
                                  shellCancelled, shellCreated, shellKnown, 
                                  shellDeleted, shellOutputReady, 
                                  shellCommitted >>

CheckoutWorker == CheckoutLookup

RequestCompletion == /\ pc["reader-worker"] = "RequestCompletion"
                     /\ ReaderCase /\ alive
                     /\ \E i \in 1..2:
                          /\ requests[i].phase \notin {"idle", "done"}
                          /\ IF requests[i].phase = "metadata"
                                THEN /\ IF ~requests[i].aborted \/ Fault = "ignore-abort"
                                           THEN /\ staleCommit' = (           staleCommit \/ requests[i].generation # epoch
                                                                   \/ requests[i].threadID # thread \/ requests[i].origin # source)
                                                /\ cancelledCommit' = (cancelledCommit \/ requests[i].aborted)
                                                /\ metadata' = TRUE
                                                /\ patch' = FALSE
                                                /\ metadataThread' = requests[i].threadID
                                                /\ metadataSource' = requests[i].origin
                                                /\ metadataRevision' = serverRevision
                                                /\ loading' = FALSE
                                           ELSE /\ TRUE
                                                /\ UNCHANGED << metadata, 
                                                                metadataThread, 
                                                                metadataSource, 
                                                                metadataRevision, 
                                                                patch, loading, 
                                                                staleCommit, 
                                                                cancelledCommit >>
                                     /\ requests' = [requests EXCEPT ![i].phase = "done"]
                                     /\ UNCHANGED << patchSource, 
                                                     patchRevision >>
                                ELSE /\ IF requests[i].phase = "before"
                                           THEN /\ IF serverRevision # requests[i].revision
                                                      THEN /\ requests' = [requests EXCEPT ![i].phase = "done"]
                                                           /\ IF ~requests'[i].aborted
                                                                 THEN /\ loading' = FALSE
                                                                 ELSE /\ TRUE
                                                                      /\ UNCHANGED loading
                                                      ELSE /\ requests' = [requests EXCEPT ![i].phase = "files"]
                                                           /\ UNCHANGED loading
                                                /\ UNCHANGED << patch, 
                                                                patchSource, 
                                                                patchRevision, 
                                                                staleCommit, 
                                                                cancelledCommit >>
                                           ELSE /\ IF requests[i].phase = "files"
                                                      THEN /\ requests' = [requests EXCEPT ![i].payload = serverRevision,
                                                                                           ![i].phase = "after"]
                                                           /\ UNCHANGED << patch, 
                                                                           patchSource, 
                                                                           patchRevision, 
                                                                           loading, 
                                                                           staleCommit, 
                                                                           cancelledCommit >>
                                                      ELSE /\ IF requests[i].phase = "after"
                                                                 THEN /\ IF Fault # "skip-post" /\ serverRevision # requests[i].revision
                                                                            THEN /\ requests' = [requests EXCEPT ![i].phase = "done"]
                                                                                 /\ IF ~requests'[i].aborted
                                                                                       THEN /\ loading' = FALSE
                                                                                       ELSE /\ TRUE
                                                                                            /\ UNCHANGED loading
                                                                            ELSE /\ requests' = [requests EXCEPT ![i].phase = "commit"]
                                                                                 /\ UNCHANGED loading
                                                                      /\ UNCHANGED << patch, 
                                                                                      patchSource, 
                                                                                      patchRevision, 
                                                                                      staleCommit, 
                                                                                      cancelledCommit >>
                                                                 ELSE /\ IF ~requests[i].aborted \/ Fault = "ignore-abort"
                                                                            THEN /\ staleCommit' = (           staleCommit \/ requests[i].generation # epoch
                                                                                                    \/ requests[i].threadID # thread \/ requests[i].origin # source)
                                                                                 /\ cancelledCommit' = (cancelledCommit \/ requests[i].aborted)
                                                                                 /\ patch' = TRUE
                                                                                 /\ patchSource' = requests[i].origin
                                                                                 /\ patchRevision' = requests[i].payload
                                                                                 /\ loading' = FALSE
                                                                            ELSE /\ TRUE
                                                                                 /\ UNCHANGED << patch, 
                                                                                                 patchSource, 
                                                                                                 patchRevision, 
                                                                                                 loading, 
                                                                                                 staleCommit, 
                                                                                                 cancelledCommit >>
                                                                      /\ requests' = [requests EXCEPT ![i].phase = "done"]
                                     /\ UNCHANGED << metadata, metadataThread, 
                                                     metadataSource, 
                                                     metadataRevision >>
                     /\ pc' = [pc EXCEPT !["reader-worker"] = "RequestCompletion"]
                     /\ UNCHANGED << alive, thread, source, epoch, moved, 
                                     serverRevision, revisionMoves, 
                                     historyEpoch, look, frontier, visited, 
                                     cursor, reads, pending, readFailed, 
                                     created, mentioned, walkSettled, history, 
                                     everHistory, listedCreated, seenCreated, 
                                     looked, opened, checkoutPhase, 
                                     checkoutURL, checkoutFailed, filesStarted, 
                                     reloadStarted, readerCancelled, 
                                     shellPhase, shellCancelled, shellCreated, 
                                     shellKnown, shellDeleted, 
                                     shellOutputReady, shellCommitted >>

Reader == RequestCompletion

ShellAwaitBoundary == /\ pc["shell-worker"] = "ShellAwaitBoundary"
                      /\ ShellCase /\ alive /\ shellPhase # "done"
                      /\ IF shellPhase = "pending"
                            THEN /\ shellPhase' = IF shellCancelled THEN "done" ELSE "sent"
                                 /\ UNCHANGED << shellCreated, shellKnown, 
                                                 shellDeleted, 
                                                 shellOutputReady, 
                                                 shellCommitted >>
                            ELSE /\ IF shellPhase = "sent"
                                       THEN /\ shellCreated' = TRUE
                                            /\ shellPhase' = "allocated"
                                            /\ UNCHANGED << shellKnown, 
                                                            shellDeleted, 
                                                            shellOutputReady, 
                                                            shellCommitted >>
                                       ELSE /\ IF shellPhase = "allocated"
                                                  THEN /\ IF Fault = "abort-create" /\ shellCancelled
                                                             THEN /\ shellPhase' = "done"
                                                                  /\ UNCHANGED shellKnown
                                                             ELSE /\ shellKnown' = TRUE
                                                                  /\ shellPhase' = "known"
                                                       /\ UNCHANGED << shellDeleted, 
                                                                       shellOutputReady, 
                                                                       shellCommitted >>
                                                  ELSE /\ IF shellPhase = "known"
                                                             THEN /\ shellPhase' = IF shellCancelled THEN "finally" ELSE "poll"
                                                                  /\ UNCHANGED << shellDeleted, 
                                                                                  shellOutputReady, 
                                                                                  shellCommitted >>
                                                             ELSE /\ IF shellPhase = "poll"
                                                                        THEN /\ IF shellCancelled
                                                                                   THEN /\ shellPhase' = "finally"
                                                                                   ELSE /\ \/ /\ shellPhase' = "output1"
                                                                                           \/ /\ shellPhase' = "finally"
                                                                             /\ UNCHANGED << shellDeleted, 
                                                                                             shellOutputReady, 
                                                                                             shellCommitted >>
                                                                        ELSE /\ IF shellPhase \in {"output1", "output2"}
                                                                                   THEN /\ IF shellCancelled
                                                                                              THEN /\ shellPhase' = "finally"
                                                                                                   /\ UNCHANGED shellOutputReady
                                                                                              ELSE /\ IF shellPhase = "output1"
                                                                                                         THEN /\ shellPhase' = "output2"
                                                                                                              /\ UNCHANGED shellOutputReady
                                                                                                         ELSE /\ shellOutputReady' = TRUE
                                                                                                              /\ shellPhase' = "finally"
                                                                                        /\ UNCHANGED << shellDeleted, 
                                                                                                        shellCommitted >>
                                                                                   ELSE /\ IF shellPhase = "finally"
                                                                                              THEN /\ shellPhase' = (IF Fault = "skip-delete" THEN "done" ELSE "deleting")
                                                                                                   /\ UNCHANGED << shellDeleted, 
                                                                                                                   shellCommitted >>
                                                                                              ELSE /\ shellDeleted' = (Case # "delete-failure")
                                                                                                   /\ shellCommitted' = (shellOutputReady /\ ~shellCancelled /\ shellDeleted')
                                                                                                   /\ shellPhase' = "done"
                                                                                        /\ UNCHANGED shellOutputReady
                                                       /\ UNCHANGED shellKnown
                                            /\ UNCHANGED shellCreated
                      /\ pc' = [pc EXCEPT !["shell-worker"] = "ShellAwaitBoundary"]
                      /\ UNCHANGED << alive, thread, source, epoch, moved, 
                                      serverRevision, revisionMoves, 
                                      historyEpoch, look, frontier, visited, 
                                      cursor, reads, pending, readFailed, 
                                      created, mentioned, walkSettled, history, 
                                      everHistory, listedCreated, seenCreated, 
                                      looked, opened, checkoutPhase, 
                                      checkoutURL, checkoutFailed, metadata, 
                                      metadataThread, metadataSource, 
                                      metadataRevision, patch, patchSource, 
                                      patchRevision, filesStarted, 
                                      reloadStarted, readerCancelled, loading, 
                                      staleCommit, cancelledCommit, requests, 
                                      shellCancelled >>

Shell == ShellAwaitBoundary

Next == World \/ Discovery \/ CheckoutWorker \/ Reader \/ Shell

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Discovery)
        /\ WF_vars(CheckoutWorker)
        /\ WF_vars(Reader)
        /\ WF_vars(Shell)

\* END TRANSLATION

VisiblePRs == history \cup checkoutURL
OpenedOnlyCreated == opened \subseteq CreatedEvidence
ListedOnlyAccepted == history \subseteq AcceptedEvidence(2)
ChildEvidenceListed == walkSettled /\ historyEpoch = epoch =>
    AcceptedEvidence(look) \subseteq history
EachSessionReadOnce == \A s \in Sessions : reads[s] <= Pages(s)
NeverOpened == opened = {}
NoWrongThreadDiscovery == (\A url \in history : url[1] = thread)
    /\ checkoutURL \subseteq {Checkout(thread)}
HistorySurvivesCheckoutFailure == everHistory \subseteq VisiblePRs
NoStaleCommit == ~staleCommit
NoCancelledCommit == ~cancelledCommit
DisplayedContextMatches == metadata =>
    metadataThread = thread /\ metadataSource = source
PatchMatchesMetadata == patch => metadata /\ patchSource = metadataSource
    /\ patchRevision = metadataRevision
CancellationStopsLoading == readerCancelled => ~loading
KnownShellEventuallyDeleted == shellKnown ~> shellDeleted
CreatedShellEventuallyDeleted == shellCreated ~> shellDeleted
ShellCancellationSettles == shellCancelled ~> (shellPhase = "done")
NoCancelledShellCommit == shellCancelled => ~shellCommitted
ReaderSettles == (\E i \in 1..2 : requests[i].phase \notin {"idle", "done"})
    ~> (\A i \in 1..2 : requests[i].phase \in {"idle", "done"})
DiscoverySettles == <> (pc["discovery-worker"] = "Done" /\ checkoutPhase = "done")
NeverCancelled == ~shellCancelled
=============================================================================
