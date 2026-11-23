-------------------------- MODULE HostedPagePane --------------------------
EXTENDS Naturals, FiniteSets

CONSTANT Fault

Contexts == 1..4
Pages == 1..3
Page(c) == CASE c \in {1, 2} -> 1 [] c = 3 -> 2 [] OTHER -> 3
Empty == <<0, 0, 0>>
Frame(c, generation, version) == <<c, generation, version>>

(* --algorithm HostedPagePane
variables
    selection = 0, generation = 0,
    versions = [p \in Pages |-> 1], allowed = Pages, online = TRUE,
    shown = Empty, observed = Empty, remountedSame = FALSE,
    loadedUpdate = FALSE;

process Viewer = "viewer"
begin
Choose:
    while TRUE do
        await generation < 4;
        with next \in (Contexts \cup {0}) \ {selection} do
            selection := next;
            generation := generation + 1;
            shown := Empty;
            observed := Empty;
        end with;
    end while;
end process;

process Publisher = "publisher"
begin
Change:
    while TRUE do
        either
            with p \in {q \in Pages : versions[q] = 1} do
                versions[p] := 2;
            end with;
        or
            with p \in Pages do
                if p \in allowed then allowed := allowed \ {p};
                else allowed := allowed \cup {p}; end if;
            end with;
        or
            online := ~online;
        end either;
    end while;
end process;

fair process Reader = "reader"
variables captured = Empty, result = 0;
begin
Read:
    while TRUE do
        await selection # 0;
        captured := Frame(selection, generation, 0);
    Sample:
        result := IF ~online THEN 3 ELSE
            IF Page(captured[1]) \in allowed THEN versions[Page(captured[1])] ELSE 0;
    Complete:
        if (captured[1] = selection /\ captured[2] = generation) \/ Fault = "stale" then
            if result = 0 then
                observed := Empty;
                if Fault # "keep-absent" then shown := Empty; end if;
            elsif result \in 1..2 then
                observed := Frame(captured[1], captured[2], result);
                if shown = Frame(captured[1], captured[2], result) then
                    if Fault = "remount" then remountedSame := TRUE; end if;
                else
                    if shown = Frame(captured[1], captured[2], 1) /\ result = 2 then
                        loadedUpdate := TRUE;
                    end if;
                    shown := Frame(captured[1], captured[2], result);
                end if;
            elsif Fault = "clear-error" then
                shown := Empty;
            end if;
        end if;
    end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "c4db9236" /\ chksum(tla) = "6c56f97c")
VARIABLES pc, selection, generation, versions, allowed, online, shown, 
          observed, remountedSame, loadedUpdate, captured, result

vars == << pc, selection, generation, versions, allowed, online, shown, 
           observed, remountedSame, loadedUpdate, captured, result >>

ProcSet == {"viewer"} \cup {"publisher"} \cup {"reader"}

Init == (* Global variables *)
        /\ selection = 0
        /\ generation = 0
        /\ versions = [p \in Pages |-> 1]
        /\ allowed = Pages
        /\ online = TRUE
        /\ shown = Empty
        /\ observed = Empty
        /\ remountedSame = FALSE
        /\ loadedUpdate = FALSE
        (* Process Reader *)
        /\ captured = Empty
        /\ result = 0
        /\ pc = [self \in ProcSet |-> CASE self = "viewer" -> "Choose"
                                        [] self = "publisher" -> "Change"
                                        [] self = "reader" -> "Read"]

Choose == /\ pc["viewer"] = "Choose"
          /\ generation < 4
          /\ \E next \in (Contexts \cup {0}) \ {selection}:
               /\ selection' = next
               /\ generation' = generation + 1
               /\ shown' = Empty
               /\ observed' = Empty
          /\ pc' = [pc EXCEPT !["viewer"] = "Choose"]
          /\ UNCHANGED << versions, allowed, online, remountedSame, 
                          loadedUpdate, captured, result >>

Viewer == Choose

