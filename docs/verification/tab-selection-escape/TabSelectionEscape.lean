inductive Tab where
  | alpha
  | bravo
  | charlie
  deriving DecidableEq, Repr

inductive EscapeTarget where
  | tab
  | menu
  deriving DecidableEq, Repr

def EscapeTarget.isTab : EscapeTarget → Bool
  | .tab => true
  | .menu => false

def fixedEscape (menuOpen : Bool) (targetIsTab : Bool) (selection : List Tab) (inView : Tab) : List Tab :=
  if targetIsTab && decide (1 < selection.length) && !menuOpen then [inView] else selection

def oldEscape (_menuOpenIgnored : Bool) (targetIsTab : Bool) (selection : List Tab) (inView : Tab) : List Tab :=
  if targetIsTab && decide (1 < selection.length) then [inView] else selection

def menuTargets (selection : List Tab) (clicked : Tab) : List Tab :=
  if clicked ∈ selection then selection else [clicked]

def fixedEscapesWhileMenuOpen (targets : List EscapeTarget) (selection : List Tab) (inView : Tab) : List Tab :=
  targets.foldl (fun current target => fixedEscape true target.isTab current inView) selection

theorem fixed_escape_keeps_selection_while_menu_open (targetIsTab : Bool) (selection : List Tab) (inView : Tab) :
    fixedEscape true targetIsTab selection inView = selection := by
  simp [fixedEscape]

theorem fixed_and_old_escape_agree_without_menu (targetIsTab : Bool) (selection : List Tab) (inView : Tab) :
    fixedEscape false targetIsTab selection inView = oldEscape false targetIsTab selection inView := by
  simp [fixedEscape, oldEscape]

theorem fixed_escape_on_tab_without_menu_clears_multi_tab_selection (selection : List Tab) (inView : Tab)
    (multiTab : 1 < selection.length) :
    fixedEscape false true selection inView = [inView] := by
  simp [fixedEscape, multiTab]

theorem old_escape_with_menu_open_groups_only_clicked_charlie :
    oldEscape true true [.alpha, .charlie] .alpha = [.alpha] ∧
    menuTargets (oldEscape true true [.alpha, .charlie] .alpha) .charlie = [.charlie] ∧
    menuTargets (fixedEscape true true [.alpha, .charlie] .alpha) .charlie = [.alpha, .charlie] := by
  decide

theorem fixed_escapes_while_menu_open_keep_selection (targets : List EscapeTarget) (selection : List Tab) (inView : Tab) :
    fixedEscapesWhileMenuOpen targets selection inView = selection := by
  induction targets with
  | nil => rfl
  | cons target rest restKeepsSelection =>
    simpa [fixedEscapesWhileMenuOpen, List.foldl, fixed_escape_keeps_selection_while_menu_open] using restKeepsSelection

theorem menu_after_escapes_while_open_targets_whole_selection (targets : List EscapeTarget) (selection : List Tab)
    (inView clicked : Tab) (clickedSelected : clicked ∈ selection) :
    menuTargets (fixedEscapesWhileMenuOpen targets selection inView) clicked = selection := by
  rw [fixed_escapes_while_menu_open_keep_selection]
  simp [menuTargets, clickedSelected]
