------------------------ MODULE RecentListRefresh ------------------------
EXTENDS Integers

CONSTANT Mode

(*--algorithm RecentListRefresh
variables phase = "held", server = 0, lists = 0, overlap = FALSE,
          observedVisit = FALSE,
          ordinary = -1, reads = 0, stamp = 0, folded = FALSE,
          repair = -1, repairStamp = -1, preservedStamp = TRUE, superseded = FALSE, ordinaryWon = FALSE,
          refusedWon = FALSE,
          failed = FALSE, badWrite = FALSE;
begin
  Events:
    while TRUE do
      either
        await server < 2;
        server := server + 1;
      or
        await ordinary = -1 /\ reads < 2;
        ordinary := server;
        reads := reads + 1;
      or
        await ordinary # -1;
        stamp := stamp + 1;
        if phase \in {"held", "waiting"} then
          overlap := TRUE;
          observedVisit := observedVisit \/ ordinary > 0;
        end if;
        if phase = "waiting" then
          lists := 0;
          phase := IF Mode \in {"no-repair", "same-notify"} THEN "idle" ELSE "scheduled";
        else
          lists := ordinary;
          ordinaryWon := ordinaryWon \/ phase = "reading";
        end if;
        ordinary := -1;
      or
        await phase = "held";
        if ordinary # -1 then
          phase := "waiting";
        else
          lists := 0;
          phase := IF overlap /\ Mode # "no-repair" THEN "scheduled" ELSE "idle";
        end if;
      or
        await phase = "scheduled";
        phase := "reading";
        repair := server;
        repairStamp := stamp;
      or
        await ~folded;
        folded := TRUE;
        stamp := stamp + 1;
        ordinary := -1;
        if phase = "waiting" then
          lists := 0;
          phase := IF overlap /\ Mode # "no-repair" THEN "scheduled" ELSE "idle";
        elsif phase = "held" then
          overlap := TRUE;
        end if;
      or
        await ~superseded /\ phase \in {"scheduled", "reading"};
        superseded := TRUE;
        if phase = "scheduled" then
          phase := "idle";
        end if;
      or
        await phase = "reading" /\ ~refusedWon /\ server > repair;
        lists := server;
        stamp := stamp + 1;
        refusedWon := TRUE;
      or
        await phase = "reading";
        if (~ordinaryWon \/ Mode = "ignore-ordinary") /\ (~refusedWon \/ Mode = "ignore-refusal")
           /\ (~superseded \/ Mode = "ignore-mutation") then
          lists := repair;
          badWrite := ordinaryWon \/ refusedWon \/ superseded;
          with publishedStamp = IF Mode = "stale-stamp" THEN repairStamp ELSE stamp do
            preservedStamp := publishedStamp = stamp;
            stamp := publishedStamp;
          end with;
        end if;
        phase := "idle";
      or
        await phase = "reading";
        failed := TRUE;
        phase := "idle";
      or
        await phase = "idle";
        skip;
      end either;
    end while;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "d5712ef8" /\ chksum(tla) = "b961b0e3")
VARIABLES phase, server, lists, overlap, observedVisit, ordinary, reads, 
          stamp, folded, repair, repairStamp, preservedStamp, superseded, 
          ordinaryWon, refusedWon, failed, badWrite

vars == << phase, server, lists, overlap, observedVisit, ordinary, reads, 
           stamp, folded, repair, repairStamp, preservedStamp, superseded, 
           ordinaryWon, refusedWon, failed, badWrite >>

Init == (* Global variables *)
        /\ phase = "held"
        /\ server = 0
        /\ lists = 0
        /\ overlap = FALSE
        /\ observedVisit = FALSE
        /\ ordinary = -1
        /\ reads = 0
        /\ stamp = 0
        /\ folded = FALSE
        /\ repair = -1
        /\ repairStamp = -1
        /\ preservedStamp = TRUE
        /\ superseded = FALSE
        /\ ordinaryWon = FALSE
        /\ refusedWon = FALSE
        /\ failed = FALSE
        /\ badWrite = FALSE

