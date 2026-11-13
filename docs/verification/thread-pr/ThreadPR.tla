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
Created(t) == <<t, "created">>
Checkout(t) == <<t, "checkout">>
\* Finite evidence classes stand for status checks AND parser acceptance.
Evidence(t, kind) == [pr |-> IF kind = "create" THEN Created(t) ELSE Checkout(t),
    recordedSuccess |-> kind \in {"create", "view"},
    parserAccepts |-> kind \in {"create", "failed"}]
ProvenCreate(e) == e.recordedSuccess /\ e.parserAccepts
Accepted(records) == {e.pr : e \in {r \in records : ProvenCreate(r)}}

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
    historyPhase = "page1", historyThread = 1, historyEpoch = 0,
    gathered = {}, history = {}, everHistory = {}, owned = {},
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
                history := {}; everHistory := {}; owned := {}; checkoutURL := {};
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
HistoryAndCheckout:
    while TRUE do
        await DiscoveryCase;
        either
            await historyPhase = "page1";
            \* Abstract a successful recorded create command or script with an accepted URL.
            gathered := Accepted({Evidence(historyThread, "create")});
            historyPhase := "page2";
        or
            await historyPhase = "page2";
            \* Page 2 also contains links/failed calls: no success evidence, no addition.
            gathered := gathered \cup Accepted({Evidence(historyThread, k) :
                k \in {"view", "failed", "mention"}});
            historyPhase := "commit";
        or
            await historyPhase = "commit";
            if historyEpoch = epoch then
                history := gathered; everHistory := gathered; owned := gathered;
            end if;
            historyPhase := "done";
        or
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
                    checkoutURL := {Checkout(historyThread)};
                    if Fault = "own-checkout" then
                        owned := owned \cup checkoutURL;
                    end if;
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
VARIABLES alive, thread, source, epoch, moved, serverRevision, revisionMoves, 
          historyPhase, historyThread, historyEpoch, gathered, history, 
          everHistory, owned, checkoutPhase, checkoutURL, checkoutFailed, 
          metadata, metadataThread, metadataSource, metadataRevision, patch, 
          patchSource, patchRevision, filesStarted, reloadStarted, 
          readerCancelled, loading, staleCommit, cancelledCommit, requests, 
          shellPhase, shellCancelled, shellCreated, shellKnown, shellDeleted, 
          shellOutputReady, shellCommitted

vars == << alive, thread, source, epoch, moved, serverRevision, revisionMoves, 
           historyPhase, historyThread, historyEpoch, gathered, history, 
           everHistory, owned, checkoutPhase, checkoutURL, checkoutFailed, 
           metadata, metadataThread, metadataSource, metadataRevision, patch, 
           patchSource, patchRevision, filesStarted, reloadStarted, 
           readerCancelled, loading, staleCommit, cancelledCommit, requests, 
           shellPhase, shellCancelled, shellCreated, shellKnown, shellDeleted, 
           shellOutputReady, shellCommitted >>

ProcSet == {"world"} \cup {"discovery-worker"} \cup {"reader-worker"} \cup {"shell-worker"}

Init == (* Global variables *)
        /\ alive = TRUE
        /\ thread = 1
        /\ source = 1
        /\ epoch = 0
        /\ moved = FALSE
        /\ serverRevision = ZeroRevision
        /\ revisionMoves = 0
        /\ historyPhase = "page1"
        /\ historyThread = 1
        /\ historyEpoch = 0
        /\ gathered = {}
        /\ history = {}
        /\ everHistory = {}
        /\ owned = {}
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

