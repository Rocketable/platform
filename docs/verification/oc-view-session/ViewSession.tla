------------------------------ MODULE ViewSession ------------------------------
(* Which session a RocketClaw remote OpenCode tab shows. The Web's sidebar marks
   the tab its route names; the copied OpenCode view inside the tab shows what
   its own router shows. Both must name the same session, or one in its family
   (a subagent the view opened inside the tab).

   One view serves every tab of a server. Selecting another of its tabs runs
   the Web's effect (internal/rocketclaw/web/src/ui.tsx RemoteSession): it
   moves the view with show() and names the server's open tabs with keep().
   The host (internal/rocketclaw/oc/host) pushes the session onto the view's
   memory history, which starts a router transition, and prunes OpenCode's
   own tab list to the named tabs (host/session-tab.tsx).

   The router (@solidjs/router 1.0.0 routing.js createRouterContext) moves
   through transitions; the last one started wins, and its location becomes
   the one the view shows only once the session it shows has loaded. Until
   then every component still reads the location being left. The copied app
   navigates on its own: it rewrites the hash of the session it shows, moves
   to a neighbour tab when its tab list loses the shown one (upstream
   packages/app/src/shell/tabs/tabs.tsx removeTab), opens a child session
   once the child has synced, and voice may open any session.

   The model is the PlusCal algorithm below; `make` translates it into the
   TLA+ between the TRANSLATION markers, which is generated, and checks each
   shape. The Web's effect runs show() and keep() in one browser task, so no
   other process steps while it runs; its own steps are separate here.

   The CONSTANTS switch between implementation shapes:
   - StaleGuard: the view drops a navigation the app starts from a location
     in another session than the view's current history entry, which is the
     case only while a move the host started has not arrived.
   - Outside: what a navigation the app starts to a session outside the
     family of the host's session does: "navigate" moves the view there,
     "drop" ignores it, "open" opens a session in its own RocketClaw tab and
     ignores another session's subagent.
   - Voice: voice may open another session from inside the view. *)
EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS StaleGuard, Outside, Voice, MaxClicks, MaxNavs

Top == {"A", "B", "C"}
Sessions == Top \cup {"Ac"}
Parent(s) == IF s = "Ac" THEN "A" ELSE "none"
Child(s) == IF s = "A" THEN "Ac" ELSE "none"
\* x is o or a subagent of o.
InFamily(x, o) == x = o \/ Parent(x) = o

