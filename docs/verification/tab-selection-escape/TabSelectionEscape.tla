------------------------- MODULE TabSelectionEscape -------------------------
EXTENDS Naturals, FiniteSets

CONSTANTS Guarded, MaxOpens

InView == "Alpha"
Clicked == "Charlie"
Tabs == {InView, Clicked}
NoMenu == "none"
Started == {InView, Clicked}
Targets(selection) == IF Clicked \in selection THEN selection ELSE {Clicked}
NoEscape == [focus |-> "none", menu |-> NoMenu, before |-> {}, after |-> {}]

(* --algorithm TabSelectionEscape
variables
    picked = {Clicked},
    menu = NoMenu,
    focus = "tab",
    opens = 0,
    grouped = {},
    intended = Started,
    lastEscape = NoEscape,
    raced = FALSE;

define
    Selection == {InView} \cup picked
end define;

process User = "user"
begin
Act:
    while grouped = {} do
        either
            await menu = NoMenu /\ opens < MaxOpens;
            menu := Clicked || focus := "tab" || opens := opens + 1;
        or
            with handled = focus = "tab" /\ Cardinality(Selection) > 1 /\ (~Guarded \/ menu = NoMenu),
                 after = IF handled THEN {InView} ELSE Selection do
                lastEscape := [focus |-> focus, menu |-> menu, before |-> Selection, after |-> after];
                raced := raced \/ (focus = "tab" /\ menu # NoMenu);
                intended := IF menu = NoMenu /\ Cardinality(Selection) > 1 THEN {InView} ELSE intended;
                picked := IF handled THEN {} ELSE picked;
                menu := NoMenu || focus := "tab";
            end with;
        or
            await menu = Clicked;
            grouped := Targets(Selection);
            menu := NoMenu || focus := "tab";
        end either;
    end while;
end process;

process Focus = "focus"
begin
MoveFocus:
    while TRUE do
        await menu # NoMenu /\ focus = "tab";
        focus := "menu";
    end while;
end process;
end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, picked, menu, focus, opens, grouped, intended, lastEscape, 
          raced

(* define statement *)
Selection == {InView} \cup picked


vars == << pc, picked, menu, focus, opens, grouped, intended, lastEscape, 
           raced >>

ProcSet == {"user"} \cup {"focus"}

Init == (* Global variables *)
        /\ picked = {Clicked}
        /\ menu = NoMenu
        /\ focus = "tab"
        /\ opens = 0
        /\ grouped = {}
        /\ intended = Started
        /\ lastEscape = NoEscape
        /\ raced = FALSE
        /\ pc = [self \in ProcSet |-> CASE self = "user" -> "Act"
                                        [] self = "focus" -> "MoveFocus"]

Act == /\ pc["user"] = "Act"
       /\ IF grouped = {}
             THEN /\ \/ /\ menu = NoMenu /\ opens < MaxOpens
                        /\ /\ focus' = "tab"
                           /\ menu' = Clicked
                           /\ opens' = opens + 1
                        /\ UNCHANGED <<picked, grouped, intended, lastEscape, raced>>
                     \/ /\ LET handled == focus = "tab" /\ Cardinality(Selection) > 1 /\ (~Guarded \/ menu = NoMenu) IN
                             LET after == IF handled THEN {InView} ELSE Selection IN
                               /\ lastEscape' = [focus |-> focus, menu |-> menu, before |-> Selection, after |-> after]
                               /\ raced' = (raced \/ (focus = "tab" /\ menu # NoMenu))
                               /\ intended' = (IF menu = NoMenu /\ Cardinality(Selection) > 1 THEN {InView} ELSE intended)
                               /\ picked' = IF handled THEN {} ELSE picked
                               /\ /\ focus' = "tab"
                                  /\ menu' = NoMenu
                        /\ UNCHANGED <<opens, grouped>>
                     \/ /\ menu = Clicked
                        /\ grouped' = Targets(Selection)
                        /\ /\ focus' = "tab"
                           /\ menu' = NoMenu
                        /\ UNCHANGED <<picked, opens, intended, lastEscape, raced>>
                  /\ pc' = [pc EXCEPT !["user"] = "Act"]
             ELSE /\ pc' = [pc EXCEPT !["user"] = "Done"]
                  /\ UNCHANGED << picked, menu, focus, opens, grouped, 
                                  intended, lastEscape, raced >>

User == Act

MoveFocus == /\ pc["focus"] = "MoveFocus"
             /\ menu # NoMenu /\ focus = "tab"
             /\ focus' = "menu"
             /\ pc' = [pc EXCEPT !["focus"] = "MoveFocus"]
             /\ UNCHANGED << picked, menu, opens, grouped, intended, 
                             lastEscape, raced >>

Focus == MoveFocus

Next == User \/ Focus

Spec == Init /\ [][Next]_vars

\* END TRANSLATION

TypeOK ==
    /\ picked \subseteq {Clicked}
    /\ menu \in {NoMenu, Clicked}
    /\ focus \in {"tab", "menu"}
    /\ opens \in 0..MaxOpens
    /\ grouped \subseteq Tabs
    /\ intended \in {Started, {InView}}
    /\ raced \in BOOLEAN

EscapeWithMenuKeepsSelection == lastEscape.menu # NoMenu => lastEscape.after = lastEscape.before

EscapeWithoutMenuClears ==
    lastEscape.focus = "tab" /\ lastEscape.menu = NoMenu /\ Cardinality(lastEscape.before) > 1
        => lastEscape.after = {InView}

GroupGetsSelection == grouped # {} => grouped = Targets(intended)

NoGroupAfterRace == ~(raced /\ grouped # {})

NoEscapeClearsOnPurpose ==
    ~(lastEscape.focus = "tab" /\ lastEscape.menu = NoMenu /\ Cardinality(lastEscape.before) > 1)

=============================================================================
