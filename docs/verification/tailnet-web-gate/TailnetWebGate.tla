----------------------------- MODULE TailnetWebGate -----------------------------
EXTENDS Naturals, Sequences

CONSTANTS Design, MaxFailures, Procs

ASSUME Design \in {"current", "gatefirst", "fixed"}
ASSUME 0 \notin Procs

Order == CASE Design = "current" -> <<"migrate", "lock", "rest">>
           [] Design = "gatefirst" -> <<"gate", "migrate", "lock", "rest">>
           [] Design = "fixed" -> <<"lock", "gate", "migrate", "rest">>

(* --algorithm TailnetWebGate
variables
    lasting \in {"none", "login", "https"},
    holder = 0,
    phase = [p \in Procs |-> "starting"],
    at = [p \in Procs |-> 1],
    gatePassed = [p \in Procs |-> FALSE],
    nodeUp = [p \in Procs |-> FALSE],
    others = [p \in Procs |-> FALSE],
    web = [p \in Procs |-> "none"],
    shutdown = [p \in Procs |-> FALSE],
    restart = [p \in Procs |-> FALSE],
    closing = [p \in Procs |-> FALSE],
    exit = [p \in Procs |-> "none"],
    firstStop = [p \in Procs |-> "none"],
    failuresLeft = MaxFailures,
    signaled = FALSE,
    restarted = FALSE,
    refused = FALSE;

define
    Step(p) == Order[at[p]]
    Starting(p, s) == phase[p] = "starting" /\ Step(p) = s
    TypeOK == /\ lasting \in {"none", "login", "https"}
              /\ holder \in Procs \cup {0}
              /\ \A p \in Procs : /\ phase[p] \in {"starting", "running", "stopping", "exited"}
                                  /\ at[p] \in 1..Len(Order)
                                  /\ web[p] \in {"none", "serving", "failed", "login", "off"}
                                  /\ exit[p] \in {"none", "0", "1", "255"}
                                  /\ firstStop[p] \in {"none", "signal", "restart"}
              /\ failuresLeft \in 0..MaxFailures
    LockFirst == \A p \in Procs : (nodeUp[p] \/ others[p] \/ Starting(p, "migrate")) => holder = p
    NoMigrateUnderOther == \A p \in Procs : Starting(p, "migrate") => holder \in {0, p}
    GateFirst == \A p \in Procs : (others[p] \/ Starting(p, "migrate")) => gatePassed[p]
    GateFailureReleases == \A p \in Procs : exit[p] = "1" => holder # p
    NoRestartForLastingCause == \A p \in Procs : exit[p] = "255" => lasting = "none"
    StopSignalStops == \A p \in Procs : (phase[p] = "exited" /\ firstStop[p] = "signal") => exit[p] = "0"
    WebComesBack == (lasting = "none" /\ ~signaled /\ \E p \in Procs : phase[p] = "running")
                        ~> ((\E p \in Procs : web[p] = "serving") \/ signaled)
    LastingStops == (lasting # "none") => <>(\A p \in Procs : phase[p] = "exited" /\ exit[p] \in {"0", "1"})
    NeverRestarted == ~restarted
    NeverServesAfterRestart == ~(restarted /\ \E p \in Procs : web[p] = "serving")
    NeverGateFailure == \A p \in Procs : exit[p] # "1"
    NeverRestartRefused == ~refused
    NeverWaits == ~\E p \in Procs : Starting(p, "lock") /\ holder \notin {0, p}
end define;

macro RequestStop(p, who) begin
    if ~shutdown[p] then
        shutdown[p] := TRUE;
        restart[p] := who = "restart";
        firstStop[p] := who;
    end if;
end macro;

macro Exit(p, code) begin
    phase[p] := "exited";
    exit[p] := code;
    nodeUp[p] := FALSE;
    others[p] := FALSE;
    web[p] := "none";
    if holder = p then
        holder := 0;
    end if;
end macro;

fair process server \in Procs
begin
Run:
    while TRUE do
        either
            await Starting(self, "lock") /\ holder = 0;
            holder := self;
            at[self] := at[self] + 1;
        or
            await Starting(self, "gate");
            if lasting = "none" then
                gatePassed[self] := TRUE;
                nodeUp[self] := TRUE;
                at[self] := at[self] + 1;
            else
                Exit(self, "1");
            end if;
        or
            await Starting(self, "migrate");
            at[self] := at[self] + 1;
        or
            await Starting(self, "rest");
            others[self] := TRUE;
            nodeUp[self] := TRUE;
            phase[self] := "running";
            if Design # "current" \/ lasting = "none" then
                web[self] := "serving";
            elsif lasting = "login" then
                web[self] := "login";
            else
                web[self] := "failed";
            end if;
        or
            await web[self] = "serving" /\ ~closing[self] /\ failuresLeft > 0;
            web[self] := "failed";
            failuresLeft := failuresLeft - 1;
        or
            await web[self] = "failed";
            if Design # "current" then
                refused := refused \/ shutdown[self];
                RequestStop(self, "restart");
            end if;
            web[self] := "off";
        or
            await phase[self] = "running" /\ shutdown[self];
            phase[self] := "stopping";
        or
            await phase[self] = "stopping" /\ ~closing[self];
            closing[self] := TRUE;
        or
            await web[self] = "serving" /\ closing[self];
            web[self] := "off";
        or
            await phase[self] = "stopping" /\ closing[self] /\ web[self] \in {"none", "login", "off"};
            Exit(self, IF restart[self] THEN "255" ELSE "0");
        or
            await phase[self] = "exited" /\ exit[self] = "255";
            phase[self] := "starting";
            at[self] := 1;
            gatePassed[self] := FALSE;
            shutdown[self] := FALSE;
            restart[self] := FALSE;
            closing[self] := FALSE;
            exit[self] := "none";
            firstStop[self] := "none";
            restarted := TRUE;
        end either;
    end while;
end process;

process operator = 0
begin
Signal:
    with p \in {q \in Procs : phase[q] = "running"} do
        signaled := TRUE;
        RequestStop(p, "signal");
    end with;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, lasting, holder, phase, at, gatePassed, nodeUp, others, web, 
          shutdown, restart, closing, exit, firstStop, failuresLeft, signaled, 
          restarted, refused

(* define statement *)
Step(p) == Order[at[p]]
Starting(p, s) == phase[p] = "starting" /\ Step(p) = s
TypeOK == /\ lasting \in {"none", "login", "https"}
          /\ holder \in Procs \cup {0}
          /\ \A p \in Procs : /\ phase[p] \in {"starting", "running", "stopping", "exited"}
                              /\ at[p] \in 1..Len(Order)
                              /\ web[p] \in {"none", "serving", "failed", "login", "off"}
                              /\ exit[p] \in {"none", "0", "1", "255"}
                              /\ firstStop[p] \in {"none", "signal", "restart"}
          /\ failuresLeft \in 0..MaxFailures
LockFirst == \A p \in Procs : (nodeUp[p] \/ others[p] \/ Starting(p, "migrate")) => holder = p
NoMigrateUnderOther == \A p \in Procs : Starting(p, "migrate") => holder \in {0, p}
GateFirst == \A p \in Procs : (others[p] \/ Starting(p, "migrate")) => gatePassed[p]
GateFailureReleases == \A p \in Procs : exit[p] = "1" => holder # p
NoRestartForLastingCause == \A p \in Procs : exit[p] = "255" => lasting = "none"
StopSignalStops == \A p \in Procs : (phase[p] = "exited" /\ firstStop[p] = "signal") => exit[p] = "0"
WebComesBack == (lasting = "none" /\ ~signaled /\ \E p \in Procs : phase[p] = "running")
                    ~> ((\E p \in Procs : web[p] = "serving") \/ signaled)
LastingStops == (lasting # "none") => <>(\A p \in Procs : phase[p] = "exited" /\ exit[p] \in {"0", "1"})
NeverRestarted == ~restarted
NeverServesAfterRestart == ~(restarted /\ \E p \in Procs : web[p] = "serving")
NeverGateFailure == \A p \in Procs : exit[p] # "1"
NeverRestartRefused == ~refused
NeverWaits == ~\E p \in Procs : Starting(p, "lock") /\ holder \notin {0, p}


vars == << pc, lasting, holder, phase, at, gatePassed, nodeUp, others, web, 
           shutdown, restart, closing, exit, firstStop, failuresLeft, 
           signaled, restarted, refused >>

ProcSet == (Procs) \cup {0}

Init == (* Global variables *)
        /\ lasting \in {"none", "login", "https"}
        /\ holder = 0
        /\ phase = [p \in Procs |-> "starting"]
        /\ at = [p \in Procs |-> 1]
        /\ gatePassed = [p \in Procs |-> FALSE]
        /\ nodeUp = [p \in Procs |-> FALSE]
        /\ others = [p \in Procs |-> FALSE]
        /\ web = [p \in Procs |-> "none"]
        /\ shutdown = [p \in Procs |-> FALSE]
        /\ restart = [p \in Procs |-> FALSE]
        /\ closing = [p \in Procs |-> FALSE]
        /\ exit = [p \in Procs |-> "none"]
        /\ firstStop = [p \in Procs |-> "none"]
        /\ failuresLeft = MaxFailures
        /\ signaled = FALSE
        /\ restarted = FALSE
        /\ refused = FALSE
        /\ pc = [self \in ProcSet |-> CASE self \in Procs -> "Run"
                                        [] self = 0 -> "Signal"]

Run(self) == /\ pc[self] = "Run"
             /\ \/ /\ Starting(self, "lock") /\ holder = 0
                   /\ holder' = self
                   /\ at' = [at EXCEPT ![self] = at[self] + 1]
                   /\ UNCHANGED <<phase, gatePassed, nodeUp, others, web, shutdown, restart, closing, exit, firstStop, failuresLeft, restarted, refused>>
                \/ /\ Starting(self, "gate")
                   /\ IF lasting = "none"
                         THEN /\ gatePassed' = [gatePassed EXCEPT ![self] = TRUE]
                              /\ nodeUp' = [nodeUp EXCEPT ![self] = TRUE]
                              /\ at' = [at EXCEPT ![self] = at[self] + 1]
                              /\ UNCHANGED << holder, phase, others, web, exit >>
                         ELSE /\ phase' = [phase EXCEPT ![self] = "exited"]
                              /\ exit' = [exit EXCEPT ![self] = "1"]
                              /\ nodeUp' = [nodeUp EXCEPT ![self] = FALSE]
                              /\ others' = [others EXCEPT ![self] = FALSE]
                              /\ web' = [web EXCEPT ![self] = "none"]
                              /\ IF holder = self
                                    THEN /\ holder' = 0
                                    ELSE /\ TRUE
                                         /\ UNCHANGED holder
                              /\ UNCHANGED << at, gatePassed >>
                   /\ UNCHANGED <<shutdown, restart, closing, firstStop, failuresLeft, restarted, refused>>
                \/ /\ Starting(self, "migrate")
                   /\ at' = [at EXCEPT ![self] = at[self] + 1]
                   /\ UNCHANGED <<holder, phase, gatePassed, nodeUp, others, web, shutdown, restart, closing, exit, firstStop, failuresLeft, restarted, refused>>
                \/ /\ Starting(self, "rest")
                   /\ others' = [others EXCEPT ![self] = TRUE]
                   /\ nodeUp' = [nodeUp EXCEPT ![self] = TRUE]
                   /\ phase' = [phase EXCEPT ![self] = "running"]
                   /\ IF Design # "current" \/ lasting = "none"
                         THEN /\ web' = [web EXCEPT ![self] = "serving"]
                         ELSE /\ IF lasting = "login"
                                    THEN /\ web' = [web EXCEPT ![self] = "login"]
                                    ELSE /\ web' = [web EXCEPT ![self] = "failed"]
                   /\ UNCHANGED <<holder, at, gatePassed, shutdown, restart, closing, exit, firstStop, failuresLeft, restarted, refused>>
                \/ /\ web[self] = "serving" /\ ~closing[self] /\ failuresLeft > 0
                   /\ web' = [web EXCEPT ![self] = "failed"]
                   /\ failuresLeft' = failuresLeft - 1
                   /\ UNCHANGED <<holder, phase, at, gatePassed, nodeUp, others, shutdown, restart, closing, exit, firstStop, restarted, refused>>
                \/ /\ web[self] = "failed"
                   /\ IF Design # "current"
                         THEN /\ refused' = (refused \/ shutdown[self])
                              /\ IF ~shutdown[self]
                                    THEN /\ shutdown' = [shutdown EXCEPT ![self] = TRUE]
                                         /\ restart' = [restart EXCEPT ![self] = "restart" = "restart"]
                                         /\ firstStop' = [firstStop EXCEPT ![self] = "restart"]
                                    ELSE /\ TRUE
                                         /\ UNCHANGED << shutdown, restart, 
                                                         firstStop >>
                         ELSE /\ TRUE
                              /\ UNCHANGED << shutdown, restart, firstStop, 
                                              refused >>
                   /\ web' = [web EXCEPT ![self] = "off"]
                   /\ UNCHANGED <<holder, phase, at, gatePassed, nodeUp, others, closing, exit, failuresLeft, restarted>>
                \/ /\ phase[self] = "running" /\ shutdown[self]
                   /\ phase' = [phase EXCEPT ![self] = "stopping"]
                   /\ UNCHANGED <<holder, at, gatePassed, nodeUp, others, web, shutdown, restart, closing, exit, firstStop, failuresLeft, restarted, refused>>
                \/ /\ phase[self] = "stopping" /\ ~closing[self]
                   /\ closing' = [closing EXCEPT ![self] = TRUE]
                   /\ UNCHANGED <<holder, phase, at, gatePassed, nodeUp, others, web, shutdown, restart, exit, firstStop, failuresLeft, restarted, refused>>
                \/ /\ web[self] = "serving" /\ closing[self]
                   /\ web' = [web EXCEPT ![self] = "off"]
                   /\ UNCHANGED <<holder, phase, at, gatePassed, nodeUp, others, shutdown, restart, closing, exit, firstStop, failuresLeft, restarted, refused>>
                \/ /\ phase[self] = "stopping" /\ closing[self] /\ web[self] \in {"none", "login", "off"}
                   /\ phase' = [phase EXCEPT ![self] = "exited"]
                   /\ exit' = [exit EXCEPT ![self] = IF restart[self] THEN "255" ELSE "0"]
                   /\ nodeUp' = [nodeUp EXCEPT ![self] = FALSE]
                   /\ others' = [others EXCEPT ![self] = FALSE]
                   /\ web' = [web EXCEPT ![self] = "none"]
                   /\ IF holder = self
                         THEN /\ holder' = 0
                         ELSE /\ TRUE
                              /\ UNCHANGED holder
                   /\ UNCHANGED <<at, gatePassed, shutdown, restart, closing, firstStop, failuresLeft, restarted, refused>>
                \/ /\ phase[self] = "exited" /\ exit[self] = "255"
                   /\ phase' = [phase EXCEPT ![self] = "starting"]
                   /\ at' = [at EXCEPT ![self] = 1]
                   /\ gatePassed' = [gatePassed EXCEPT ![self] = FALSE]
                   /\ shutdown' = [shutdown EXCEPT ![self] = FALSE]
                   /\ restart' = [restart EXCEPT ![self] = FALSE]
                   /\ closing' = [closing EXCEPT ![self] = FALSE]
                   /\ exit' = [exit EXCEPT ![self] = "none"]
                   /\ firstStop' = [firstStop EXCEPT ![self] = "none"]
                   /\ restarted' = TRUE
                   /\ UNCHANGED <<holder, nodeUp, others, web, failuresLeft, refused>>
             /\ pc' = [pc EXCEPT ![self] = "Run"]
             /\ UNCHANGED << lasting, signaled >>

server(self) == Run(self)

Signal == /\ pc[0] = "Signal"
          /\ \E p \in {q \in Procs : phase[q] = "running"}:
               /\ signaled' = TRUE
               /\ IF ~shutdown[p]
                     THEN /\ shutdown' = [shutdown EXCEPT ![p] = TRUE]
                          /\ restart' = [restart EXCEPT ![p] = "signal" = "restart"]
                          /\ firstStop' = [firstStop EXCEPT ![p] = "signal"]
                     ELSE /\ TRUE
                          /\ UNCHANGED << shutdown, restart, firstStop >>
          /\ pc' = [pc EXCEPT ![0] = "Done"]
          /\ UNCHANGED << lasting, holder, phase, at, gatePassed, nodeUp, 
                          others, web, closing, exit, failuresLeft, restarted, 
                          refused >>

operator == Signal

Next == operator
           \/ (\E self \in Procs: server(self))

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in Procs : WF_vars(server(self))

\* END TRANSLATION
=============================================================================