(* --algorithm ViewSession
variables
    \* The Web: the tab its route names, which the sidebar marks; its open tabs of this server; the
    \* session it last moved the view to and the tabs it last named.
    route = "A", open = Top, told = "A", kept = Top,
    \* The host: the session it moves the view to, OpenCode's own tab list, and the view's history entry.
    opened = "A", store = Top, hist = "A",
    \* The router: the location the view shows, every transition started, the last one, and those
    \* not yet settled, with those whose callback has run.
    ref = "A", trans = <<>>, last = 0, pending = {}, fired = {},
    \* A child the person opened in the view, waiting for its sync.
    childReq = "none",
    \* The tab the person last chose, whether they asked the view for a session since, and whether
    \* the app's own housekeeping ever moved the Web's route.
    clicked = "A", asked = FALSE, hijacked = FALSE,
    clicks = 0, navs = 0;

\* The app navigates the view from location `from` to `to`; `request` says whether the person asked
\* for it (a child, voice) or it is the app's own housekeeping (a hash, a pruned tab).
macro Navigate(from, to, request) begin
    if StaleGuard /\ from # hist then
        skip;
    elsif to \in Sessions /\ ~InFamily(to, opened) /\ Outside = "open" /\ Parent(to) = "none" then
        route := to;
        open := open \cup {to};
        hijacked := hijacked \/ ~request;
    elsif to \in Sessions /\ ~InFamily(to, opened) /\ Outside \in {"open", "drop"} then
        skip;
    else
        trans := Append(trans, [to |-> to, kind |-> "navigate"]);
        last := Len(trans);
        pending := pending \cup {Len(trans)};
    end if;
end macro;

\* The host prunes OpenCode's tab list to `keep` and the session it shows. Losing the tab the view's
\* location is in moves the view to a neighbour tab.
macro Prune(keep) begin
    with gone = (store \cup {opened}) \ (keep \cup {opened}), rest = (store \cup {opened}) \ gone do
        store := rest;
        if \E x \in gone : InFamily(ref, x) then
            with to \in rest do
                Navigate(ref, to, FALSE);
            end with;
        end if;
    end with;
end macro;

process Person = "person"
begin
    Pick:
        while clicks < MaxClicks do
            await pc["web"] = "Effect";
            either
                with s \in open \ {route} do
                    route := s;
                    clicked := s;
                end with;
            or
                \* Closing the tab shown moves the Web's route to another of its tabs.
                with s \in open \ {route} do
                    open := open \ {route};
                    route := s;
                    clicked := s;
                end with;
            or
                with s \in Top \ open do
                    open := open \cup {s};
                    route := s;
                    clicked := s;
                end with;
            end either;
            asked := FALSE;
            clicks := clicks + 1;
        end while;
end process;

\* RemoteSession's effect: show() when the route names another session, then keep().
fair process Web = "web"
begin
    Effect:
        while TRUE do
            await told # route \/ kept # open;
            if told # route then
                Show:
                    told := route;
                    opened := route;
                    \* Setting the session runs SessionTab's effects first, with the tabs named last time.
                    Prune(kept);
                Push:
                    hist := opened;
                    trans := Append(trans, [to |-> opened, kind |-> "native"]);
                    last := Len(trans);
                    pending := pending \cup {Len(trans)};
            end if;
            Keep:
                kept := open;
                Prune(open);
        end while;
end process;

\* Each transition's callback runs, and its location shows if it is still the last one; then it
\* settles, and the last one's navigation is written to the history.
fair process Router = "router"
begin
    Settle:
        while TRUE do
            await pc["web"] = "Effect";
            with t \in pending do
                if t \notin fired then
                    if last = t then
                        ref := trans[t].to;
                    end if;
                    fired := fired \cup {t};
                else
                    if last = t then
                        if trans[t].kind = "navigate" then
                            hist := trans[t].to;
                        end if;
                        last := 0;
                    end if;
                    pending := pending \ {t};
                end if;
            end with;
        end while;
end process;

\* The app's own navigations, each started from the location the view shows.
process App = "app"
begin
    Act:
        while navs < MaxNavs do
            await pc["web"] = "Effect";
            either
                \* The timeline writes or clears the hash of the session it shows.
                await ref \in Sessions;
                Navigate(ref, ref, FALSE);
            or
                \* The person opens the subagent's card; the view goes there once the child synced.
                await Child(ref) # "none" /\ childReq = "none";
                childReq := Child(ref);
                asked := TRUE;
            or
                \* The ancestor trail goes back to the parent.
                await ref \in Sessions /\ Parent(ref) # "none";
                Navigate(ref, Parent(ref), TRUE);
            or
                \* Voice opens another session.
                await Voice;
                with s \in Top \ {ref, Parent(ref)} do
                    Navigate(ref, s, TRUE);
                end with;
                asked := TRUE;
            end either;
            navs := navs + 1;
        end while;
end process;

fair process Sync = "sync"
begin
    Synced:
        while TRUE do
            await childReq # "none" /\ pc["web"] = "Effect";
            Navigate(ref, childReq, TRUE);
            childReq := "none";
        end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, route, open, told, kept, opened, store, hist, ref, trans, last, 
          pending, fired, childReq, clicked, asked, hijacked, clicks, navs

vars == << pc, route, open, told, kept, opened, store, hist, ref, trans, last, 
           pending, fired, childReq, clicked, asked, hijacked, clicks, navs
        >>

ProcSet == {"person"} \cup {"web"} \cup {"router"} \cup {"app"} \cup {"sync"}

Init == (* Global variables *)
        /\ route = "A"
        /\ open = Top
        /\ told = "A"
        /\ kept = Top
        /\ opened = "A"
        /\ store = Top
        /\ hist = "A"
        /\ ref = "A"
        /\ trans = <<>>
        /\ last = 0
        /\ pending = {}
        /\ fired = {}
        /\ childReq = "none"
        /\ clicked = "A"
        /\ asked = FALSE
        /\ hijacked = FALSE
        /\ clicks = 0
        /\ navs = 0
        /\ pc = [self \in ProcSet |-> CASE self = "person" -> "Pick"
                                        [] self = "web" -> "Effect"
                                        [] self = "router" -> "Settle"
                                        [] self = "app" -> "Act"
                                        [] self = "sync" -> "Synced"]

Pick == /\ pc["person"] = "Pick"
        /\ IF clicks < MaxClicks
              THEN /\ pc["web"] = "Effect"
                   /\ \/ /\ \E s \in open \ {route}:
                              /\ route' = s
                              /\ clicked' = s
                         /\ open' = open
                      \/ /\ \E s \in open \ {route}:
                              /\ open' = open \ {route}
                              /\ route' = s
                              /\ clicked' = s
                      \/ /\ \E s \in Top \ open:
                              /\ open' = (open \cup {s})
                              /\ route' = s
                              /\ clicked' = s
                   /\ asked' = FALSE
                   /\ clicks' = clicks + 1
                   /\ pc' = [pc EXCEPT !["person"] = "Pick"]
              ELSE /\ pc' = [pc EXCEPT !["person"] = "Done"]
                   /\ UNCHANGED << route, open, clicked, asked, clicks >>
        /\ UNCHANGED << told, kept, opened, store, hist, ref, trans, last, 
                        pending, fired, childReq, hijacked, navs >>

Person == Pick

Effect == /\ pc["web"] = "Effect"
          /\ told # route \/ kept # open
          /\ IF told # route
                THEN /\ pc' = [pc EXCEPT !["web"] = "Show"]
                ELSE /\ pc' = [pc EXCEPT !["web"] = "Keep"]
          /\ UNCHANGED << route, open, told, kept, opened, store, hist, ref, 
                          trans, last, pending, fired, childReq, clicked, 
                          asked, hijacked, clicks, navs >>

Keep == /\ pc["web"] = "Keep"
        /\ kept' = open
        /\ LET gone == (store \cup {opened}) \ (open \cup {opened}) IN
             LET rest == (store \cup {opened}) \ gone IN
               /\ store' = rest
               /\ IF \E x \in gone : InFamily(ref, x)
                     THEN /\ \E to \in rest:
                               IF StaleGuard /\ ref # hist
                                  THEN /\ TRUE
                                       /\ UNCHANGED << route, open, trans, 
                                                       last, pending, hijacked >>
                                  ELSE /\ IF to \in Sessions /\ ~InFamily(to, opened) /\ Outside = "open" /\ Parent(to) = "none"
                                             THEN /\ route' = to
                                                  /\ open' = (open \cup {to})
                                                  /\ hijacked' = (hijacked \/ ~FALSE)
                                                  /\ UNCHANGED << trans, last, 
                                                                  pending >>
                                             ELSE /\ IF to \in Sessions /\ ~InFamily(to, opened) /\ Outside \in {"open", "drop"}
                                                        THEN /\ TRUE
                                                             /\ UNCHANGED << trans, 
                                                                             last, 
                                                                             pending >>
                                                        ELSE /\ trans' = Append(trans, [to |-> to, kind |-> "navigate"])
                                                             /\ last' = Len(trans')
                                                             /\ pending' = (pending \cup {Len(trans')})
                                                  /\ UNCHANGED << route, open, 
                                                                  hijacked >>
                     ELSE /\ TRUE
                          /\ UNCHANGED << route, open, trans, last, pending, 
                                          hijacked >>
        /\ pc' = [pc EXCEPT !["web"] = "Effect"]
        /\ UNCHANGED << told, opened, hist, ref, fired, childReq, clicked, 
                        asked, clicks, navs >>

