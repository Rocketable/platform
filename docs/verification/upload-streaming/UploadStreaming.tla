--------------------------- MODULE UploadStreaming ---------------------------
(* One Web upload streamed to attachment storage, then recorded in the database
   (SessionService.SaveUpload in internal/rocketclaw/backend/attachments.go).

   The upload writes the arriving frames to a pending file (disk) or a pending
   multipart upload (S3), publishes it under the attachment id, then inserts
   and commits the attachments row. When anything after the first write fails,
   discardUnsavedAttachment checks whether the row exists; if not, it deletes
   the object, or queues the id in pending_attachment_deletions when the delete
   fails. A sweeper later deletes queued objects.

   - DiscardOnPutError: a failed Put is followed by the discard too. FALSE
     returns the error at once, as SaveAttachment does for its own Put.
   - DetachDiscard: the discard runs on context.WithoutCancel. FALSE runs it on
     the request's context, which the client's abort cancels.
   - DatabaseMayFail: the discard's own database reads and writes may fail.

   The client may abort at any time (it never has to). The upload and the
   sweeper keep working (weak fairness). *)
EXTENDS TLC

CONSTANTS DiscardOnPutError, DetachDiscard, DatabaseMayFail

ASSUME {DiscardOnPutError, DetachDiscard, DatabaseMayFail} \subseteq BOOLEAN