Change == /\ pc["publisher"] = "Change"
          /\ \/ /\ \E p \in {q \in Pages : versions[q] = 1}:
                     versions' = [versions EXCEPT ![p] = 2]
                /\ UNCHANGED <<allowed, online>>
             \/ /\ \E p \in Pages:
                     IF p \in allowed
                        THEN /\ allowed' = allowed \ {p}
                        ELSE /\ allowed' = (allowed \cup {p})
                /\ UNCHANGED <<versions, online>>
             \/ /\ online' = ~online
                /\ UNCHANGED <<versions, allowed>>
          /\ pc' = [pc EXCEPT !["publisher"] = "Change"]
          /\ UNCHANGED << selection, generation, shown, observed, 
                          remountedSame, loadedUpdate, captured, result >>

Publisher == Change

Read == /\ pc["reader"] = "Read"
        /\ selection # 0
        /\ captured' = Frame(selection, generation, 0)
        /\ pc' = [pc EXCEPT !["reader"] = "Sample"]
        /\ UNCHANGED << selection, generation, versions, allowed, online, 
                        shown, observed, remountedSame, loadedUpdate, result >>

Sample == /\ pc["reader"] = "Sample"
          /\ result' =       IF ~online THEN 3 ELSE
                       IF Page(captured[1]) \in allowed THEN versions[Page(captured[1])] ELSE 0
          /\ pc' = [pc EXCEPT !["reader"] = "Complete"]
          /\ UNCHANGED << selection, generation, versions, allowed, online, 
                          shown, observed, remountedSame, loadedUpdate, 
                          captured >>

Complete == /\ pc["reader"] = "Complete"
            /\ IF (captured[1] = selection /\ captured[2] = generation) \/ Fault = "stale"
                  THEN /\ IF result = 0
                             THEN /\ observed' = Empty
                                  /\ IF Fault # "keep-absent"
                                        THEN /\ shown' = Empty
                                        ELSE /\ TRUE
                                             /\ shown' = shown
                                  /\ UNCHANGED << remountedSame, loadedUpdate >>
                             ELSE /\ IF result \in 1..2
                                        THEN /\ observed' = Frame(captured[1], captured[2], result)
                                             /\ IF shown = Frame(captured[1], captured[2], result)
                                                   THEN /\ IF Fault = "remount"
                                                              THEN /\ remountedSame' = TRUE
                                                              ELSE /\ TRUE
                                                                   /\ UNCHANGED remountedSame
                                                        /\ UNCHANGED << shown, 
                                                                        loadedUpdate >>
                                                   ELSE /\ IF shown = Frame(captured[1], captured[2], 1) /\ result = 2
                                                              THEN /\ loadedUpdate' = TRUE
                                                              ELSE /\ TRUE
                                                                   /\ UNCHANGED loadedUpdate
                                                        /\ shown' = Frame(captured[1], captured[2], result)
                                                        /\ UNCHANGED remountedSame
                                        ELSE /\ IF Fault = "clear-error"
                                                   THEN /\ shown' = Empty
                                                   ELSE /\ TRUE
                                                        /\ shown' = shown
                                             /\ UNCHANGED << observed, 
                                                             remountedSame, 
                                                             loadedUpdate >>
                  ELSE /\ TRUE
                       /\ UNCHANGED << shown, observed, remountedSame, 
                                       loadedUpdate >>
            /\ pc' = [pc EXCEPT !["reader"] = "Read"]
            /\ UNCHANGED << selection, generation, versions, allowed, online, 
                            captured, result >>

Reader == Read \/ Sample \/ Complete

Next == Viewer \/ Publisher \/ Reader

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Reader)

\* END TRANSLATION 

CurrentSelection == shown = Empty \/ (shown[1] = selection /\ shown[2] = generation)
LastSuccess == shown = observed
NoRedundantMount == ~remountedSame
NeverLoadedUpdate == ~loadedUpdate

FreshWhenStable ==
    \A c \in Contexts, v \in 1..2 :
        (<>[](selection = c /\ online /\ Page(c) \in allowed /\ versions[Page(c)] = v))
        => <>[](shown = Frame(c, generation, v))

=============================================================================
