---------------------------- MODULE TurnStanding ----------------------------
EXTENDS Integers, Sequences, FiniteSets

(* One session's Bridge and the standing of its turns (Mutation = "none"); each other Mutation brings back one
   behavior the rule forbids. README.md maps every step to the code.

   Every input is addressed ("addr") or unaddressed ("amb", Slack ambient). A turn takes its standing from the input
   or wake that starts it, and runs only inputs and Completion Notes of that standing. A steer joins an open turn only
   when their standings match; otherwise it waits and later runs as its own turn. A Background Job's note carries the
   standing of the turn that started the job. A running turn claims only notes of its standing. A wake takes the
   standing of the earliest waking note and claims only notes of that standing. A non-waking note runs only with a
   later turn of its standing. *)

CONSTANTS AddrInputs, AmbInputs, MaxJobs, MaxShutdowns, Mutation

Mutations == {"none", "mixed-wake", "steer-any", "drain-any"}

ASSUME Mutation \in Mutations
ASSUME AddrInputs \cap AmbInputs = {} /\ \A i \in AddrInputs \cup AmbInputs : i \in Nat \ {0}

Inputs == AddrInputs \cup AmbInputs
Jobs == 1..MaxJobs
Standings == {"addr", "amb"}
NoTurn == [on |-> FALSE, s |-> "addr", open |-> FALSE, ins |-> {}, notes |-> {}]