World == /\ \/ /\ (DiscoveryCase \/ ReaderCase) /\ ~moved
               /\ moved' = TRUE
               /\ epoch' = epoch + 1
               /\ IF Case = "source-change"
                     THEN /\ source' = 2
                          /\ IF Fault # "retain-source"
                                THEN /\ metadata' = FALSE
                                     /\ patch' = FALSE
                                ELSE /\ TRUE
                                     /\ UNCHANGED << metadata, patch >>
                          /\ UNCHANGED << thread, history, everHistory, owned, 
                                          checkoutURL >>
                     ELSE /\ thread' = 2
                          /\ source' = 2
                          /\ history' = {}
                          /\ everHistory' = {}
                          /\ owned' = {}
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
                          /\ UNCHANGED << reloadStarted, readerCancelled, 
                                          loading >>
               /\ UNCHANGED <<alive, serverRevision, revisionMoves, filesStarted, shellCancelled>>
            \/ /\ ReaderCase /\ metadata /\ ~filesStarted /\ ~readerCancelled
               /\ filesStarted' = TRUE
               /\ loading' = TRUE
               /\ requests' =         [requests EXCEPT ![2].aborted = TRUE,
                              ![1] = [EmptyRequest EXCEPT !.phase = "before",
                                  !.threadID = thread, !.origin = source, !.generation = epoch,
                                  !.revision = metadataRevision]]
               /\ UNCHANGED <<alive, thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, owned, checkoutURL, metadata, patch, reloadStarted, readerCancelled, shellCancelled>>
            \/ /\ ReaderCase /\ ~reloadStarted /\ ~loading
               /\ reloadStarted' = TRUE
               /\ readerCancelled' = FALSE
               /\ metadata' = FALSE
               /\ patch' = FALSE
               /\ loading' = TRUE
               /\ requests' =         [requests EXCEPT ![1].aborted = TRUE,
                              ![2] = [EmptyRequest EXCEPT !.phase = "metadata",
                                  !.threadID = thread, !.origin = source, !.generation = epoch]]
               /\ UNCHANGED <<alive, thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, owned, checkoutURL, filesStarted, shellCancelled>>
            \/ /\ ReaderCase /\ loading /\ ~readerCancelled
               /\ readerCancelled' = TRUE
               /\ loading' = FALSE
               /\ requests' = [i \in 1..2 |-> [requests[i] EXCEPT !.aborted = TRUE]]
               /\ UNCHANGED <<alive, thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, owned, checkoutURL, metadata, patch, filesStarted, reloadStarted, shellCancelled>>
            \/ /\ ReaderCase /\ revisionMoves < RevisionLimit
               /\ \E coordinate \in {"base", "head", "count"}:
                    serverRevision' = [serverRevision EXCEPT ![coordinate] = 1 - serverRevision[coordinate]]
               /\ revisionMoves' = revisionMoves + 1
               /\ UNCHANGED <<alive, thread, source, epoch, moved, history, everHistory, owned, checkoutURL, metadata, patch, filesStarted, reloadStarted, readerCancelled, loading, requests, shellCancelled>>
            \/ /\ ShellCase /\ ~shellCancelled /\ shellPhase # "done"
               /\ shellCancelled' = TRUE
               /\ UNCHANGED <<alive, thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, owned, checkoutURL, metadata, patch, filesStarted, reloadStarted, readerCancelled, loading, requests>>
            \/ /\ Case = "browser-loss" /\ alive /\ shellKnown /\ ~shellDeleted
               /\ alive' = FALSE
               /\ UNCHANGED <<thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, owned, checkoutURL, metadata, patch, filesStarted, reloadStarted, readerCancelled, loading, requests, shellCancelled>>
            \/ /\ TRUE
               /\ UNCHANGED <<alive, thread, source, epoch, moved, serverRevision, revisionMoves, history, everHistory, owned, checkoutURL, metadata, patch, filesStarted, reloadStarted, readerCancelled, loading, requests, shellCancelled>>
         /\ UNCHANGED << historyPhase, historyThread, historyEpoch, gathered, 
                         checkoutPhase, checkoutFailed, metadataThread, 
                         metadataSource, metadataRevision, patchSource, 
                         patchRevision, staleCommit, cancelledCommit, 
                         shellPhase, shellCreated, shellKnown, shellDeleted, 
                         shellOutputReady, shellCommitted >>

