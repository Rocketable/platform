---- MODULE DraftPresence ----
EXTENDS TLC

CONSTANT Serialize
Clients == {"first", "second"}
Peer(client) == CHOOSE other \in Clients : other # client
Nobody == "none"

(* --algorithm DraftPresence
variables holder = Nobody,
          rows = [client \in Clients |-> Nobody],
          stale = Clients,
          waiting = {};

fair process Writer \in Clients
begin
Admit:
    if Serialize then
        await holder = Nobody;
        holder := self;
    end if;
Refresh:
    await rows[self] = Nobody;
    rows[self] := self;
Expire:
    if Peer(self) \in stale then
        waiting := waiting \cup {self};
    end if;
Finish:
    await self \notin waiting \/ rows[Peer(self)] = Nobody;
    stale := stale \ {self, Peer(self)};
    rows[self] := Nobody;
    waiting := waiting \ {self};
    if Serialize then
        holder := Nobody;
    end if;
end process;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "4d57f6ea" /\ chksum(tla) = "d0bbaa44")
VARIABLES pc, holder, rows, stale, waiting

vars == << pc, holder, rows, stale, waiting >>

ProcSet == (Clients)

Init == (* Global variables *)
        /\ holder = Nobody
        /\ rows = [client \in Clients |-> Nobody]
        /\ stale = Clients
        /\ waiting = {}
        /\ pc = [self \in ProcSet |-> "Admit"]

Admit(self) == /\ pc[self] = "Admit"
               /\ IF Serialize
                     THEN /\ holder = Nobody
                          /\ holder' = self
                     ELSE /\ TRUE
                          /\ UNCHANGED holder
               /\ pc' = [pc EXCEPT ![self] = "Refresh"]
               /\ UNCHANGED << rows, stale, waiting >>

Refresh(self) == /\ pc[self] = "Refresh"
                 /\ rows[self] = Nobody
                 /\ rows' = [rows EXCEPT ![self] = self]
                 /\ pc' = [pc EXCEPT ![self] = "Expire"]
                 /\ UNCHANGED << holder, stale, waiting >>

Expire(self) == /\ pc[self] = "Expire"
                /\ IF Peer(self) \in stale
                      THEN /\ waiting' = (waiting \cup {self})
                      ELSE /\ TRUE
                           /\ UNCHANGED waiting
                /\ pc' = [pc EXCEPT ![self] = "Finish"]
                /\ UNCHANGED << holder, rows, stale >>

Finish(self) == /\ pc[self] = "Finish"
                /\ self \notin waiting \/ rows[Peer(self)] = Nobody
                /\ stale' = stale \ {self, Peer(self)}
                /\ rows' = [rows EXCEPT ![self] = Nobody]
                /\ waiting' = waiting \ {self}
                /\ IF Serialize
                      THEN /\ holder' = Nobody
                      ELSE /\ TRUE
                           /\ UNCHANGED holder
                /\ pc' = [pc EXCEPT ![self] = "Done"]

Writer(self) == Admit(self) \/ Refresh(self) \/ Expire(self)
                   \/ Finish(self)

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == (\E self \in Clients: Writer(self))
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in Clients : WF_vars(Writer(self))

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION 

NoCycle ==
    ~(\A client \in Clients :
        client \in waiting /\ rows[Peer(client)] = Peer(client))

BothFinish == <>(\A client \in Clients : pc[client] = "Done")
====