(* --algorithm TurnStanding
variables
    \* Durable: inputs, the Thread Queue, the turn row with its inputs and claimed notes, the Background Jobs.
    inp = [i \in Inputs |-> "new"],
    waiting = <<>>,
    turn = NoTurn,
    job = [j \in Jobs |-> "none"],
    jobSt = [j \in Jobs |-> "addr"],
    jobWake = [j \in Jobs |-> FALSE],
    noteOrder = <<>>,
    \* In memory: the Bridge's request channel, and whether the Bridge runs.
    reqCh = <<>>,
    up = TRUE,
    shutdowns = 0,
    \* Ghosts for the witnesses.
    joined = FALSE,
    slipJoined = FALSE,
    woke = {};

define
    StandingOf(i) == IF i \in AmbInputs THEN "amb" ELSE "addr"
    Joins(i) == turn.on /\ turn.open /\ (StandingOf(i) = turn.s \/ Mutation = "steer-any")
    Pending == {j \in Jobs : job[j] = "pending"}
    WakingPending == {j \in Pending : jobWake[j]}
    EarliestWaking == noteOrder[CHOOSE n \in 1..Len(noteOrder) :
                          /\ noteOrder[n] \in WakingPending
                          /\ \A m \in 1..(n - 1) : noteOrder[m] \notin WakingPending]
    Claimable(s) == {j \in Pending : jobSt[j] = s \/ (Mutation = "drain-any" /\ ~jobWake[j])}
    WakeClaim(s) == IF Mutation = "mixed-wake" THEN Pending ELSE Claimable(s)
    WakeStanding(s, c) == IF Mutation = "mixed-wake"
                          THEN IF \E j \in c : jobSt[j] = "amb" THEN "amb" ELSE "addr"
                          ELSE s
    InputReq(i) == [k |-> "input", id |-> i, s |-> StandingOf(i)]
    IsInputReq(r) == r.k = "input"
    ReqIds(q) == [n \in 1..Len(q) |-> q[n].id]
    Queued(i) == Len(SelectSeq(waiting, LAMBDA x : x = i)) + Len(SelectSeq(reqCh, LAMBDA r : r.id = i))

    NoMixedTurn == turn.on => /\ \A i \in turn.ins : StandingOf(i) = turn.s
                              /\ \A j \in turn.notes : jobSt[j] = turn.s
    OneOwner == /\ \A i \in Inputs : /\ Queued(i) <= 1
                                     /\ inp[i] = "queued" <=> Queued(i) = 1
                                     /\ inp[i] = "inTurn" <=> (turn.on /\ i \in turn.ins)
                /\ \A j \in Jobs : job[j] = "claimed" <=> (turn.on /\ j \in turn.notes)

    InputsRun == \A i \in Inputs : inp[i] # "new" ~> inp[i] = "done"
    WakingNotesRun == \A j \in Jobs : (job[j] = "pending" /\ jobWake[j]) ~> job[j] = "done"
    NonWakingNotesRun == \A j \in Jobs : (job[j] = "pending" /\ ~jobWake[j]) ~> job[j] = "done"

    NoSteerJoins == ~joined
    NoSlipJoin == ~slipJoined
    NoTwoStandingWakes == woke # Standings
    NoLateNote == \A j \in Jobs : ~(job[j] \in {"claimed", "done"} /\ ~jobWake[j])
end define;

\* The Bridge: pickLaterWork, handle and activateInbound, then the running turn. Each branch is one atomic step.
fair process Lane = "lane"
begin
Run:
    while TRUE do
        await up;
        either
            \* pickLaterWork: the earliest waking note first, with its standing; otherwise the head of the Thread Queue.
            await ~turn.on /\ reqCh = <<>> /\ (WakingPending # {} \/ waiting # <<>>);
            if WakingPending # {} then
                reqCh := <<[k |-> "wake", id |-> 0, s |-> jobSt[EarliestWaking]]>>;
            else
                reqCh := <<InputReq(Head(waiting))>>;
                waiting := Tail(waiting);
            end if;
        or
            \* handle, activateInbound and claimWakeNotes: the turn starts with the request's standing.
            await ~turn.on /\ reqCh # <<>>;
            with r = Head(reqCh) do
                reqCh := Tail(reqCh);
                if r.k = "input" then
                    turn := [on |-> TRUE, s |-> r.s, open |-> TRUE, ins |-> {r.id}, notes |-> {}];
                    inp[r.id] := "inTurn";
                else
                    with c = WakeClaim(r.s) do
                        if \E j \in c : jobWake[j] then
                            turn := [on |-> TRUE, s |-> WakeStanding(r.s, c), open |-> TRUE, ins |-> {}, notes |-> c];
                            job := [j \in Jobs |-> IF j \in c THEN "claimed" ELSE job[j]];
                            woke := woke \cup {WakeStanding(r.s, c)};
                        end if;
                    end with;
                end if;
            end with;
        or
            \* Drain: the open turn claims the pending notes of its standing.
            await turn.on /\ turn.open /\ Claimable(turn.s) # {};
            with c = Claimable(turn.s) do
                turn.notes := turn.notes \cup c;
                job := [j \in Jobs |-> IF j \in c THEN "claimed" ELSE job[j]];
            end with;
        or
            \* A tool call starts a Background Job; its note will carry the turn's standing.
            await turn.on /\ turn.open /\ \E j \in Jobs : job[j] = "none";
            with j = CHOOSE x \in Jobs : job[x] = "none" /\ \A y \in Jobs : job[y] = "none" => x <= y do
                job[j] := "running";
                jobSt[j] := turn.s;
            end with;
        or
            \* The final Drain finds nothing new: the turn's input closes.
            await turn.on /\ turn.open /\ Claimable(turn.s) = {};
            turn.open := FALSE;
        or
            \* finish: the turn's inputs and notes are done.
            await turn.on /\ ~turn.open;
            inp := [i \in Inputs |-> IF i \in turn.ins THEN "done" ELSE inp[i]];
            job := [j \in Jobs |-> IF j \in turn.notes THEN "done" ELSE job[j]];
            turn := NoTurn;
        end either;
    end while;
end process;

\* An input arrives once: a steer that joins the open turn (enqueue, admitAgentWrite, or slipSteer while the Bridge
\* is stopped), or a prompt or a steer that does not join, into the request channel or the Thread Queue.
fair process Arrive = "arrive"
begin
Come:
    while TRUE do
        with i \in {x \in Inputs : inp[x] = "new"} do
            either
                await Joins(i);
                turn.ins := turn.ins \cup {i};
                inp[i] := "inTurn";
                joined := TRUE;
                slipJoined := slipJoined \/ ~up;
            or
                await up;
                reqCh := Append(reqCh, InputReq(i));
                inp[i] := "queued";
            or
                waiting := Append(waiting, i);
                inp[i] := "queued";
            end either;
        end with;
    end while;
end process;

\* A Background Job finishes: its note is waking (completed) or not (stopped or killed).
fair process JobRunner = "jobs"
begin
JobDone:
    while TRUE do
        with j \in {x \in Jobs : job[x] = "running"}, w \in BOOLEAN do
            job[j] := "pending";
            jobWake[j] := w;
            noteOrder := Append(noteOrder, j);
        end with;
    end while;
end process;

\* The Bridge stops: its waiting requests are stored in the Thread Queue, a wake request is dropped, the turn row stays.
process Shutdown = "shutdown"
begin
Stop:
    while TRUE do
        await up /\ shutdowns < MaxShutdowns;
        up := FALSE;
        shutdowns := shutdowns + 1;
        waiting := waiting \o ReqIds(SelectSeq(reqCh, IsInputReq));
        reqCh := <<>>;
    end while;
end process;

\* The restart resumes the running turn with its stored standing.
fair process Restart = "restart"
begin
Start:
    while TRUE do
        await ~up;
        up := TRUE;
    end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES inp, waiting, turn, job, jobSt, jobWake, noteOrder, reqCh, up, 
          shutdowns, joined, slipJoined, woke

(* define statement *)
StandingOf(i) == IF i \in AmbInputs THEN "amb" ELSE "addr"
Joins(i) == turn.on /\ turn.open /\ (StandingOf(i) = turn.s \/ Mutation = "steer-any")
Pending == {j \in Jobs : job[j] = "pending"}
WakingPending == {j \in Pending : jobWake[j]}
EarliestWaking == noteOrder[CHOOSE n \in 1..Len(noteOrder) :
                      /\ noteOrder[n] \in WakingPending
                      /\ \A m \in 1..(n - 1) : noteOrder[m] \notin WakingPending]
Claimable(s) == {j \in Pending : jobSt[j] = s \/ (Mutation = "drain-any" /\ ~jobWake[j])}
WakeClaim(s) == IF Mutation = "mixed-wake" THEN Pending ELSE Claimable(s)
WakeStanding(s, c) == IF Mutation = "mixed-wake"
                      THEN IF \E j \in c : jobSt[j] = "amb" THEN "amb" ELSE "addr"
                      ELSE s
InputReq(i) == [k |-> "input", id |-> i, s |-> StandingOf(i)]
IsInputReq(r) == r.k = "input"
ReqIds(q) == [n \in 1..Len(q) |-> q[n].id]
Queued(i) == Len(SelectSeq(waiting, LAMBDA x : x = i)) + Len(SelectSeq(reqCh, LAMBDA r : r.id = i))

NoMixedTurn == turn.on => /\ \A i \in turn.ins : StandingOf(i) = turn.s
                          /\ \A j \in turn.notes : jobSt[j] = turn.s
OneOwner == /\ \A i \in Inputs : /\ Queued(i) <= 1
                                 /\ inp[i] = "queued" <=> Queued(i) = 1
                                 /\ inp[i] = "inTurn" <=> (turn.on /\ i \in turn.ins)
            /\ \A j \in Jobs : job[j] = "claimed" <=> (turn.on /\ j \in turn.notes)

InputsRun == \A i \in Inputs : inp[i] # "new" ~> inp[i] = "done"
WakingNotesRun == \A j \in Jobs : (job[j] = "pending" /\ jobWake[j]) ~> job[j] = "done"
NonWakingNotesRun == \A j \in Jobs : (job[j] = "pending" /\ ~jobWake[j]) ~> job[j] = "done"

NoSteerJoins == ~joined
NoSlipJoin == ~slipJoined
NoTwoStandingWakes == woke # Standings
NoLateNote == \A j \in Jobs : ~(job[j] \in {"claimed", "done"} /\ ~jobWake[j])


vars == << inp, waiting, turn, job, jobSt, jobWake, noteOrder, reqCh, up, 
           shutdowns, joined, slipJoined, woke >>

ProcSet == {"lane"} \cup {"arrive"} \cup {"jobs"} \cup {"shutdown"} \cup {"restart"}

Init == (* Global variables *)
        /\ inp = [i \in Inputs |-> "new"]
        /\ waiting = <<>>
        /\ turn = NoTurn
        /\ job = [j \in Jobs |-> "none"]
        /\ jobSt = [j \in Jobs |-> "addr"]
        /\ jobWake = [j \in Jobs |-> FALSE]
        /\ noteOrder = <<>>
        /\ reqCh = <<>>
        /\ up = TRUE
        /\ shutdowns = 0
        /\ joined = FALSE
        /\ slipJoined = FALSE
        /\ woke = {}

Lane == /\ up
        /\ \/ /\ ~turn.on /\ reqCh = <<>> /\ (WakingPending # {} \/ waiting # <<>>)
              /\ IF WakingPending # {}
                    THEN /\ reqCh' = <<[k |-> "wake", id |-> 0, s |-> jobSt[EarliestWaking]]>>
                         /\ UNCHANGED waiting
                    ELSE /\ reqCh' = <<InputReq(Head(waiting))>>
                         /\ waiting' = Tail(waiting)
              /\ UNCHANGED <<inp, turn, job, jobSt, woke>>
           \/ /\ ~turn.on /\ reqCh # <<>>
              /\ LET r == Head(reqCh) IN
                   /\ reqCh' = Tail(reqCh)
                   /\ IF r.k = "input"
                         THEN /\ turn' = [on |-> TRUE, s |-> r.s, open |-> TRUE, ins |-> {r.id}, notes |-> {}]
                              /\ inp' = [inp EXCEPT ![r.id] = "inTurn"]
                              /\ UNCHANGED << job, woke >>
                         ELSE /\ LET c == WakeClaim(r.s) IN
                                   IF \E j \in c : jobWake[j]
                                      THEN /\ turn' = [on |-> TRUE, s |-> WakeStanding(r.s, c), open |-> TRUE, ins |-> {}, notes |-> c]
                                           /\ job' = [j \in Jobs |-> IF j \in c THEN "claimed" ELSE job[j]]
                                           /\ woke' = (woke \cup {WakeStanding(r.s, c)})
                                      ELSE /\ TRUE
                                           /\ UNCHANGED << turn, job, woke >>
                              /\ inp' = inp
              /\ UNCHANGED <<waiting, jobSt>>
           \/ /\ turn.on /\ turn.open /\ Claimable(turn.s) # {}
              /\ LET c == Claimable(turn.s) IN
                   /\ turn' = [turn EXCEPT !.notes = turn.notes \cup c]
                   /\ job' = [j \in Jobs |-> IF j \in c THEN "claimed" ELSE job[j]]
              /\ UNCHANGED <<inp, waiting, jobSt, reqCh, woke>>
           \/ /\ turn.on /\ turn.open /\ \E j \in Jobs : job[j] = "none"
              /\ LET j == CHOOSE x \in Jobs : job[x] = "none" /\ \A y \in Jobs : job[y] = "none" => x <= y IN
                   /\ job' = [job EXCEPT ![j] = "running"]
                   /\ jobSt' = [jobSt EXCEPT ![j] = turn.s]
              /\ UNCHANGED <<inp, waiting, turn, reqCh, woke>>
           \/ /\ turn.on /\ turn.open /\ Claimable(turn.s) = {}
              /\ turn' = [turn EXCEPT !.open = FALSE]
              /\ UNCHANGED <<inp, waiting, job, jobSt, reqCh, woke>>
           \/ /\ turn.on /\ ~turn.open
              /\ inp' = [i \in Inputs |-> IF i \in turn.ins THEN "done" ELSE inp[i]]
              /\ job' = [j \in Jobs |-> IF j \in turn.notes THEN "done" ELSE job[j]]
              /\ turn' = NoTurn
              /\ UNCHANGED <<waiting, jobSt, reqCh, woke>>
        /\ UNCHANGED << jobWake, noteOrder, up, shutdowns, joined, slipJoined >>

Arrive == /\ \E i \in {x \in Inputs : inp[x] = "new"}:
               \/ /\ Joins(i)
                  /\ turn' = [turn EXCEPT !.ins = turn.ins \cup {i}]
                  /\ inp' = [inp EXCEPT ![i] = "inTurn"]
                  /\ joined' = TRUE
                  /\ slipJoined' = (slipJoined \/ ~up)
                  /\ UNCHANGED <<waiting, reqCh>>
               \/ /\ up
                  /\ reqCh' = Append(reqCh, InputReq(i))
                  /\ inp' = [inp EXCEPT ![i] = "queued"]
                  /\ UNCHANGED <<waiting, turn, joined, slipJoined>>
               \/ /\ waiting' = Append(waiting, i)
                  /\ inp' = [inp EXCEPT ![i] = "queued"]
                  /\ UNCHANGED <<turn, reqCh, joined, slipJoined>>
          /\ UNCHANGED << job, jobSt, jobWake, noteOrder, up, shutdowns, woke >>

JobRunner == /\ \E j \in {x \in Jobs : job[x] = "running"}:
                  \E w \in BOOLEAN:
                    /\ job' = [job EXCEPT ![j] = "pending"]
                    /\ jobWake' = [jobWake EXCEPT ![j] = w]
                    /\ noteOrder' = Append(noteOrder, j)
             /\ UNCHANGED << inp, waiting, turn, jobSt, reqCh, up, shutdowns, 
                             joined, slipJoined, woke >>

Shutdown == /\ up /\ shutdowns < MaxShutdowns
            /\ up' = FALSE
            /\ shutdowns' = shutdowns + 1
            /\ waiting' = waiting \o ReqIds(SelectSeq(reqCh, IsInputReq))
            /\ reqCh' = <<>>
            /\ UNCHANGED << inp, turn, job, jobSt, jobWake, noteOrder, joined, 
                            slipJoined, woke >>

Restart == /\ ~up
           /\ up' = TRUE
           /\ UNCHANGED << inp, waiting, turn, job, jobSt, jobWake, noteOrder, 
                           reqCh, shutdowns, joined, slipJoined, woke >>

Next == Lane \/ Arrive \/ JobRunner \/ Shutdown \/ Restart

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Lane)
        /\ WF_vars(Arrive)
        /\ WF_vars(JobRunner)
        /\ WF_vars(Restart)

\* END TRANSLATION

=============================================================================
