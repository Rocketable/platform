----------------------------- MODULE ContextOverflowCompaction -----------------------------
EXTENDS Naturals

CONSTANTS Window, Threshold, CompactLimit, Compacted, Reply, MaxInput, Turns, StepsPerTurn, Fixed, Fallback

ASSUME Compacted + Reply + MaxInput <= Window
ASSUME Threshold <= Window
ASSUME Window <= CompactLimit

(* --algorithm ContextOverflowCompaction
variables
    saved = 0,
    sent = 0,
    turn = 1,
    step = 1,
    lastStep = "none",
    holdsCompacted = FALSE,
    overflowStreak = 0,
    outcome = "running",
    history = 0,
    newest = 0,
    chunk = 0,
    chunkRecovered = FALSE;

define
    TypeOK == /\ saved \in Nat /\ sent \in Nat
              /\ turn \in 1..Turns /\ step \in 1..StepsPerTurn
              /\ lastStep \in {"none", "reply", "threshold", "recovery", "chunked"}
              /\ holdsCompacted \in BOOLEAN
              /\ overflowStreak \in Nat
              /\ outcome \in {"running", "done", "failed"}
              /\ history \in Nat /\ newest \in Nat /\ chunk \in Nat
              /\ chunkRecovered \in BOOLEAN
    SavedWithinWindowAfterCompaction == lastStep \in {"threshold", "recovery"} => saved <= Window
    NoResendOfCompactedHistory == ~holdsCompacted
    OneOverflowPerEpisode == overflowStreak <= 1
    WithinCompactLimit == outcome # "failed"
    NeverRecovered == lastStep # "recovery"
    NeverThresholdCompaction == lastStep # "threshold"
    NeverChunkRecovered == ~chunkRecovered
end define;

macro Recovered(request) begin
    lastStep := "recovery";
    if Fixed then
        saved := Compacted + Reply;
        holdsCompacted := FALSE;
    else
        saved := request + Reply;
        holdsCompacted := TRUE;
    end if;
end macro;

fair process session = "session"
begin
Request:
    while outcome = "running" do
        with input \in 1..MaxInput, request = saved + input do
            sent := request;
            if request > Window then
                overflowStreak := overflowStreak + 1;
                if request <= CompactLimit then
                    Recovered(request);
                elsif Fallback then
                    history := saved;
                    newest := input;
                    chunk := 1;
                else
                    outcome := "failed";
                end if;
            elsif request + Reply >= Threshold then
                overflowStreak := 0;
                lastStep := "threshold";
                saved := Compacted + Reply;
                holdsCompacted := FALSE;
            else
                overflowStreak := 0;
                lastStep := "reply";
                saved := request + Reply;
            end if;
        end with;
    ChunkedFallback:
        while chunk > 0 do
            if chunk > history \/ chunk > CompactLimit then
                outcome := "failed";
                chunk := 0;
            else
                with retry = Compacted + (history - chunk) + newest do
                    sent := retry;
                    if retry <= Window then
                        overflowStreak := 0;
                        chunkRecovered := TRUE;
                        chunk := 0;
                        holdsCompacted := FALSE;
                        if retry + Reply >= Threshold then
                            lastStep := "threshold";
                            saved := Compacted + Reply;
                        else
                            lastStep := "reply";
                            saved := retry + Reply;
                        end if;
                    elsif retry <= CompactLimit then
                        chunkRecovered := TRUE;
                        chunk := 0;
                        Recovered(retry);
                    else
                        lastStep := "chunked";
                        saved := retry;
                        holdsCompacted := FALSE;
                        chunk := chunk + 1;
                    end if;
                end with;
            end if;
        end while;
    Advance:
        if outcome = "running" then
            if step < StepsPerTurn then
                step := step + 1;
            elsif turn < Turns then
                turn := turn + 1;
                step := 1;
            else
                outcome := "done";
            end if;
        end if;
    end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, saved, sent, turn, step, lastStep, holdsCompacted, 
          overflowStreak, outcome, history, newest, chunk, chunkRecovered

(* define statement *)
TypeOK == /\ saved \in Nat /\ sent \in Nat
          /\ turn \in 1..Turns /\ step \in 1..StepsPerTurn
          /\ lastStep \in {"none", "reply", "threshold", "recovery", "chunked"}
          /\ holdsCompacted \in BOOLEAN
          /\ overflowStreak \in Nat
          /\ outcome \in {"running", "done", "failed"}
          /\ history \in Nat /\ newest \in Nat /\ chunk \in Nat
          /\ chunkRecovered \in BOOLEAN
SavedWithinWindowAfterCompaction == lastStep \in {"threshold", "recovery"} => saved <= Window
NoResendOfCompactedHistory == ~holdsCompacted
OneOverflowPerEpisode == overflowStreak <= 1
WithinCompactLimit == outcome # "failed"
NeverRecovered == lastStep # "recovery"
NeverThresholdCompaction == lastStep # "threshold"
NeverChunkRecovered == ~chunkRecovered


vars == << pc, saved, sent, turn, step, lastStep, holdsCompacted, 
           overflowStreak, outcome, history, newest, chunk, chunkRecovered >>

ProcSet == {"session"}

Init == (* Global variables *)
        /\ saved = 0
        /\ sent = 0
        /\ turn = 1
        /\ step = 1
        /\ lastStep = "none"
        /\ holdsCompacted = FALSE
        /\ overflowStreak = 0
        /\ outcome = "running"
        /\ history = 0
        /\ newest = 0
        /\ chunk = 0
        /\ chunkRecovered = FALSE
        /\ pc = [self \in ProcSet |-> "Request"]

Request == /\ pc["session"] = "Request"
           /\ IF outcome = "running"
                 THEN /\ \E input \in 1..MaxInput:
                           LET request == saved + input IN
                             /\ sent' = request
                             /\ IF request > Window
                                   THEN /\ overflowStreak' = overflowStreak + 1
                                        /\ IF request <= CompactLimit
                                              THEN /\ lastStep' = "recovery"
                                                   /\ IF Fixed
                                                         THEN /\ saved' = Compacted + Reply
                                                              /\ holdsCompacted' = FALSE
                                                         ELSE /\ saved' = request + Reply
                                                              /\ holdsCompacted' = TRUE
                                                   /\ UNCHANGED << outcome, 
                                                                   history, 
                                                                   newest, 
                                                                   chunk >>
                                              ELSE /\ IF Fallback
                                                         THEN /\ history' = saved
                                                              /\ newest' = input
                                                              /\ chunk' = 1
                                                              /\ UNCHANGED outcome
                                                         ELSE /\ outcome' = "failed"
                                                              /\ UNCHANGED << history, 
                                                                              newest, 
                                                                              chunk >>
                                                   /\ UNCHANGED << saved, 
                                                                   lastStep, 
                                                                   holdsCompacted >>
                                   ELSE /\ IF request + Reply >= Threshold
                                              THEN /\ overflowStreak' = 0
                                                   /\ lastStep' = "threshold"
                                                   /\ saved' = Compacted + Reply
                                                   /\ holdsCompacted' = FALSE
                                              ELSE /\ overflowStreak' = 0
                                                   /\ lastStep' = "reply"
                                                   /\ saved' = request + Reply
                                                   /\ UNCHANGED holdsCompacted
                                        /\ UNCHANGED << outcome, history, 
                                                        newest, chunk >>
                      /\ pc' = [pc EXCEPT !["session"] = "ChunkedFallback"]
                 ELSE /\ pc' = [pc EXCEPT !["session"] = "Done"]
                      /\ UNCHANGED << saved, sent, lastStep, holdsCompacted, 
                                      overflowStreak, outcome, history, newest, 
                                      chunk >>
           /\ UNCHANGED << turn, step, chunkRecovered >>

ChunkedFallback == /\ pc["session"] = "ChunkedFallback"
                   /\ IF chunk > 0
                         THEN /\ IF chunk > history \/ chunk > CompactLimit
                                    THEN /\ outcome' = "failed"
                                         /\ chunk' = 0
                                         /\ UNCHANGED << saved, sent, lastStep, 
                                                         holdsCompacted, 
                                                         overflowStreak, 
                                                         chunkRecovered >>
                                    ELSE /\ LET retry == Compacted + (history - chunk) + newest IN
                                              /\ sent' = retry
                                              /\ IF retry <= Window
                                                    THEN /\ overflowStreak' = 0
                                                         /\ chunkRecovered' = TRUE
                                                         /\ chunk' = 0
                                                         /\ holdsCompacted' = FALSE
                                                         /\ IF retry + Reply >= Threshold
                                                               THEN /\ lastStep' = "threshold"
                                                                    /\ saved' = Compacted + Reply
                                                               ELSE /\ lastStep' = "reply"
                                                                    /\ saved' = retry + Reply
                                                    ELSE /\ IF retry <= CompactLimit
                                                               THEN /\ chunkRecovered' = TRUE
                                                                    /\ chunk' = 0
                                                                    /\ lastStep' = "recovery"
                                                                    /\ IF Fixed
                                                                          THEN /\ saved' = Compacted + Reply
                                                                               /\ holdsCompacted' = FALSE
                                                                          ELSE /\ saved' = retry + Reply
                                                                               /\ holdsCompacted' = TRUE
                                                               ELSE /\ lastStep' = "chunked"
                                                                    /\ saved' = retry
                                                                    /\ holdsCompacted' = FALSE
                                                                    /\ chunk' = chunk + 1
                                                                    /\ UNCHANGED chunkRecovered
                                                         /\ UNCHANGED overflowStreak
                                         /\ UNCHANGED outcome
                              /\ pc' = [pc EXCEPT !["session"] = "ChunkedFallback"]
                         ELSE /\ pc' = [pc EXCEPT !["session"] = "Advance"]
                              /\ UNCHANGED << saved, sent, lastStep, 
                                              holdsCompacted, overflowStreak, 
                                              outcome, chunk, chunkRecovered >>
                   /\ UNCHANGED << turn, step, history, newest >>

Advance == /\ pc["session"] = "Advance"
           /\ IF outcome = "running"
                 THEN /\ IF step < StepsPerTurn
                            THEN /\ step' = step + 1
                                 /\ UNCHANGED << turn, outcome >>
                            ELSE /\ IF turn < Turns
                                       THEN /\ turn' = turn + 1
                                            /\ step' = 1
                                            /\ UNCHANGED outcome
                                       ELSE /\ outcome' = "done"
                                            /\ UNCHANGED << turn, step >>
                 ELSE /\ TRUE
                      /\ UNCHANGED << turn, step, outcome >>
           /\ pc' = [pc EXCEPT !["session"] = "Request"]
           /\ UNCHANGED << saved, sent, lastStep, holdsCompacted, 
                           overflowStreak, history, newest, chunk, 
                           chunkRecovered >>

session == Request \/ ChunkedFallback \/ Advance

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == session
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(session)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION

EveryTurnCompletes == <>(outcome = "done")

=============================================================================================
