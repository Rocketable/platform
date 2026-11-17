-------------------------- MODULE InputEvidence --------------------------
EXTENDS Naturals, Sequences, TLC

CONSTANTS MaxInputs, MaxCompactions, MaxRestarts,
          DropOnCompact, DropOnRestart, AliasSnapshot, CarryOnNextTurn

(* --algorithm InputEvidence
variables turn = 1, accepted = <<>>, evidence = <<>>, durable = <<>>,
          snapshot = <<>>, snapshotAtDispatch = <<>>, snapshotActive = FALSE,
          compactions = 0, restarts = 0;
begin
Run:
  while TRUE do
    either
      when Len(accepted) < MaxInputs;
      with event = <<turn, Len(accepted) + 1>> do
        accepted := Append(accepted, event);
        evidence := Append(evidence, event);
        durable := evidence;
        if AliasSnapshot /\ snapshotActive then
          snapshot := evidence;
        end if;
      end with;
    or
      when compactions < MaxCompactions;
      compactions := compactions + 1;
      if DropOnCompact then
        evidence := <<>>;
        durable := evidence;
      end if;
    or
      when restarts < MaxRestarts;
      restarts := restarts + 1;
      if DropOnRestart then
        evidence := <<>>;
      else
        evidence := durable;
      end if;
    or
      snapshot := evidence;
      snapshotAtDispatch := evidence;
      snapshotActive := TRUE;
    or
      when turn = 1;
      turn := 2;
      accepted := <<>>;
      if ~CarryOnNextTurn then
        evidence := <<>>;
        durable := <<>>;
      end if;
      snapshot := <<>>;
      snapshotAtDispatch := <<>>;
      snapshotActive := FALSE;
      compactions := 0;
      restarts := 0;
    end either;
  end while;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "389046c0" /\ chksum(tla) = "c32937f")
VARIABLES turn, accepted, evidence, durable, snapshot, snapshotAtDispatch, 
          snapshotActive, compactions, restarts

vars == << turn, accepted, evidence, durable, snapshot, snapshotAtDispatch, 
           snapshotActive, compactions, restarts >>

Init == (* Global variables *)
        /\ turn = 1
        /\ accepted = <<>>
        /\ evidence = <<>>
        /\ durable = <<>>
        /\ snapshot = <<>>
        /\ snapshotAtDispatch = <<>>
        /\ snapshotActive = FALSE
        /\ compactions = 0
        /\ restarts = 0

Next == \/ /\ Len(accepted) < MaxInputs
           /\ LET event == <<turn, Len(accepted) + 1>> IN
                /\ accepted' = Append(accepted, event)
                /\ evidence' = Append(evidence, event)
                /\ durable' = evidence'
                /\ IF AliasSnapshot /\ snapshotActive
                      THEN /\ snapshot' = evidence'
                      ELSE /\ TRUE
                           /\ UNCHANGED snapshot
           /\ UNCHANGED <<turn, snapshotAtDispatch, snapshotActive, compactions, restarts>>
        \/ /\ compactions < MaxCompactions
           /\ compactions' = compactions + 1
           /\ IF DropOnCompact
                 THEN /\ evidence' = <<>>
                      /\ durable' = evidence'
                 ELSE /\ TRUE
                      /\ UNCHANGED << evidence, durable >>
           /\ UNCHANGED <<turn, accepted, snapshot, snapshotAtDispatch, snapshotActive, restarts>>
        \/ /\ restarts < MaxRestarts
           /\ restarts' = restarts + 1
           /\ IF DropOnRestart
                 THEN /\ evidence' = <<>>
                 ELSE /\ evidence' = durable
           /\ UNCHANGED <<turn, accepted, durable, snapshot, snapshotAtDispatch, snapshotActive, compactions>>
        \/ /\ snapshot' = evidence
           /\ snapshotAtDispatch' = evidence
           /\ snapshotActive' = TRUE
           /\ UNCHANGED <<turn, accepted, evidence, durable, compactions, restarts>>
        \/ /\ turn = 1
           /\ turn' = 2
           /\ accepted' = <<>>
           /\ IF ~CarryOnNextTurn
                 THEN /\ evidence' = <<>>
                      /\ durable' = <<>>
                 ELSE /\ TRUE
                      /\ UNCHANGED << evidence, durable >>
           /\ snapshot' = <<>>
           /\ snapshotAtDispatch' = <<>>
           /\ snapshotActive' = FALSE
           /\ compactions' = 0
           /\ restarts' = 0

Spec == Init /\ [][Next]_vars

\* END TRANSLATION 

OrderedEvidence == evidence = accepted /\ durable = accepted
TurnIsolation == \A i \in 1..Len(evidence): evidence[i][1] = turn
SnapshotStable == snapshotActive => snapshot = snapshotAtDispatch

=============================================================================