Next == \/ /\ server < 2
           /\ server' = server + 1
           /\ UNCHANGED <<phase, lists, overlap, observedVisit, ordinary, reads, stamp, folded, repair, repairStamp, preservedStamp, superseded, ordinaryWon, refusedWon, failed, badWrite>>
        \/ /\ ordinary = -1 /\ reads < 2
           /\ ordinary' = server
           /\ reads' = reads + 1
           /\ UNCHANGED <<phase, server, lists, overlap, observedVisit, stamp, folded, repair, repairStamp, preservedStamp, superseded, ordinaryWon, refusedWon, failed, badWrite>>
        \/ /\ ordinary # -1
           /\ stamp' = stamp + 1
           /\ IF phase \in {"held", "waiting"}
                 THEN /\ overlap' = TRUE
                      /\ observedVisit' = (observedVisit \/ ordinary > 0)
                 ELSE /\ TRUE
                      /\ UNCHANGED << overlap, observedVisit >>
           /\ IF phase = "waiting"
                 THEN /\ lists' = 0
                      /\ phase' = (IF Mode \in {"no-repair", "same-notify"} THEN "idle" ELSE "scheduled")
                      /\ UNCHANGED ordinaryWon
                 ELSE /\ lists' = ordinary
                      /\ ordinaryWon' = (ordinaryWon \/ phase = "reading")
                      /\ phase' = phase
           /\ ordinary' = -1
           /\ UNCHANGED <<server, reads, folded, repair, repairStamp, preservedStamp, superseded, refusedWon, failed, badWrite>>
        \/ /\ phase = "held"
           /\ IF ordinary # -1
                 THEN /\ phase' = "waiting"
                      /\ lists' = lists
                 ELSE /\ lists' = 0
                      /\ phase' = (IF overlap /\ Mode # "no-repair" THEN "scheduled" ELSE "idle")
           /\ UNCHANGED <<server, overlap, observedVisit, ordinary, reads, stamp, folded, repair, repairStamp, preservedStamp, superseded, ordinaryWon, refusedWon, failed, badWrite>>
        \/ /\ phase = "scheduled"
           /\ phase' = "reading"
           /\ repair' = server
           /\ repairStamp' = stamp
           /\ UNCHANGED <<server, lists, overlap, observedVisit, ordinary, reads, stamp, folded, preservedStamp, superseded, ordinaryWon, refusedWon, failed, badWrite>>
        \/ /\ ~folded
           /\ folded' = TRUE
           /\ stamp' = stamp + 1
           /\ ordinary' = -1
           /\ IF phase = "waiting"
                 THEN /\ lists' = 0
                      /\ phase' = (IF overlap /\ Mode # "no-repair" THEN "scheduled" ELSE "idle")
                      /\ UNCHANGED overlap
                 ELSE /\ IF phase = "held"
                            THEN /\ overlap' = TRUE
                            ELSE /\ TRUE
                                 /\ UNCHANGED overlap
                      /\ UNCHANGED << phase, lists >>
           /\ UNCHANGED <<server, observedVisit, reads, repair, repairStamp, preservedStamp, superseded, ordinaryWon, refusedWon, failed, badWrite>>
        \/ /\ ~superseded /\ phase \in {"scheduled", "reading"}
           /\ superseded' = TRUE
           /\ IF phase = "scheduled"
                 THEN /\ phase' = "idle"
                 ELSE /\ TRUE
                      /\ phase' = phase
           /\ UNCHANGED <<server, lists, overlap, observedVisit, ordinary, reads, stamp, folded, repair, repairStamp, preservedStamp, ordinaryWon, refusedWon, failed, badWrite>>
        \/ /\ phase = "reading" /\ ~refusedWon /\ server > repair
           /\ lists' = server
           /\ stamp' = stamp + 1
           /\ refusedWon' = TRUE
           /\ UNCHANGED <<phase, server, overlap, observedVisit, ordinary, reads, folded, repair, repairStamp, preservedStamp, superseded, ordinaryWon, failed, badWrite>>
        \/ /\ phase = "reading"
           /\ IF (~ordinaryWon \/ Mode = "ignore-ordinary") /\ (~refusedWon \/ Mode = "ignore-refusal")
                 /\ (~superseded \/ Mode = "ignore-mutation")
                 THEN /\ lists' = repair
                      /\ badWrite' = (ordinaryWon \/ refusedWon \/ superseded)
                      /\ LET publishedStamp == IF Mode = "stale-stamp" THEN repairStamp ELSE stamp IN
                           /\ preservedStamp' = (publishedStamp = stamp)
                           /\ stamp' = publishedStamp
                 ELSE /\ TRUE
                      /\ UNCHANGED << lists, stamp, preservedStamp, 
                                      badWrite >>
           /\ phase' = "idle"
           /\ UNCHANGED <<server, overlap, observedVisit, ordinary, reads, folded, repair, repairStamp, superseded, ordinaryWon, refusedWon, failed>>
        \/ /\ phase = "reading"
           /\ failed' = TRUE
           /\ phase' = "idle"
           /\ UNCHANGED <<server, lists, overlap, observedVisit, ordinary, reads, stamp, folded, repair, repairStamp, preservedStamp, superseded, ordinaryWon, refusedWon, badWrite>>
        \/ /\ phase = "idle"
           /\ TRUE
           /\ UNCHANGED <<phase, server, lists, overlap, observedVisit, ordinary, reads, stamp, folded, repair, repairStamp, preservedStamp, superseded, ordinaryWon, refusedWon, failed, badWrite>>

Spec == Init /\ [][Next]_vars

\* END TRANSLATION

StampPreserved == preservedStamp

NoStaleRepair == ~badWrite
VisitRecovered == (phase = "idle" /\ observedVisit /\ ~superseded /\ ~failed) => lists > 0

=============================================================================
