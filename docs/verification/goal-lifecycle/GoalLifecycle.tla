--------------------------- MODULE GoalLifecycle ---------------------------
EXTENDS Integers, Sequences

(* One conversation's goal as built (Mutation = "none"): its turns run one at a time in the bridge's lane, a goal
   continuation waits in the bridge's in-memory request channel, and the database row holds the goal's status,
   its turn count and its continuation claim (goal_pending). A person stops and resumes the goal; the process can
   crash, which drops the request channel and keeps the database and the running turn, which resumes. Each other
   Mutation turns one part of the claim off, for the must-fail checks. README.md maps every label to its code. *)

CONSTANTS MaxTurns, MaxStops, MaxResumes, MaxCrashes, Mutation

ASSUME Mutation \in {"none", "no-claim", "resume-no-claim", "no-startup-clear"}

Resumable == {"stopped", "blocked", "exhausted"}

(* --algorithm GoalLifecycle
variables
    status = "active",
    turnsUsed = 0,
    pending = FALSE,
    running = TRUE,
    queue = <<>>,
    taken = FALSE,
    up = TRUE,
    stops = 0,
    resumes = 0,
    crashes = 0;

define
    AtMostOneContinuation == Len(queue) <= 1
    ClaimMatchesQueue == up => (pending <=> Len(queue) = 1)
    TurnsWithinBudget == turnsUsed <= MaxTurns
    GoalKeepsMoving == status = "active" ~> (status /= "active" \/ running \/ Len(queue) > 0)
    GoalEnds == <>[](status /= "active" /\ ~running)
end define;

macro claim(always) begin
    if always \/ (status = "active" /\ ~pending) then
        pending := TRUE;
        queue := Append(queue, "C");
    end if;
end macro;

fair process Lane = "lane"
begin
Loop:
    while TRUE do
        await up;
        if running then
Turn:
            await up;
            either
                skip;
            or
                if status = "active" then
                    with outcome \in {"complete", "blocked"} do status := outcome; end with;
                end if;
            end either;
Account:
            await up;
            if status = "active" then
                turnsUsed := turnsUsed + 1;
                if turnsUsed >= MaxTurns then status := "exhausted"; end if;
            end if;
FinishGoalTurn:
            await up;
            claim(Mutation = "no-claim" /\ status = "active");
            running := FALSE;
        else
Take:
            await up /\ queue /= <<>>;
            queue := Tail(queue);
            pending := FALSE;
            taken := TRUE;
SkipObsolete:
            await up;
            if taken /\ status = "active" then running := TRUE; end if;
            taken := FALSE;
        end if;
    end while;
end process;

fair process Person = "person"
begin
Act:
    while stops < MaxStops \/ resumes < MaxResumes do
        either
            await up /\ stops < MaxStops;
            stops := stops + 1;
            if status = "active" then status := "stopped"; end if;
        or
            await up /\ resumes < MaxResumes;
            resumes := resumes + 1;
            if status \in Resumable then
                status := "active";
                turnsUsed := 0;
ResumeClaim:
                await up;
                if Mutation /= "resume-no-claim" then
                    claim(Mutation = "no-claim");
                end if;
            end if;
        end either;
    end while;
end process;

fair process Startup = "startup"
begin
Crash:
    while crashes < MaxCrashes do
        crashes := crashes + 1;
        up := FALSE;
        queue := <<>>;
        taken := FALSE;
Restart:
        if Mutation /= "no-startup-clear" then pending := FALSE; end if;
        up := TRUE;
StartActiveGoals:
        if ~running then claim(FALSE); end if;
    end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, status, turnsUsed, pending, running, queue, taken, up, stops, 
          resumes, crashes

(* define statement *)
AtMostOneContinuation == Len(queue) <= 1
ClaimMatchesQueue == up => (pending <=> Len(queue) = 1)
TurnsWithinBudget == turnsUsed <= MaxTurns
GoalKeepsMoving == status = "active" ~> (status /= "active" \/ running \/ Len(queue) > 0)
GoalEnds == <>[](status /= "active" /\ ~running)


vars == << pc, status, turnsUsed, pending, running, queue, taken, up, stops, 
           resumes, crashes >>

ProcSet == {"lane"} \cup {"person"} \cup {"startup"}

Init == (* Global variables *)
        /\ status = "active"
        /\ turnsUsed = 0
        /\ pending = FALSE
        /\ running = TRUE
        /\ queue = <<>>
        /\ taken = FALSE
        /\ up = TRUE
        /\ stops = 0
        /\ resumes = 0
        /\ crashes = 0
        /\ pc = [self \in ProcSet |-> CASE self = "lane" -> "Loop"
                                        [] self = "person" -> "Act"
                                        [] self = "startup" -> "Crash"]

Loop == /\ pc["lane"] = "Loop"
        /\ up
        /\ IF running
              THEN /\ pc' = [pc EXCEPT !["lane"] = "Turn"]
              ELSE /\ pc' = [pc EXCEPT !["lane"] = "Take"]
        /\ UNCHANGED << status, turnsUsed, pending, running, queue, taken, up, 
                        stops, resumes, crashes >>

Turn == /\ pc["lane"] = "Turn"
        /\ up
        /\ \/ /\ TRUE
              /\ UNCHANGED status
           \/ /\ IF status = "active"
                    THEN /\ \E outcome \in {"complete", "blocked"}:
                              status' = outcome
                    ELSE /\ TRUE
                         /\ UNCHANGED status
        /\ pc' = [pc EXCEPT !["lane"] = "Account"]
        /\ UNCHANGED << turnsUsed, pending, running, queue, taken, up, stops, 
                        resumes, crashes >>

Account == /\ pc["lane"] = "Account"
           /\ up
           /\ IF status = "active"
                 THEN /\ turnsUsed' = turnsUsed + 1
                      /\ IF turnsUsed' >= MaxTurns
                            THEN /\ status' = "exhausted"
                            ELSE /\ TRUE
                                 /\ UNCHANGED status
                 ELSE /\ TRUE
                      /\ UNCHANGED << status, turnsUsed >>
           /\ pc' = [pc EXCEPT !["lane"] = "FinishGoalTurn"]
           /\ UNCHANGED << pending, running, queue, taken, up, stops, resumes, 
                           crashes >>

FinishGoalTurn == /\ pc["lane"] = "FinishGoalTurn"
                  /\ up
                  /\ IF (Mutation = "no-claim" /\ status = "active") \/ (status = "active" /\ ~pending)
                        THEN /\ pending' = TRUE
                             /\ queue' = Append(queue, "C")
                        ELSE /\ TRUE
                             /\ UNCHANGED << pending, queue >>
                  /\ running' = FALSE
                  /\ pc' = [pc EXCEPT !["lane"] = "Loop"]
                  /\ UNCHANGED << status, turnsUsed, taken, up, stops, resumes, 
                                  crashes >>

Take == /\ pc["lane"] = "Take"
        /\ up /\ queue /= <<>>
        /\ queue' = Tail(queue)
        /\ pending' = FALSE
        /\ taken' = TRUE
        /\ pc' = [pc EXCEPT !["lane"] = "SkipObsolete"]
        /\ UNCHANGED << status, turnsUsed, running, up, stops, resumes, 
                        crashes >>

SkipObsolete == /\ pc["lane"] = "SkipObsolete"
                /\ up
                /\ IF taken /\ status = "active"
                      THEN /\ running' = TRUE
                      ELSE /\ TRUE
                           /\ UNCHANGED running
                /\ taken' = FALSE
                /\ pc' = [pc EXCEPT !["lane"] = "Loop"]
                /\ UNCHANGED << status, turnsUsed, pending, queue, up, stops, 
                                resumes, crashes >>

Lane == Loop \/ Turn \/ Account \/ FinishGoalTurn \/ Take \/ SkipObsolete

Act == /\ pc["person"] = "Act"
       /\ IF stops < MaxStops \/ resumes < MaxResumes
             THEN /\ \/ /\ up /\ stops < MaxStops
                        /\ stops' = stops + 1
                        /\ IF status = "active"
                              THEN /\ status' = "stopped"
                              ELSE /\ TRUE
                                   /\ UNCHANGED status
                        /\ pc' = [pc EXCEPT !["person"] = "Act"]
                        /\ UNCHANGED <<turnsUsed, resumes>>
                     \/ /\ up /\ resumes < MaxResumes
                        /\ resumes' = resumes + 1
                        /\ IF status \in Resumable
                              THEN /\ status' = "active"
                                   /\ turnsUsed' = 0
                                   /\ pc' = [pc EXCEPT !["person"] = "ResumeClaim"]
                              ELSE /\ pc' = [pc EXCEPT !["person"] = "Act"]
                                   /\ UNCHANGED << status, turnsUsed >>
                        /\ stops' = stops
             ELSE /\ pc' = [pc EXCEPT !["person"] = "Done"]
                  /\ UNCHANGED << status, turnsUsed, stops, resumes >>
       /\ UNCHANGED << pending, running, queue, taken, up, crashes >>

ResumeClaim == /\ pc["person"] = "ResumeClaim"
               /\ up
               /\ IF Mutation /= "resume-no-claim"
                     THEN /\ IF (Mutation = "no-claim") \/ (status = "active" /\ ~pending)
                                THEN /\ pending' = TRUE
                                     /\ queue' = Append(queue, "C")
                                ELSE /\ TRUE
                                     /\ UNCHANGED << pending, queue >>
                     ELSE /\ TRUE
                          /\ UNCHANGED << pending, queue >>
               /\ pc' = [pc EXCEPT !["person"] = "Act"]
               /\ UNCHANGED << status, turnsUsed, running, taken, up, stops, 
                               resumes, crashes >>

Person == Act \/ ResumeClaim

Crash == /\ pc["startup"] = "Crash"
         /\ IF crashes < MaxCrashes
               THEN /\ crashes' = crashes + 1
                    /\ up' = FALSE
                    /\ queue' = <<>>
                    /\ taken' = FALSE
                    /\ pc' = [pc EXCEPT !["startup"] = "Restart"]
               ELSE /\ pc' = [pc EXCEPT !["startup"] = "Done"]
                    /\ UNCHANGED << queue, taken, up, crashes >>
         /\ UNCHANGED << status, turnsUsed, pending, running, stops, resumes >>

Restart == /\ pc["startup"] = "Restart"
           /\ IF Mutation /= "no-startup-clear"
                 THEN /\ pending' = FALSE
                 ELSE /\ TRUE
                      /\ UNCHANGED pending
           /\ up' = TRUE
           /\ pc' = [pc EXCEPT !["startup"] = "StartActiveGoals"]
           /\ UNCHANGED << status, turnsUsed, running, queue, taken, stops, 
                           resumes, crashes >>

StartActiveGoals == /\ pc["startup"] = "StartActiveGoals"
                    /\ IF ~running
                          THEN /\ IF FALSE \/ (status = "active" /\ ~pending)
                                     THEN /\ pending' = TRUE
                                          /\ queue' = Append(queue, "C")
                                     ELSE /\ TRUE
                                          /\ UNCHANGED << pending, queue >>
                          ELSE /\ TRUE
                               /\ UNCHANGED << pending, queue >>
                    /\ pc' = [pc EXCEPT !["startup"] = "Crash"]
                    /\ UNCHANGED << status, turnsUsed, running, taken, up, 
                                    stops, resumes, crashes >>

Startup == Crash \/ Restart \/ StartActiveGoals

Next == Lane \/ Person \/ Startup

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Lane)
        /\ WF_vars(Person)
        /\ WF_vars(Startup)

\* END TRANSLATION
=============================================================================
