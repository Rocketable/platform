---------------------------- MODULE CheckoutAlert ----------------------------
EXTENDS TLC

CONSTANTS CLASSIFIER, FOLDERS

RepoWithBranch == {"branch", "jjBookmark"}

GhView(f, l, hasPR) ==
    CASE f = "notRepo" -> IF l = "ok" THEN {"notRepo"} ELSE {"notRepo", "auth"}
      [] f = "detached" -> IF l = "ok" THEN {"noBranch"} ELSE {"noBranch", "auth"}
      [] l = "bad" -> {"auth"}
      [] hasPR -> {"pr"}
      [] OTHER -> {"noPR"}

Classify(out) ==
    CASE out = "pr" -> "pr"
      [] CLASSIFIER = "current" -> IF out = "noPR" THEN "none" ELSE "error"
      [] CLASSIFIER = "fixed" -> IF out \in {"noPR", "notRepo", "noBranch"} THEN "none" ELSE "error"
      [] CLASSIFIER = "broad" -> "none"

(* --algorithm CheckoutAlert
variables
    folder \in FOLDERS,
    login \in {"ok", "bad"},
    branchHasPR \in BOOLEAN,
    historyPR \in BOOLEAN,
    checkout = "pending",
    history = "pending",
    reader = "idle";

define
    AlertShown == checkout = "error"
    PRShown == reader = "read"
    AlertMeansLoginFailed == AlertShown => login = "bad"
    NoAlertBesideReadPR == ~(AlertShown /\ PRShown)
    LoginFailureAlerts == (checkout # "pending" /\ login = "bad" /\ folder \in RepoWithBranch) => AlertShown
end define;

fair process CheckoutLookup = "checkout"
variable out = "none";
begin
    RunGhPrView:
        with o \in GhView(folder, login, branchHasPR) do
            out := o;
        end with;
    ClassifyOutput:
        checkout := Classify(out);
end process;

fair process HistoryWalk = "history"
begin
    WalkSessions:
        history := "done";
end process;

fair process Reader = "reader"
begin
    ReadPR:
        await checkout # "pending" /\ history = "done";
        if historyPR \/ checkout = "pr" then
            reader := IF login = "ok" THEN "read" ELSE "failed";
        end if;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, folder, login, branchHasPR, historyPR, checkout, history, 
          reader

(* define statement *)
AlertShown == checkout = "error"
PRShown == reader = "read"
AlertMeansLoginFailed == AlertShown => login = "bad"
NoAlertBesideReadPR == ~(AlertShown /\ PRShown)
LoginFailureAlerts == (checkout # "pending" /\ login = "bad" /\ folder \in RepoWithBranch) => AlertShown

VARIABLE out

vars == << pc, folder, login, branchHasPR, historyPR, checkout, history, 
           reader, out >>

ProcSet == {"checkout"} \cup {"history"} \cup {"reader"}

Init == (* Global variables *)
        /\ folder \in FOLDERS
        /\ login \in {"ok", "bad"}
        /\ branchHasPR \in BOOLEAN
        /\ historyPR \in BOOLEAN
        /\ checkout = "pending"
        /\ history = "pending"
        /\ reader = "idle"
        (* Process CheckoutLookup *)
        /\ out = "none"
        /\ pc = [self \in ProcSet |-> CASE self = "checkout" -> "RunGhPrView"
                                        [] self = "history" -> "WalkSessions"
                                        [] self = "reader" -> "ReadPR"]

RunGhPrView == /\ pc["checkout"] = "RunGhPrView"
               /\ \E o \in GhView(folder, login, branchHasPR):
                    out' = o
               /\ pc' = [pc EXCEPT !["checkout"] = "ClassifyOutput"]
               /\ UNCHANGED << folder, login, branchHasPR, historyPR, checkout, 
                               history, reader >>

ClassifyOutput == /\ pc["checkout"] = "ClassifyOutput"
                  /\ checkout' = Classify(out)
                  /\ pc' = [pc EXCEPT !["checkout"] = "Done"]
                  /\ UNCHANGED << folder, login, branchHasPR, historyPR, 
                                  history, reader, out >>

CheckoutLookup == RunGhPrView \/ ClassifyOutput

WalkSessions == /\ pc["history"] = "WalkSessions"
                /\ history' = "done"
                /\ pc' = [pc EXCEPT !["history"] = "Done"]
                /\ UNCHANGED << folder, login, branchHasPR, historyPR, 
                                checkout, reader, out >>

HistoryWalk == WalkSessions

ReadPR == /\ pc["reader"] = "ReadPR"
          /\ checkout # "pending" /\ history = "done"
          /\ IF historyPR \/ checkout = "pr"
                THEN /\ reader' = (IF login = "ok" THEN "read" ELSE "failed")
                ELSE /\ TRUE
                     /\ UNCHANGED reader
          /\ pc' = [pc EXCEPT !["reader"] = "Done"]
          /\ UNCHANGED << folder, login, branchHasPR, historyPR, checkout, 
                          history, out >>

Reader == ReadPR

(* Allow infinite stuttering to prevent deadlock on termination. *)
Terminating == /\ \A self \in ProcSet: pc[self] = "Done"
               /\ UNCHANGED vars

Next == CheckoutLookup \/ HistoryWalk \/ Reader
           \/ Terminating

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(CheckoutLookup)
        /\ WF_vars(HistoryWalk)
        /\ WF_vars(Reader)

Termination == <>(\A self \in ProcSet: pc[self] = "Done")

\* END TRANSLATION
=============================================================================