Show == /\ pc["web"] = "Show"
        /\ told' = route
        /\ opened' = route
        /\ LET gone == (store \cup {opened'}) \ (kept \cup {opened'}) IN
             LET rest == (store \cup {opened'}) \ gone IN
               /\ store' = rest
               /\ IF \E x \in gone : InFamily(ref, x)
                     THEN /\ \E to \in rest:
                               IF StaleGuard /\ ref # hist
                                  THEN /\ TRUE
                                       /\ UNCHANGED << route, open, trans, 
                                                       last, pending, hijacked >>
                                  ELSE /\ IF to \in Sessions /\ ~InFamily(to, opened') /\ Outside = "open" /\ Parent(to) = "none"
                                             THEN /\ route' = to
                                                  /\ open' = (open \cup {to})
                                                  /\ hijacked' = (hijacked \/ ~FALSE)
                                                  /\ UNCHANGED << trans, last, 
                                                                  pending >>
                                             ELSE /\ IF to \in Sessions /\ ~InFamily(to, opened') /\ Outside \in {"open", "drop"}
                                                        THEN /\ TRUE
                                                             /\ UNCHANGED << trans, 
                                                                             last, 
                                                                             pending >>
                                                        ELSE /\ trans' = Append(trans, [to |-> to, kind |-> "navigate"])
                                                             /\ last' = Len(trans')
                                                             /\ pending' = (pending \cup {Len(trans')})
                                                  /\ UNCHANGED << route, open, 
                                                                  hijacked >>
                     ELSE /\ TRUE
                          /\ UNCHANGED << route, open, trans, last, pending, 
                                          hijacked >>
        /\ pc' = [pc EXCEPT !["web"] = "Push"]
        /\ UNCHANGED << kept, hist, ref, fired, childReq, clicked, asked, 
                        clicks, navs >>

