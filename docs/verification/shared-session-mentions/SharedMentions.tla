---------------------------- MODULE SharedMentions ----------------------------
EXTENDS FiniteSets

CONSTANT Mode

Messages == {"native", "remote"}
People == {"selected", "other"}

(* --algorithm SharedMentions
variables
    phase = [m \in Messages |-> "draft"],
    shared = TRUE,
    access = People,
    unread = {},
    observed = {},
    premature = FALSE;
define
    Eligible == shared /\ "selected" \in access
    Exposed == IF Eligible \/ Mode = "stale-view" THEN unread ELSE {}
    MarkAfterAdmission == ~premature
    MarkOnlySelected == \A mark \in unread : mark[2] = "selected"
    ViewRequiresAccess == Exposed = {} \/ Eligible
    ActivityKeepsMarks == unread = observed
end define;
begin
    step:
    while TRUE do
        either
            with m \in Messages do
                await phase[m] = "draft";
                phase[m] := "queued";
            end with;
        or
            with m \in Messages do
                await phase[m] \in {"draft", "queued"};
                phase[m] := "visible";
            end with;
        or
            with m \in Messages do
                await phase[m] \in {"draft", "queued"};
                phase[m] := "cancelled";
            end with;
        or
            await phase["remote"] = "queued";
            phase["remote"] := "unbound-copy";
        or
            with m \in Messages do
                await Eligible /\ (phase[m] = "visible"
                    \/ (Mode = "early" /\ phase[m] = "queued")
                    \/ (Mode = "trust-copy" /\ phase[m] = "unbound-copy"));
                with mark = <<m, IF Mode = "wrong-person" THEN "other" ELSE "selected">> do
                    unread := unread \cup {mark};
                    observed := observed \cup {mark};
                end with;
                premature := premature \/ phase[m] # "visible";
            end with;
        or
            shared := ~shared;
        or
            access := IF "selected" \in access THEN {"other"} ELSE People;
        or
            unread := {};
            observed := {};
        or
            unread := IF Mode = "overwrite" THEN {} ELSE unread;
        end either;
    end while;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "1f838467" /\ chksum(tla) = "9ca6b1b7")
VARIABLES phase, shared, access, unread, observed, premature

(* define statement *)
Eligible == shared /\ "selected" \in access
Exposed == IF Eligible \/ Mode = "stale-view" THEN unread ELSE {}
MarkAfterAdmission == ~premature
MarkOnlySelected == \A mark \in unread : mark[2] = "selected"
ViewRequiresAccess == Exposed = {} \/ Eligible
ActivityKeepsMarks == unread = observed


vars == << phase, shared, access, unread, observed, premature >>

Init == (* Global variables *)
        /\ phase = [m \in Messages |-> "draft"]
        /\ shared = TRUE
        /\ access = People
        /\ unread = {}
        /\ observed = {}
        /\ premature = FALSE

Next == \/ /\ \E m \in Messages:
                /\ phase[m] = "draft"
                /\ phase' = [phase EXCEPT ![m] = "queued"]
           /\ UNCHANGED <<shared, access, unread, observed, premature>>
        \/ /\ \E m \in Messages:
                /\ phase[m] \in {"draft", "queued"}
                /\ phase' = [phase EXCEPT ![m] = "visible"]
           /\ UNCHANGED <<shared, access, unread, observed, premature>>
        \/ /\ \E m \in Messages:
                /\ phase[m] \in {"draft", "queued"}
                /\ phase' = [phase EXCEPT ![m] = "cancelled"]
           /\ UNCHANGED <<shared, access, unread, observed, premature>>
        \/ /\ phase["remote"] = "queued"
           /\ phase' = [phase EXCEPT !["remote"] = "unbound-copy"]
           /\ UNCHANGED <<shared, access, unread, observed, premature>>
        \/ /\ \E m \in Messages:
                /\   Eligible /\ (phase[m] = "visible"
                   \/ (Mode = "early" /\ phase[m] = "queued")
                   \/ (Mode = "trust-copy" /\ phase[m] = "unbound-copy"))
                /\ LET mark == <<m, IF Mode = "wrong-person" THEN "other" ELSE "selected">> IN
                     /\ unread' = (unread \cup {mark})
                     /\ observed' = (observed \cup {mark})
                /\ premature' = (premature \/ phase[m] # "visible")
           /\ UNCHANGED <<phase, shared, access>>
        \/ /\ shared' = ~shared
           /\ UNCHANGED <<phase, access, unread, observed, premature>>
        \/ /\ access' = (IF "selected" \in access THEN {"other"} ELSE People)
           /\ UNCHANGED <<phase, shared, unread, observed, premature>>
        \/ /\ unread' = {}
           /\ observed' = {}
           /\ UNCHANGED <<phase, shared, access, premature>>
        \/ /\ unread' = (IF Mode = "overwrite" THEN {} ELSE unread)
           /\ UNCHANGED <<phase, shared, access, observed, premature>>

Spec == Init /\ [][Next]_vars

\* END TRANSLATION 
=============================================================================
