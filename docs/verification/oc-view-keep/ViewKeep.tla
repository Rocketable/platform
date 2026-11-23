------------------------------- MODULE ViewKeep -------------------------------
(* The lifecycle of the page's one OpenCode view in RocketClaw's Web
   (internal/rocketclaw/web/src/ui.tsx) and what it may do while hidden.

   The view holds every configured OpenCode server. It mounts once the page has
   an OpenCode tab, stays mounted but hidden while any other tab shows, and is
   disposed when no OpenCode tab is left. The Web's effects run in the browser's
   task after a render, so a person's action and the effect it causes are
   separate steps; each effect runs in one task, so its own writes are atomic.
   The view's entry is imported asynchronously; the import may settle after the
   effect that started it was cleaned up, and it may fail.

   Sessions A and B are one server's, C another's. One view moves between them
   with the same show(); which session a move settles on is checked by
   ../oc-view-session.

   The CONSTANTS switch between implementation shapes:
   - KeepHidden: a non-OpenCode tab hides the view (TRUE) or disposes it, as
     before (FALSE).
   - MarkGate: OpenCode marks a session viewed only while the view is visible.
   - SlotGate: only the shown view writes RocketClaw's remoteView slot; FALSE is
     the code before, whose show() effect also ran, and claimed the slot, when
     the open tabs changed.
   - Guard: an import that settles after its effect was cleaned up is dropped.
   - Fails: the entry import may fail; the person may retry. *)
EXTENDS Naturals, FiniteSets

CONSTANTS KeepHidden, MarkGate, SlotGate, Guard, Fails, MaxActs

Tabs == {"A", "B", "C"}
Other == "chat"
None == "none"

