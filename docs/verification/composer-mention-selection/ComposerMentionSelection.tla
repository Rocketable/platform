---- MODULE ComposerMentionSelection ----
EXTENDS TLC

CONSTANT Synchronous

(* --algorithm ComposerMentionSelection {
  variables text = "query", selection = "end", tagged = FALSE, pending = FALSE;

  fair process (editor = "editor") {
    Accept:
      text := "accepted";
      tagged := TRUE;
      if (Synchronous) {
        selection := "after mention";
      } else {
        pending := TRUE;
      };
    SelectAll:
      selection := "all";
    Replace:
      if (selection = "all") {
        text := "replaced";
        tagged := FALSE;
      } else {
        text := "inserted";
      };
      selection := "end";
  }

  fair process (frame = "frame") {
    Restore:
      await text # "query";
      if (pending) {
        selection := "after mention";
        pending := FALSE;
      };
  }
} *)
\* BEGIN TRANSLATION (chksum(pcal) = "8b5c4792" /\ chksum(tla) = "2021ab1e")
VARIABLES pc, text, selection, tagged, pending

vars == << pc, text, selection, tagged, pending >>

ProcSet == {"editor"} \cup {"frame"}

Init ==
        /\ text = "query"
        /\ selection = "end"
        /\ tagged = FALSE
        /\ pending = FALSE
        /\ pc = [self \in ProcSet |-> CASE self = "editor" -> "Accept"
                                        [] self = "frame" -> "Restore"]

Accept == /\ pc["editor"] = "Accept"
          /\ text' = "accepted"
          /\ tagged' = TRUE
          /\ IF Synchronous
                THEN /\ selection' = "after mention"
                     /\ UNCHANGED pending
                ELSE /\ pending' = TRUE
                     /\ UNCHANGED selection
          /\ pc' = [pc EXCEPT !["editor"] = "SelectAll"]

SelectAll == /\ pc["editor"] = "SelectAll"
             /\ selection' = "all"
             /\ pc' = [pc EXCEPT !["editor"] = "Replace"]
             /\ UNCHANGED << text, tagged, pending >>

Replace == /\ pc["editor"] = "Replace"
           /\ IF selection = "all"
                 THEN /\ text' = "replaced"
                      /\ tagged' = FALSE
                 ELSE /\ text' = "inserted"
                      /\ UNCHANGED tagged
           /\ selection' = "end"
           /\ pc' = [pc EXCEPT !["editor"] = "Done"]
           /\ UNCHANGED pending

editor == Accept \/ SelectAll \/ Replace

Restore == /\ pc["frame"] = "Restore"
           /\ text # "query"
           /\ IF pending
                 THEN /\ selection' = "after mention"
                      /\ pending' = FALSE
                 ELSE /\ TRUE
                      /\ UNCHANGED << selection, pending >>
           /\ pc' = [pc EXCEPT !["frame"] = "Done"]
           /\ UNCHANGED << text, tagged >>

frame == Restore

Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == editor \/ frame
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(editor)
        /\ WF_vars(frame)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION 

ReadyAtMention == pc["editor"] = "SelectAll" => selection = "after mention"
OwnSelection == pc["editor"] = "Replace" => selection = "all"
ReplacementRemovesTag == pc["editor"] = "Done" => text = "replaced" /\ ~tagged
Finished == <>(pc["editor"] = "Done" /\ ~pending)
====