(* --algorithm UploadStreaming
variables
    pending = FALSE,
    object = FALSE,
    published = FALSE,
    record = "none",
    queued = FALSE,
    aborted = FALSE;

define
    TypeOK ==
        /\ pending \in BOOLEAN
        /\ object \in BOOLEAN
        /\ published \in BOOLEAN
        /\ record \in {"none", "open", "committed"}
        /\ queued \in BOOLEAN
        /\ aborted \in BOOLEAN

    Finished == pc["upload"] = "Done"

    NoDanglingRecord == record = "committed" => object

    NothingLeftBehind == Finished => ~pending /\ (object => record = "committed" \/ queued)

    Settles == <>[](object <=> record = "committed")

    NeverCommitted == record /= "committed"

    NeverQueued == ~queued

    NeverRemovedAfterAbort == ~(Finished /\ aborted /\ published /\ ~object)
end define;

fair process upload = "upload"
begin
Receive:
    pending := TRUE;
Stream:
    if aborted then
        pending := FALSE;
        goto PutFailed;
    else
        either
            skip;
        or
            pending := FALSE;
            goto PutFailed;
        end either;
    end if;
Publish:
    pending := FALSE;
    either
        object := TRUE || published := TRUE;
    or
        object := TRUE || published := TRUE;
        goto PutFailed;
    or
        goto PutFailed;
    end either;
Begin:
    if aborted then
        goto Discard;
    else
        either
            record := "open";
        or
            goto Discard;
        end either;
    end if;
Commit:
    if aborted then
        record := "none";
        goto Discard;
    else
        either
            record := "committed";
            goto Finish;
        or
            record := "none";
            goto Discard;
        or
            record := "committed";
            goto Discard;
        end either;
    end if;
PutFailed:
    if ~DiscardOnPutError then
        goto Finish;
    end if;
Discard:
    if (aborted /\ ~DetachDiscard) \/ record = "committed" then
        goto Finish;
    elsif DatabaseMayFail then
        either
            goto Finish;
        or
            skip;
        end either;
    end if;
Delete:
    either
        object := FALSE;
        goto Finish;
    or
        skip;
    end either;
Queue:
    if DatabaseMayFail then
        either
            queued := TRUE;
        or
            skip;
        end either;
    else
        queued := TRUE;
    end if;
Finish:
    skip;
end process;

fair process sweeper = "sweeper"
begin
Sweep:
    await queued;
    object := FALSE;
Dequeue:
    queued := FALSE;
    goto Sweep;
end process;

process client = "client"
begin
Abort:
    aborted := TRUE;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, pending, object, published, record, queued, aborted

(* define statement *)
TypeOK ==
    /\ pending \in BOOLEAN
    /\ object \in BOOLEAN
    /\ published \in BOOLEAN
    /\ record \in {"none", "open", "committed"}
    /\ queued \in BOOLEAN
    /\ aborted \in BOOLEAN

Finished == pc["upload"] = "Done"

NoDanglingRecord == record = "committed" => object

NothingLeftBehind == Finished => ~pending /\ (object => record = "committed" \/ queued)

Settles == <>[](object <=> record = "committed")

NeverCommitted == record /= "committed"

NeverQueued == ~queued

NeverRemovedAfterAbort == ~(Finished /\ aborted /\ published /\ ~object)


vars == << pc, pending, object, published, record, queued, aborted >>

ProcSet == {"upload"} \cup {"sweeper"} \cup {"client"}

Init == (* Global variables *)
        /\ pending = FALSE
        /\ object = FALSE
        /\ published = FALSE
        /\ record = "none"
        /\ queued = FALSE
        /\ aborted = FALSE
        /\ pc = [self \in ProcSet |-> CASE self = "upload" -> "Receive"
                                        [] self = "sweeper" -> "Sweep"
                                        [] self = "client" -> "Abort"]

Receive == /\ pc["upload"] = "Receive"
           /\ pending' = TRUE
           /\ pc' = [pc EXCEPT !["upload"] = "Stream"]
           /\ UNCHANGED << object, published, record, queued, aborted >>

Stream == /\ pc["upload"] = "Stream"
          /\ IF aborted
                THEN /\ pending' = FALSE
                     /\ pc' = [pc EXCEPT !["upload"] = "PutFailed"]
                ELSE /\ \/ /\ TRUE
                           /\ pc' = [pc EXCEPT !["upload"] = "Publish"]
                           /\ UNCHANGED pending
                        \/ /\ pending' = FALSE
                           /\ pc' = [pc EXCEPT !["upload"] = "PutFailed"]
          /\ UNCHANGED << object, published, record, queued, aborted >>

Publish == /\ pc["upload"] = "Publish"
           /\ pending' = FALSE
           /\ \/ /\ /\ object' = TRUE
                    /\ published' = TRUE
                 /\ pc' = [pc EXCEPT !["upload"] = "Begin"]
              \/ /\ /\ object' = TRUE
                    /\ published' = TRUE
                 /\ pc' = [pc EXCEPT !["upload"] = "PutFailed"]
              \/ /\ pc' = [pc EXCEPT !["upload"] = "PutFailed"]
                 /\ UNCHANGED <<object, published>>
           /\ UNCHANGED << record, queued, aborted >>

Begin == /\ pc["upload"] = "Begin"
         /\ IF aborted
               THEN /\ pc' = [pc EXCEPT !["upload"] = "Discard"]
                    /\ UNCHANGED record
               ELSE /\ \/ /\ record' = "open"
                          /\ pc' = [pc EXCEPT !["upload"] = "Commit"]
                       \/ /\ pc' = [pc EXCEPT !["upload"] = "Discard"]
                          /\ UNCHANGED record
         /\ UNCHANGED << pending, object, published, queued, aborted >>

Commit == /\ pc["upload"] = "Commit"
          /\ IF aborted
                THEN /\ record' = "none"
                     /\ pc' = [pc EXCEPT !["upload"] = "Discard"]
                ELSE /\ \/ /\ record' = "committed"
                           /\ pc' = [pc EXCEPT !["upload"] = "Finish"]
                        \/ /\ record' = "none"
                           /\ pc' = [pc EXCEPT !["upload"] = "Discard"]
                        \/ /\ record' = "committed"
                           /\ pc' = [pc EXCEPT !["upload"] = "Discard"]
          /\ UNCHANGED << pending, object, published, queued, aborted >>

PutFailed == /\ pc["upload"] = "PutFailed"
             /\ IF ~DiscardOnPutError
                   THEN /\ pc' = [pc EXCEPT !["upload"] = "Finish"]
                   ELSE /\ pc' = [pc EXCEPT !["upload"] = "Discard"]
             /\ UNCHANGED << pending, object, published, record, queued, 
                             aborted >>

Discard == /\ pc["upload"] = "Discard"
           /\ IF (aborted /\ ~DetachDiscard) \/ record = "committed"
                 THEN /\ pc' = [pc EXCEPT !["upload"] = "Finish"]
                 ELSE /\ IF DatabaseMayFail
                            THEN /\ \/ /\ pc' = [pc EXCEPT !["upload"] = "Finish"]
                                    \/ /\ TRUE
                                       /\ pc' = [pc EXCEPT !["upload"] = "Delete"]
                            ELSE /\ pc' = [pc EXCEPT !["upload"] = "Delete"]
           /\ UNCHANGED << pending, object, published, record, queued, aborted >>

Delete == /\ pc["upload"] = "Delete"
          /\ \/ /\ object' = FALSE
                /\ pc' = [pc EXCEPT !["upload"] = "Finish"]
             \/ /\ TRUE
                /\ pc' = [pc EXCEPT !["upload"] = "Queue"]
                /\ UNCHANGED object
          /\ UNCHANGED << pending, published, record, queued, aborted >>

Queue == /\ pc["upload"] = "Queue"
         /\ IF DatabaseMayFail
               THEN /\ \/ /\ queued' = TRUE
                       \/ /\ TRUE
                          /\ UNCHANGED queued
               ELSE /\ queued' = TRUE
         /\ pc' = [pc EXCEPT !["upload"] = "Finish"]
         /\ UNCHANGED << pending, object, published, record, aborted >>

Finish == /\ pc["upload"] = "Finish"
          /\ TRUE
          /\ pc' = [pc EXCEPT !["upload"] = "Done"]
          /\ UNCHANGED << pending, object, published, record, queued, aborted >>

upload == Receive \/ Stream \/ Publish \/ Begin \/ Commit \/ PutFailed
             \/ Discard \/ Delete \/ Queue \/ Finish

Sweep == /\ pc["sweeper"] = "Sweep"
         /\ queued
         /\ object' = FALSE
         /\ pc' = [pc EXCEPT !["sweeper"] = "Dequeue"]
         /\ UNCHANGED << pending, published, record, queued, aborted >>

Dequeue == /\ pc["sweeper"] = "Dequeue"
           /\ queued' = FALSE
           /\ pc' = [pc EXCEPT !["sweeper"] = "Sweep"]
           /\ UNCHANGED << pending, object, published, record, aborted >>

sweeper == Sweep \/ Dequeue

Abort == /\ pc["client"] = "Abort"
         /\ aborted' = TRUE
         /\ pc' = [pc EXCEPT !["client"] = "Done"]
         /\ UNCHANGED << pending, object, published, record, queued >>

client == Abort

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == upload \/ sweeper \/ client
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(upload)
        /\ WF_vars(sweeper)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION
==============================================================================