(* --algorithm ViewKeep
variables
    \* The Web: open OpenCode tabs on any space, the tab the route shows, the open tabs last named to the view.
    open \in {{"A", "C"}, {"A"}}, route \in {Other, "A"}, kept = {},
    \* The view's lifecycle, the generation of the effect that last started an import, imports in flight by
    \* generation, and the generations whose effect was cleaned up.
    phase = "absent", gen = 0, inflight = {}, cleaned = {},
    \* The mounted view: the session it shows and whether it is visible; RocketClaw's remoteView slot.
    shown = None, visible = FALSE, slot = None,
    \* The window has focus; whether OpenCode ever marked a session viewed while hidden; whether a view
    \* ever mounted for a cleaned-up effect.
    focused = TRUE, hiddenMark = FALSE, orphan = FALSE,
    acts = 0;

define
    OnOpenCode == route \in Tabs
    Wanted == open # {} /\ (KeepHidden \/ OnOpenCode)
    \* What the Web's effects still have to do.
    Starting == Wanted /\ phase = "absent"
    Stopping == ~Wanted /\ phase \in {"loading", "failed"}
    Disposing == ~Wanted /\ phase = "mounted"
    Showing == phase = "mounted" /\ OnOpenCode /\ (shown # route \/ ~visible \/ slot # route)
    Hiding == phase = "mounted" /\ ~OnOpenCode /\ (visible \/ slot # None)
    Keeping == phase = "mounted" /\ kept # open
    Pending == Starting \/ Stopping \/ Disposing \/ Showing \/ Hiding \/ Keeping
    Quiet == ~Pending /\ inflight = {}

    MarkedWhileVisible == ~hiddenMark
    SlotOnlyShown == slot # None => phase = "mounted" /\ visible /\ slot = shown
    NoOrphanView == ~orphan
    Agreement == Quiet =>
        /\ (open # {} => phase \in {"mounted", "failed"})
        /\ (open = {} => phase = "absent")
        /\ (phase = "mounted" => (visible <=> OnOpenCode) /\ (OnOpenCode => shown = route /\ slot = route))
        /\ (~OnOpenCode => slot = None)

    Done == acts = MaxActs
    EventuallyShown == (Done /\ OnOpenCode) ~> (visible /\ shown = route)
    EventuallyWarm == (Done /\ open # {}) ~> (phase = "mounted")
end define;

process Person = "person"
begin
    Act:
        while acts < MaxActs do
            either
                with t \in (open \cup {Other}) \ {route} do route := t; end with;
            or
                with t \in Tabs \ open do open := open \cup {t}; route := t; end with;
            or
                \* Closing an OpenCode tab moves the route to another open tab, or off OpenCode.
                with t \in open, next \in (open \ {t}) \cup {Other} do
                    open := open \ {t};
                    if route = t then route := next; end if;
                end with;
            or
                focused := ~focused;
            or
                \* Retry, offered while the import failed.
                await phase = "failed";
                gen := gen + 1;
                inflight := inflight \cup {gen};
                phase := "loading";
            end either;
            acts := acts + 1;
        end while;
end process;

\* The Web's effects, each in one task.
fair process Web = "web"
begin
    Effect:
        while TRUE do
            await Pending;
            if Starting then
                gen := gen + 1;
                inflight := inflight \cup {gen};
                phase := "loading";
            elsif Stopping then
                cleaned := cleaned \cup {gen};
                phase := "absent";
            elsif Disposing then
                cleaned := cleaned \cup {gen};
                phase := "absent";
                shown := None;
                visible := FALSE;
                slot := None;
                kept := {};
            elsif Showing then
                shown := route;
                visible := TRUE;
                slot := route;
            elsif Hiding then
                visible := FALSE;
                slot := None;
            else
                kept := open;
                if ~SlotGate then slot := shown; end if;
            end if;
        end while;
end process;

\* The entry import settles: the view mounts on the route's session, or the last OpenCode tab, or fails.
fair process Import = "import"
begin
    Settle:
        while TRUE do
            await inflight # {};
            with g \in inflight do
                inflight := inflight \ {g};
                if g \in cleaned /\ Guard then
                    skip;
                elsif g \in cleaned then
                    orphan := TRUE;
                else
                    either
                        phase := "mounted";
                        visible := OnOpenCode;
                        slot := IF OnOpenCode THEN route ELSE None;
                        with first \in IF OnOpenCode THEN {route} ELSE open do shown := first; end with;
                    or
                        await Fails;
                        phase := "failed";
                    end either;
                end if;
            end with;
        end while;
end process;

\* OpenCode's MarkSessionViewed: while its window has focus it marks the session it shows viewed.
process Mark = "mark"
begin
    Viewed:
        while TRUE do
            await phase = "mounted" /\ shown # None /\ focused /\ (MarkGate => visible) /\ ~hiddenMark;
            hiddenMark := ~visible;
        end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, open, route, kept, phase, gen, inflight, cleaned, shown, 
          visible, slot, focused, hiddenMark, orphan, acts

(* define statement *)
OnOpenCode == route \in Tabs
Wanted == open # {} /\ (KeepHidden \/ OnOpenCode)

Starting == Wanted /\ phase = "absent"
Stopping == ~Wanted /\ phase \in {"loading", "failed"}
Disposing == ~Wanted /\ phase = "mounted"
Showing == phase = "mounted" /\ OnOpenCode /\ (shown # route \/ ~visible \/ slot # route)
Hiding == phase = "mounted" /\ ~OnOpenCode /\ (visible \/ slot # None)
Keeping == phase = "mounted" /\ kept # open
Pending == Starting \/ Stopping \/ Disposing \/ Showing \/ Hiding \/ Keeping
Quiet == ~Pending /\ inflight = {}

MarkedWhileVisible == ~hiddenMark
SlotOnlyShown == slot # None => phase = "mounted" /\ visible /\ slot = shown
NoOrphanView == ~orphan
Agreement == Quiet =>
    /\ (open # {} => phase \in {"mounted", "failed"})
    /\ (open = {} => phase = "absent")
    /\ (phase = "mounted" => (visible <=> OnOpenCode) /\ (OnOpenCode => shown = route /\ slot = route))
    /\ (~OnOpenCode => slot = None)

Done == acts = MaxActs
EventuallyShown == (Done /\ OnOpenCode) ~> (visible /\ shown = route)
EventuallyWarm == (Done /\ open # {}) ~> (phase = "mounted")


vars == << pc, open, route, kept, phase, gen, inflight, cleaned, shown, 
           visible, slot, focused, hiddenMark, orphan, acts >>

ProcSet == {"person"} \cup {"web"} \cup {"import"} \cup {"mark"}

Init == (* Global variables *)
        /\ open \in {{"A", "C"}, {"A"}}
        /\ route \in {Other, "A"}
        /\ kept = {}
        /\ phase = "absent"
        /\ gen = 0
        /\ inflight = {}
        /\ cleaned = {}
        /\ shown = None
        /\ visible = FALSE
        /\ slot = None
        /\ focused = TRUE
        /\ hiddenMark = FALSE
        /\ orphan = FALSE
        /\ acts = 0
        /\ pc = [self \in ProcSet |-> CASE self = "person" -> "Act"
                                        [] self = "web" -> "Effect"
                                        [] self = "import" -> "Settle"
                                        [] self = "mark" -> "Viewed"]

Act == /\ pc["person"] = "Act"
       /\ IF acts < MaxActs
             THEN /\ \/ /\ \E t \in (open \cup {Other}) \ {route}:
                             route' = t
                        /\ UNCHANGED <<open, phase, gen, inflight, focused>>
                     \/ /\ \E t \in Tabs \ open:
                             /\ open' = (open \cup {t})
                             /\ route' = t
                        /\ UNCHANGED <<phase, gen, inflight, focused>>
                     \/ /\ \E t \in open:
                             \E next \in (open \ {t}) \cup {Other}:
                               /\ open' = open \ {t}
                               /\ IF route = t
                                     THEN /\ route' = next
                                     ELSE /\ TRUE
                                          /\ route' = route
                        /\ UNCHANGED <<phase, gen, inflight, focused>>
                     \/ /\ focused' = ~focused
                        /\ UNCHANGED <<open, route, phase, gen, inflight>>
                     \/ /\ phase = "failed"
                        /\ gen' = gen + 1
                        /\ inflight' = (inflight \cup {gen'})
                        /\ phase' = "loading"
                        /\ UNCHANGED <<open, route, focused>>
                  /\ acts' = acts + 1
                  /\ pc' = [pc EXCEPT !["person"] = "Act"]
             ELSE /\ pc' = [pc EXCEPT !["person"] = "Done"]
                  /\ UNCHANGED << open, route, phase, gen, inflight, focused, 
                                  acts >>
       /\ UNCHANGED << kept, cleaned, shown, visible, slot, hiddenMark, orphan >>

Person == Act

Effect == /\ pc["web"] = "Effect"
          /\ Pending
          /\ IF Starting
                THEN /\ gen' = gen + 1
                     /\ inflight' = (inflight \cup {gen'})
                     /\ phase' = "loading"
                     /\ UNCHANGED << kept, cleaned, shown, visible, slot >>
                ELSE /\ IF Stopping
                           THEN /\ cleaned' = (cleaned \cup {gen})
                                /\ phase' = "absent"
                                /\ UNCHANGED << kept, shown, visible, slot >>
                           ELSE /\ IF Disposing
                                      THEN /\ cleaned' = (cleaned \cup {gen})
                                           /\ phase' = "absent"
                                           /\ shown' = None
                                           /\ visible' = FALSE
                                           /\ slot' = None
                                           /\ kept' = {}
                                      ELSE /\ IF Showing
                                                 THEN /\ shown' = route
                                                      /\ visible' = TRUE
                                                      /\ slot' = route
                                                      /\ kept' = kept
                                                 ELSE /\ IF Hiding
                                                            THEN /\ visible' = FALSE
                                                                 /\ slot' = None
                                                                 /\ kept' = kept
                                                            ELSE /\ kept' = open
                                                                 /\ IF ~SlotGate
                                                                       THEN /\ slot' = shown
                                                                       ELSE /\ TRUE
                                                                            /\ slot' = slot
                                                                 /\ UNCHANGED visible
                                                      /\ shown' = shown
                                           /\ UNCHANGED << phase, cleaned >>
                     /\ UNCHANGED << gen, inflight >>
          /\ pc' = [pc EXCEPT !["web"] = "Effect"]
          /\ UNCHANGED << open, route, focused, hiddenMark, orphan, acts >>

Web == Effect

Settle == /\ pc["import"] = "Settle"
          /\ inflight # {}
          /\ \E g \in inflight:
               /\ inflight' = inflight \ {g}
               /\ IF g \in cleaned /\ Guard
                     THEN /\ TRUE
                          /\ UNCHANGED << phase, shown, visible, slot, orphan >>
                     ELSE /\ IF g \in cleaned
                                THEN /\ orphan' = TRUE
                                     /\ UNCHANGED << phase, shown, visible, 
                                                     slot >>
                                ELSE /\ \/ /\ phase' = "mounted"
                                           /\ visible' = OnOpenCode
                                           /\ slot' = IF OnOpenCode THEN route ELSE None
                                           /\ \E first \in IF OnOpenCode THEN {route} ELSE open:
                                                shown' = first
                                        \/ /\ Fails
                                           /\ phase' = "failed"
                                           /\ UNCHANGED <<shown, visible, slot>>
                                     /\ UNCHANGED orphan
          /\ pc' = [pc EXCEPT !["import"] = "Settle"]
          /\ UNCHANGED << open, route, kept, gen, cleaned, focused, hiddenMark, 
                          acts >>

Import == Settle

Viewed == /\ pc["mark"] = "Viewed"
          /\ phase = "mounted" /\ shown # None /\ focused /\ (MarkGate => visible) /\ ~hiddenMark
          /\ hiddenMark' = ~visible
          /\ pc' = [pc EXCEPT !["mark"] = "Viewed"]
          /\ UNCHANGED << open, route, kept, phase, gen, inflight, cleaned, 
                          shown, visible, slot, focused, orphan, acts >>

Mark == Viewed

Next == Person \/ Web \/ Import \/ Mark

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Web)
        /\ WF_vars(Import)

\* END TRANSLATION
=============================================================================