Discovery == /\ DiscoveryCase
             /\ \/ /\ historyPhase = "page1"
                   /\ gathered' = Accepted({Evidence(historyThread, "create")})
                   /\ historyPhase' = "page2"
                   /\ UNCHANGED <<history, everHistory, owned, checkoutPhase, checkoutURL, checkoutFailed>>
                \/ /\ historyPhase = "page2"
                   /\ gathered' = (        gathered \cup Accepted({Evidence(historyThread, k) :
                                   k \in {"view", "failed", "mention"}}))
                   /\ historyPhase' = "commit"
                   /\ UNCHANGED <<history, everHistory, owned, checkoutPhase, checkoutURL, checkoutFailed>>
                \/ /\ historyPhase = "commit"
                   /\ IF historyEpoch = epoch
                         THEN /\ history' = gathered
                              /\ everHistory' = gathered
                              /\ owned' = gathered
                         ELSE /\ TRUE
                              /\ UNCHANGED << history, everHistory, owned >>
                   /\ historyPhase' = "done"
                   /\ UNCHANGED <<gathered, checkoutPhase, checkoutURL, checkoutFailed>>
                \/ /\ checkoutPhase = "pending"
                   /\ \/ /\ checkoutPhase' = "success"
                      \/ /\ checkoutPhase' = "failure"
                      \/ /\ checkoutPhase' = "none"
                   /\ UNCHANGED <<historyPhase, gathered, history, everHistory, owned, checkoutURL, checkoutFailed>>
                \/ /\ checkoutPhase \in {"success", "failure", "none"}
                   /\ IF historyEpoch = epoch
                         THEN /\ IF checkoutPhase = "success"
                                    THEN /\ checkoutURL' = {Checkout(historyThread)}
                                         /\ IF Fault = "own-checkout"
                                               THEN /\ owned' = (owned \cup checkoutURL')
                                               ELSE /\ TRUE
                                                    /\ owned' = owned
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
                                         /\ UNCHANGED << owned, checkoutURL >>
                         ELSE /\ TRUE
                              /\ UNCHANGED << history, owned, checkoutURL, 
                                              checkoutFailed >>
                   /\ checkoutPhase' = "done"
                   /\ UNCHANGED <<historyPhase, gathered, everHistory>>
             /\ UNCHANGED << alive, thread, source, epoch, moved, 
                             serverRevision, revisionMoves, historyThread, 
                             historyEpoch, metadata, metadataThread, 
                             metadataSource, metadataRevision, patch, 
                             patchSource, patchRevision, filesStarted, 
                             reloadStarted, readerCancelled, loading, 
                             staleCommit, cancelledCommit, requests, 
                             shellPhase, shellCancelled, shellCreated, 
                             shellKnown, shellDeleted, shellOutputReady, 
                             shellCommitted >>

Reader == /\ ReaderCase /\ alive
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
                                     /\ UNCHANGED << metadata, metadataThread, 
                                                     metadataSource, 
                                                     metadataRevision, patch, 
                                                     loading, staleCommit, 
                                                     cancelledCommit >>
                          /\ requests' = [requests EXCEPT ![i].phase = "done"]
                          /\ UNCHANGED << patchSource, patchRevision >>
                     ELSE /\ IF requests[i].phase = "before"
                                THEN /\ IF serverRevision # requests[i].revision
                                           THEN /\ requests' = [requests EXCEPT ![i].phase = "done"]
                                                /\ IF ~requests'[i].aborted
                                                      THEN /\ loading' = FALSE
                                                      ELSE /\ TRUE
                                                           /\ UNCHANGED loading
                                           ELSE /\ requests' = [requests EXCEPT ![i].phase = "files"]
                                                /\ UNCHANGED loading
                                     /\ UNCHANGED << patch, patchSource, 
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
                                          metadataSource, metadataRevision >>
          /\ UNCHANGED << alive, thread, source, epoch, moved, serverRevision, 
                          revisionMoves, historyPhase, historyThread, 
                          historyEpoch, gathered, history, everHistory, owned, 
                          checkoutPhase, checkoutURL, checkoutFailed, 
                          filesStarted, reloadStarted, readerCancelled, 
                          shellPhase, shellCancelled, shellCreated, shellKnown, 
                          shellDeleted, shellOutputReady, shellCommitted >>

Shell == /\ ShellCase /\ alive /\ shellPhase # "done"
         /\ IF shellPhase = "pending"
               THEN /\ shellPhase' = IF shellCancelled THEN "done" ELSE "sent"
                    /\ UNCHANGED << shellCreated, shellKnown, shellDeleted, 
                                    shellOutputReady, shellCommitted >>
               ELSE /\ IF shellPhase = "sent"
                          THEN /\ shellCreated' = TRUE
                               /\ shellPhase' = "allocated"
                               /\ UNCHANGED << shellKnown, shellDeleted, 
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
         /\ UNCHANGED << alive, thread, source, epoch, moved, serverRevision, 
                         revisionMoves, historyPhase, historyThread, 
                         historyEpoch, gathered, history, everHistory, owned, 
                         checkoutPhase, checkoutURL, checkoutFailed, metadata, 
                         metadataThread, metadataSource, metadataRevision, 
                         patch, patchSource, patchRevision, filesStarted, 
                         reloadStarted, readerCancelled, loading, staleCommit, 
                         cancelledCommit, requests, shellCancelled >>

Next == World \/ Discovery \/ Reader \/ Shell

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Discovery)
        /\ WF_vars(Reader)
        /\ WF_vars(Shell)

\* END TRANSLATION

VisiblePRs == history \cup checkoutURL
OnlyProvenCreateOwned == owned \subseteq {Created(thread)}
NoWrongThreadDiscovery == history \subseteq {Created(thread)}
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
DiscoverySettles == <> (historyPhase = "done" /\ checkoutPhase = "done")
NeverCancelled == ~shellCancelled
=============================================================================
