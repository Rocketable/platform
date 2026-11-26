---------------------------- MODULE OfflineView ----------------------------
EXTENDS TLC, Sequences

CONSTANTS Rule
Tabs == {"a", "b"}
Routes == Tabs \cup {"local"}
Family(shown) == CASE shown \in {"a", "a-child"} -> "a"
                   [] shown \in {"b", "b-child"} -> "b"
                   [] OTHER -> ""

(* --algorithm OfflineView
variables route \in Routes,
          offline = {"a"},
          reconfigured = FALSE,
          mounted = FALSE,
          warmed = FALSE,
          target = "",
          pendingChild = FALSE,
          slot = "",
          shown = "",
          reported = "",
          admitted = "",
          painted = FALSE;

define
    Visible == route \in Tabs /\ (route \notin offline \/ reported = route)
    Start == IF route = "local" THEN "a"
             ELSE IF Rule = "cut-start" /\ route \in offline /\ ~Visible
                  THEN IF route = "a" THEN "" ELSE "a"
                  ELSE route
    KeepWarmWithOpenTabs == ~warmed \/ mounted
    NoOfflineWithoutVisibleReport == route \notin offline \/ reported # route \/ admitted = route
    OpenViewSurvivesOffline == ~painted \/ Visible
    ReportMatchesRoute == reported = "" \/ Family(shown) = reported
    ReportBelongsToMount == mounted \/ (shown = "" /\ reported = "")
end define;

macro dispose() begin
    mounted := FALSE;
    target := "";
    pendingChild := FALSE;
    slot := "";
    shown := "";
    reported := "";
    admitted := "";
    painted := FALSE;
end macro;

macro showView(active, outages) begin
    if mounted then
        slot := IF active \in Tabs /\ (active \notin outages \/ reported = active) THEN active ELSE "";
        if slot # "" then
            if target # slot then pendingChild := FALSE; end if;
            target := slot;
            if Rule \in {"fixed", "shown-child", "stale-readmit"} /\ reported = "" /\ shown = slot then
                reported := slot;
                admitted := slot;
            end if;
        end if;
    end if;
end macro;

begin
Again:
    while TRUE do
        either
            with nextRoute \in Routes \ {route} do
                route := nextRoute;
                painted := FALSE;
                showView(route, offline);
            end with;
        or
            with tab \in Tabs do
                offline := IF tab \in offline THEN offline \ {tab} ELSE offline \cup {tab};
                showView(route, offline);
            end with;
        or
            await ~mounted /\ Start # "";
            mounted := TRUE;
            warmed := TRUE;
            target := Start;
        or
            await mounted /\ Start = "";
            dispose();
        or
            await ~reconfigured;
            reconfigured := TRUE;
            warmed := FALSE;
            dispose();
        or
            await mounted;
            showView(route, offline);
        or
            await mounted /\ Visible /\ slot = route /\ shown = target;
            pendingChild := TRUE;
        or
            await mounted;
            with location \in IF pendingChild /\ ((Visible /\ slot = route) \/ Rule \in {"fixed", "stale-readmit"})
                              THEN {target, target \o "-child"} ELSE {target} do
                await location # shown;
                if location # target then pendingChild := FALSE; end if;
                if Visible /\ slot = route then
                    admitted := route;
                    reported := route;
                else
                    reported := IF Rule = "old" \/
                                   (Rule = "stale-readmit" /\ route = target /\ route \in offline /\ shown = route \o "-child")
                                THEN route ELSE "";
                    admitted := "";
                end if;
                shown := location;
            end with;
        or
            await Visible /\ slot = route /\ Family(shown) = route;
            painted := TRUE;
        end either;
    end while;
end algorithm; *)
\* BEGIN TRANSLATION (chksum(pcal) = "51de7784" /\ chksum(tla) = "8c82878f")
VARIABLES route, offline, reconfigured, mounted, warmed, target, pendingChild, 
          slot, shown, reported, admitted, painted

(* define statement *)
Visible == route \in Tabs /\ (route \notin offline \/ reported = route)
Start == IF route = "local" THEN "a"
         ELSE IF Rule = "cut-start" /\ route \in offline /\ ~Visible
              THEN IF route = "a" THEN "" ELSE "a"
              ELSE route
KeepWarmWithOpenTabs == ~warmed \/ mounted
NoOfflineWithoutVisibleReport == route \notin offline \/ reported # route \/ admitted = route
OpenViewSurvivesOffline == ~painted \/ Visible
ReportMatchesRoute == reported = "" \/ Family(shown) = reported
ReportBelongsToMount == mounted \/ (shown = "" /\ reported = "")


vars == << route, offline, reconfigured, mounted, warmed, target, 
           pendingChild, slot, shown, reported, admitted, painted >>

Init == (* Global variables *)
        /\ route \in Routes
        /\ offline = {"a"}
        /\ reconfigured = FALSE
        /\ mounted = FALSE
        /\ warmed = FALSE
        /\ target = ""
        /\ pendingChild = FALSE
        /\ slot = ""
        /\ shown = ""
        /\ reported = ""
        /\ admitted = ""
        /\ painted = FALSE