Push == /\ pc["web"] = "Push"
        /\ hist' = opened
        /\ trans' = Append(trans, [to |-> opened, kind |-> "native"])
        /\ last' = Len(trans')
        /\ pending' = (pending \cup {Len(trans')})
        /\ pc' = [pc EXCEPT !["web"] = "Keep"]
        /\ UNCHANGED << route, open, told, kept, opened, store, ref, fired, 
                        childReq, clicked, asked, hijacked, clicks, navs >>

Web == Effect \/ Keep \/ Show \/ Push

Settle == /\ pc["router"] = "Settle"
          /\ pc["web"] = "Effect"
          /\ \E t \in pending:
               IF t \notin fired
                  THEN /\ IF last = t
                             THEN /\ ref' = trans[t].to
                             ELSE /\ TRUE
                                  /\ ref' = ref
                       /\ fired' = (fired \cup {t})
                       /\ UNCHANGED << hist, last, pending >>
                  ELSE /\ IF last = t
                             THEN /\ IF trans[t].kind = "navigate"
                                        THEN /\ hist' = trans[t].to
                                        ELSE /\ TRUE
                                             /\ hist' = hist
                                  /\ last' = 0
                             ELSE /\ TRUE
                                  /\ UNCHANGED << hist, last >>
                       /\ pending' = pending \ {t}
                       /\ UNCHANGED << ref, fired >>
          /\ pc' = [pc EXCEPT !["router"] = "Settle"]
          /\ UNCHANGED << route, open, told, kept, opened, store, trans, 
                          childReq, clicked, asked, hijacked, clicks, navs >>

Router == Settle

Act == /\ pc["app"] = "Act"
       /\ IF navs < MaxNavs
             THEN /\ pc["web"] = "Effect"
                  /\ \/ /\ ref \in Sessions
                        /\ IF StaleGuard /\ ref # hist
                              THEN /\ TRUE
                                   /\ UNCHANGED << route, open, trans, last, 
                                                   pending, hijacked >>
                              ELSE /\ IF ref \in Sessions /\ ~InFamily(ref, opened) /\ Outside = "open" /\ Parent(ref) = "none"
                                         THEN /\ route' = ref
                                              /\ open' = (open \cup {ref})
                                              /\ hijacked' = (hijacked \/ ~FALSE)
                                              /\ UNCHANGED << trans, last, 
                                                              pending >>
                                         ELSE /\ IF ref \in Sessions /\ ~InFamily(ref, opened) /\ Outside \in {"open", "drop"}
                                                    THEN /\ TRUE
                                                         /\ UNCHANGED << trans, 
                                                                         last, 
                                                                         pending >>
                                                    ELSE /\ trans' = Append(trans, [to |-> ref, kind |-> "navigate"])
                                                         /\ last' = Len(trans')
                                                         /\ pending' = (pending \cup {Len(trans')})
                                              /\ UNCHANGED << route, open, 
                                                              hijacked >>
                        /\ UNCHANGED <<childReq, asked>>
                     \/ /\ Child(ref) # "none" /\ childReq = "none"
                        /\ childReq' = Child(ref)
                        /\ asked' = TRUE
                        /\ UNCHANGED <<route, open, trans, last, pending, hijacked>>
                     \/ /\ ref \in Sessions /\ Parent(ref) # "none"
                        /\ IF StaleGuard /\ ref # hist
                              THEN /\ TRUE
                                   /\ UNCHANGED << route, open, trans, last, 
                                                   pending, hijacked >>
                              ELSE /\ IF (Parent(ref)) \in Sessions /\ ~InFamily((Parent(ref)), opened) /\ Outside = "open" /\ Parent((Parent(ref))) = "none"
                                         THEN /\ route' = Parent(ref)
                                              /\ open' = (open \cup {(Parent(ref))})
                                              /\ hijacked' = (hijacked \/ ~TRUE)
                                              /\ UNCHANGED << trans, last, 
                                                              pending >>
                                         ELSE /\ IF (Parent(ref)) \in Sessions /\ ~InFamily((Parent(ref)), opened) /\ Outside \in {"open", "drop"}
                                                    THEN /\ TRUE
                                                         /\ UNCHANGED << trans, 
                                                                         last, 
                                                                         pending >>
                                                    ELSE /\ trans' = Append(trans, [to |-> (Parent(ref)), kind |-> "navigate"])
                                                         /\ last' = Len(trans')
                                                         /\ pending' = (pending \cup {Len(trans')})
                                              /\ UNCHANGED << route, open, 
                                                              hijacked >>
                        /\ UNCHANGED <<childReq, asked>>
                     \/ /\ Voice
                        /\ \E s \in Top \ {ref, Parent(ref)}:
                             IF StaleGuard /\ ref # hist
                                THEN /\ TRUE
                                     /\ UNCHANGED << route, open, trans, last, 
                                                     pending, hijacked >>
                                ELSE /\ IF s \in Sessions /\ ~InFamily(s, opened) /\ Outside = "open" /\ Parent(s) = "none"
                                           THEN /\ route' = s
                                                /\ open' = (open \cup {s})
                                                /\ hijacked' = (hijacked \/ ~TRUE)
                                                /\ UNCHANGED << trans, last, 
                                                                pending >>
                                           ELSE /\ IF s \in Sessions /\ ~InFamily(s, opened) /\ Outside \in {"open", "drop"}
                                                      THEN /\ TRUE
                                                           /\ UNCHANGED << trans, 
                                                                           last, 
                                                                           pending >>
                                                      ELSE /\ trans' = Append(trans, [to |-> s, kind |-> "navigate"])
                                                           /\ last' = Len(trans')
                                                           /\ pending' = (pending \cup {Len(trans')})
                                                /\ UNCHANGED << route, open, 
                                                                hijacked >>
                        /\ asked' = TRUE
                        /\ UNCHANGED childReq
                  /\ navs' = navs + 1
                  /\ pc' = [pc EXCEPT !["app"] = "Act"]
             ELSE /\ pc' = [pc EXCEPT !["app"] = "Done"]
                  /\ UNCHANGED << route, open, trans, last, pending, childReq, 
                                  asked, hijacked, navs >>
       /\ UNCHANGED << told, kept, opened, store, hist, ref, fired, clicked, 
                       clicks >>

