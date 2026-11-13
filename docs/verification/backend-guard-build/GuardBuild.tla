----------------------------- MODULE GuardBuild -----------------------------
EXTENDS Naturals, FiniteSets

CONSTANTS Guards, LockBuild, WaitForGuards

(* --algorithm GuardBuild
variables directory = TRUE, ready = FALSE, owner = 0,
          successfulBuilds = 0, activeChildren = {}, finished = {},
          usedMissingBinary = FALSE, removedDuringUse = FALSE;

fair process Guard \in Guards
variables found = FALSE;
begin
Acquire:
    await ~LockBuild \/ owner = 0;
    if LockBuild then owner := self; end if;
Inspect:
    found := ready;
Build:
    if ~found then
        either
            ready := TRUE;
            successfulBuilds := successfulBuilds + 1;
        or
            goto Failed;
        end either;
    end if;
Release:
    if LockBuild then owner := 0; end if;
StartChild:
    usedMissingBinary := usedMissingBinary \/ ~ready \/ ~directory;
    activeChildren := activeChildren \cup {self};
FinishChild:
    activeChildren := activeChildren \ {self};
Finish:
    finished := finished \cup {self};
    goto Done;
Failed:
    if LockBuild then owner := 0; end if;
    finished := finished \cup {self};
end process;

fair process Main = 0
begin
Cleanup:
    await IF WaitForGuards THEN finished = Guards ELSE ready;
    removedDuringUse := activeChildren # {} \/ finished # Guards;
    directory := FALSE;
end process;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "4311234d" /\ chksum(tla) = "893ef7be")
VARIABLES pc, directory, ready, owner, successfulBuilds, activeChildren, 
          finished, usedMissingBinary, removedDuringUse, found

vars == << pc, directory, ready, owner, successfulBuilds, activeChildren, 
           finished, usedMissingBinary, removedDuringUse, found >>

ProcSet == (Guards) \cup {0}

Init == (* Global variables *)
        /\ directory = TRUE
        /\ ready = FALSE
        /\ owner = 0
        /\ successfulBuilds = 0
        /\ activeChildren = {}
        /\ finished = {}
        /\ usedMissingBinary = FALSE
        /\ removedDuringUse = FALSE
        (* Process Guard *)
        /\ found = [self \in Guards |-> FALSE]
        /\ pc = [self \in ProcSet |-> CASE self \in Guards -> "Acquire"
                                        [] self = 0 -> "Cleanup"]

Acquire(self) == /\ pc[self] = "Acquire"
                 /\ ~LockBuild \/ owner = 0
                 /\ IF LockBuild
                       THEN /\ owner' = self
                       ELSE /\ TRUE
                            /\ owner' = owner
                 /\ pc' = [pc EXCEPT ![self] = "Inspect"]
                 /\ UNCHANGED << directory, ready, successfulBuilds, 
                                 activeChildren, finished, usedMissingBinary, 
                                 removedDuringUse, found >>

Inspect(self) == /\ pc[self] = "Inspect"
                 /\ found' = [found EXCEPT ![self] = ready]
                 /\ pc' = [pc EXCEPT ![self] = "Build"]
                 /\ UNCHANGED << directory, ready, owner, successfulBuilds, 
                                 activeChildren, finished, usedMissingBinary, 
                                 removedDuringUse >>

Build(self) == /\ pc[self] = "Build"
               /\ IF ~found[self]
                     THEN /\ \/ /\ ready' = TRUE
                                /\ successfulBuilds' = successfulBuilds + 1
                                /\ pc' = [pc EXCEPT ![self] = "Release"]
                             \/ /\ pc' = [pc EXCEPT ![self] = "Failed"]
                                /\ UNCHANGED <<ready, successfulBuilds>>
                     ELSE /\ pc' = [pc EXCEPT ![self] = "Release"]
                          /\ UNCHANGED << ready, successfulBuilds >>
               /\ UNCHANGED << directory, owner, activeChildren, finished, 
                               usedMissingBinary, removedDuringUse, found >>

Release(self) == /\ pc[self] = "Release"
                 /\ IF LockBuild
                       THEN /\ owner' = 0
                       ELSE /\ TRUE
                            /\ owner' = owner
                 /\ pc' = [pc EXCEPT ![self] = "StartChild"]
                 /\ UNCHANGED << directory, ready, successfulBuilds, 
                                 activeChildren, finished, usedMissingBinary, 
                                 removedDuringUse, found >>

StartChild(self) == /\ pc[self] = "StartChild"
                    /\ usedMissingBinary' = (usedMissingBinary \/ ~ready \/ ~directory)
                    /\ activeChildren' = (activeChildren \cup {self})
                    /\ pc' = [pc EXCEPT ![self] = "FinishChild"]
                    /\ UNCHANGED << directory, ready, owner, successfulBuilds, 
                                    finished, removedDuringUse, found >>

FinishChild(self) == /\ pc[self] = "FinishChild"
                     /\ activeChildren' = activeChildren \ {self}
                     /\ pc' = [pc EXCEPT ![self] = "Finish"]
                     /\ UNCHANGED << directory, ready, owner, successfulBuilds, 
                                     finished, usedMissingBinary, 
                                     removedDuringUse, found >>

Finish(self) == /\ pc[self] = "Finish"
                /\ finished' = (finished \cup {self})
                /\ pc' = [pc EXCEPT ![self] = "Done"]
                /\ UNCHANGED << directory, ready, owner, successfulBuilds, 
                                activeChildren, usedMissingBinary, 
                                removedDuringUse, found >>

Failed(self) == /\ pc[self] = "Failed"
                /\ IF LockBuild
                      THEN /\ owner' = 0
                      ELSE /\ TRUE
                           /\ owner' = owner
                /\ finished' = (finished \cup {self})
                /\ pc' = [pc EXCEPT ![self] = "Done"]
                /\ UNCHANGED << directory, ready, successfulBuilds, 
                                activeChildren, usedMissingBinary, 
                                removedDuringUse, found >>

Guard(self) == Acquire(self) \/ Inspect(self) \/ Build(self)
                  \/ Release(self) \/ StartChild(self) \/ FinishChild(self)
                  \/ Finish(self) \/ Failed(self)

Cleanup == /\ pc[0] = "Cleanup"
           /\ IF WaitForGuards THEN finished = Guards ELSE ready
           /\ removedDuringUse' = (activeChildren # {} \/ finished # Guards)
           /\ directory' = FALSE
           /\ pc' = [pc EXCEPT ![0] = "Done"]
           /\ UNCHANGED << ready, owner, successfulBuilds, activeChildren, 
                           finished, usedMissingBinary, found >>

Main == Cleanup

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == Main
           \/ (\E self \in Guards: Guard(self))
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ \A self \in Guards : WF_vars(Guard(self))
        /\ WF_vars(Main)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION 

AtMostOneSuccessfulBuild == successfulBuilds <= 1
ChildrenUsePublishedBinary == ~usedMissingBinary
CleanupAfterUse == ~removedDuringUse
EventuallyCleaned == <>~directory
=============================================================================