Next == \/ /\ \E nextRoute \in Routes \ {route}:
                /\ route' = nextRoute
                /\ painted' = FALSE
                /\ IF mounted
                      THEN /\ slot' = (IF route' \in Tabs /\ (route' \notin offline \/ reported = route') THEN route' ELSE "")
                           /\ IF slot' # ""
                                 THEN /\ IF target # slot'
                                            THEN /\ pendingChild' = FALSE
                                            ELSE /\ TRUE
                                                 /\ UNCHANGED pendingChild
                                      /\ target' = slot'
                                      /\ IF Rule \in {"fixed", "shown-child", "stale-readmit"} /\ reported = "" /\ shown = slot'
                                            THEN /\ reported' = slot'
                                                 /\ admitted' = slot'
                                            ELSE /\ TRUE
                                                 /\ UNCHANGED << reported, 
                                                                 admitted >>
                                 ELSE /\ TRUE
                                      /\ UNCHANGED << target, pendingChild, 
                                                      reported, admitted >>
                      ELSE /\ TRUE
                           /\ UNCHANGED << target, pendingChild, slot, 
                                           reported, admitted >>
           /\ UNCHANGED <<offline, reconfigured, mounted, warmed, shown>>
        \/ /\ \E tab \in Tabs:
                /\ offline' = (IF tab \in offline THEN offline \ {tab} ELSE offline \cup {tab})
                /\ IF mounted
                      THEN /\ slot' = (IF route \in Tabs /\ (route \notin offline' \/ reported = route) THEN route ELSE "")
                           /\ IF slot' # ""
                                 THEN /\ IF target # slot'
                                            THEN /\ pendingChild' = FALSE
                                            ELSE /\ TRUE
                                                 /\ UNCHANGED pendingChild
                                      /\ target' = slot'
                                      /\ IF Rule \in {"fixed", "shown-child", "stale-readmit"} /\ reported = "" /\ shown = slot'
                                            THEN /\ reported' = slot'
                                                 /\ admitted' = slot'
                                            ELSE /\ TRUE
                                                 /\ UNCHANGED << reported, 
                                                                 admitted >>
                                 ELSE /\ TRUE
                                      /\ UNCHANGED << target, pendingChild, 
                                                      reported, admitted >>
                      ELSE /\ TRUE
                           /\ UNCHANGED << target, pendingChild, slot, 
                                           reported, admitted >>
           /\ UNCHANGED <<route, reconfigured, mounted, warmed, shown, painted>>
        \/ /\ ~mounted /\ Start # ""
           /\ mounted' = TRUE
           /\ warmed' = TRUE
           /\ target' = Start
           /\ UNCHANGED <<route, offline, reconfigured, pendingChild, slot, shown, reported, admitted, painted>>
        \/ /\ mounted /\ Start = ""
           /\ mounted' = FALSE
           /\ target' = ""
           /\ pendingChild' = FALSE
           /\ slot' = ""
           /\ shown' = ""
           /\ reported' = ""
           /\ admitted' = ""
           /\ painted' = FALSE
           /\ UNCHANGED <<route, offline, reconfigured, warmed>>
        \/ /\ ~reconfigured
           /\ reconfigured' = TRUE
           /\ warmed' = FALSE
           /\ mounted' = FALSE
           /\ target' = ""
           /\ pendingChild' = FALSE
           /\ slot' = ""
           /\ shown' = ""
           /\ reported' = ""
           /\ admitted' = ""
           /\ painted' = FALSE
           /\ UNCHANGED <<route, offline>>
        \/ /\ mounted
           /\ IF mounted
                 THEN /\ slot' = (IF route \in Tabs /\ (route \notin offline \/ reported = route) THEN route ELSE "")
                      /\ IF slot' # ""
                            THEN /\ IF target # slot'
                                       THEN /\ pendingChild' = FALSE
                                       ELSE /\ TRUE
                                            /\ UNCHANGED pendingChild
                                 /\ target' = slot'
                                 /\ IF Rule \in {"fixed", "shown-child", "stale-readmit"} /\ reported = "" /\ shown = slot'
                                       THEN /\ reported' = slot'
                                            /\ admitted' = slot'
                                       ELSE /\ TRUE
                                            /\ UNCHANGED << reported, 
                                                            admitted >>
                            ELSE /\ TRUE
                                 /\ UNCHANGED << target, pendingChild, 
                                                 reported, admitted >>
                 ELSE /\ TRUE
                      /\ UNCHANGED << target, pendingChild, slot, reported, 
                                      admitted >>
           /\ UNCHANGED <<route, offline, reconfigured, mounted, warmed, shown, painted>>
        \/ /\ mounted /\ Visible /\ slot = route /\ shown = target
           /\ pendingChild' = TRUE
           /\ UNCHANGED <<route, offline, reconfigured, mounted, warmed, target, slot, shown, reported, admitted, painted>>
        \/ /\ mounted
           /\ \E location \in IF pendingChild /\ ((Visible /\ slot = route) \/ Rule \in {"fixed", "stale-readmit"})
                              THEN {target, target \o "-child"} ELSE {target}:
                /\ location # shown
                /\ IF location # target
                      THEN /\ pendingChild' = FALSE
                      ELSE /\ TRUE
                           /\ UNCHANGED pendingChild
                /\ IF Visible /\ slot = route
                      THEN /\ admitted' = route
                           /\ reported' = route
                      ELSE /\ reported' = (IF Rule = "old" \/
                                              (Rule = "stale-readmit" /\ route = target /\ route \in offline /\ shown = route \o "-child")
                                           THEN route ELSE "")
                           /\ admitted' = ""
                /\ shown' = location
           /\ UNCHANGED <<route, offline, reconfigured, mounted, warmed, target, slot, painted>>
        \/ /\ Visible /\ slot = route /\ Family(shown) = route
           /\ painted' = TRUE
           /\ UNCHANGED <<route, offline, reconfigured, mounted, warmed, target, pendingChild, slot, shown, reported, admitted>>

Spec == Init /\ [][Next]_vars

\* END TRANSLATION 

=============================================================================