App == Act

Synced == /\ pc["sync"] = "Synced"
          /\ childReq # "none" /\ pc["web"] = "Effect"
          /\ IF StaleGuard /\ ref # hist
                THEN /\ TRUE
                     /\ UNCHANGED << route, open, trans, last, pending, 
                                     hijacked >>
                ELSE /\ IF childReq \in Sessions /\ ~InFamily(childReq, opened) /\ Outside = "open" /\ Parent(childReq) = "none"
                           THEN /\ route' = childReq
                                /\ open' = (open \cup {childReq})
                                /\ hijacked' = (hijacked \/ ~TRUE)
                                /\ UNCHANGED << trans, last, pending >>
                           ELSE /\ IF childReq \in Sessions /\ ~InFamily(childReq, opened) /\ Outside \in {"open", "drop"}
                                      THEN /\ TRUE
                                           /\ UNCHANGED << trans, last, 
                                                           pending >>
                                      ELSE /\ trans' = Append(trans, [to |-> childReq, kind |-> "navigate"])
                                           /\ last' = Len(trans')
                                           /\ pending' = (pending \cup {Len(trans')})
                                /\ UNCHANGED << route, open, hijacked >>
          /\ childReq' = "none"
          /\ pc' = [pc EXCEPT !["sync"] = "Synced"]
          /\ UNCHANGED << told, kept, opened, store, hist, ref, fired, clicked, 
                          asked, clicks, navs >>

Sync == Synced

Next == Person \/ Web \/ Router \/ App \/ Sync

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Web)
        /\ WF_vars(Router)
        /\ WF_vars(Sync)

\* END TRANSLATION

---------------------------------------------------------------------------------
\* Requirements.

\* Nothing is moving: no transition is under way, the Web's effect has told the view everything, and no
\* child the person opened is still syncing.
Quiet == pending = {} /\ pc["web"] = "Effect" /\ told = route /\ kept = open /\ childReq = "none"

\* Once nothing is moving, the view shows the session the sidebar marks, or one of its subagents.
Agreement == Quiet => ref \in Sessions /\ InFamily(ref, route)

\* The app's housekeeping never opens a session in a tab of its own.
NoHijack == ~hijacked

\* Once nothing is moving, the sidebar marks the tab the person chose last, unless they asked the
\* view for another session since.
ChoiceStands == (Quiet /\ ~asked) => route = clicked

---------------------------------------------------------------------------------
\* What must happen in at least one state, so a check that passes doesn't pass because the model never
\* gets there. Each .cfg's check of these runs them as invariants and expects each to be violated.

\* A subagent shows inside its parent's tab.
ChildShown == Quiet /\ ref = "Ac" /\ route = "A"

\* Voice or a child opened a session in a tab of its own.
RequestOpened == Quiet /\ asked /\ route # clicked

\* The checks of these expect each to be violated.
ChildNeverShown == ~ChildShown
RequestNeverOpened == ~RequestOpened
=================================================================================
