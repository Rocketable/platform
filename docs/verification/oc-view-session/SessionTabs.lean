structure Session where
  server : Nat
  id : Nat
  deriving DecidableEq, Repr

inductive Tab where
  | session (s : Session)
  | other (name : Nat)
  deriving DecidableEq, Repr

def keeps (same : Session → Session → Bool) (shown : Session) (opened : List Session) (s : Session) : Bool :=
  same s shown || opened.any (same s)

def pruneWith (same : Session → Session → Bool) (shown : Session) (opened : List Session) (tabs : List Tab) : List Tab :=
  tabs.filter fun
    | .session s => keeps same shown opened s
    | .other _ => true

def samePair (a b : Session) : Bool := a == b

def sameId (a b : Session) : Bool := a.id == b.id

def prune := pruneWith samePair

theorem shown_tab_stays (shown : Session) (opened : List Session) (tabs : List Tab) (present : Tab.session shown ∈ tabs) :
    Tab.session shown ∈ prune shown opened tabs := by
  simp [prune, pruneWith, keeps, samePair, present]

theorem open_tab_stays_on_every_server (shown s : Session) (opened : List Session) (tabs : List Tab)
    (isOpen : s ∈ opened) (present : Tab.session s ∈ tabs) :
    Tab.session s ∈ prune shown opened tabs := by
  simp only [prune, pruneWith, List.mem_filter, keeps, samePair]
  refine ⟨present, ?_⟩
  simp only [Bool.or_eq_true, List.any_eq_true]
  exact Or.inr ⟨s, isOpen, by simp [samePair]⟩

theorem other_tabs_stay (shown : Session) (opened : List Session) (tabs : List Tab) (name : Nat)
    (present : Tab.other name ∈ tabs) :
    Tab.other name ∈ prune shown opened tabs := by
  simp [prune, pruneWith, present]

theorem closed_tab_goes (shown s : Session) (opened : List Session) (tabs : List Tab)
    (kept : Tab.session s ∈ prune shown opened tabs) :
    s = shown ∨ s ∈ opened := by
  simp only [prune, pruneWith, List.mem_filter, keeps, samePair, Bool.or_eq_true, List.any_eq_true,
    beq_iff_eq] at kept
  rcases kept.2 with isShown | ⟨other, isOpen, same⟩
  · exact Or.inl isShown
  · exact Or.inr (same ▸ isOpen)

theorem prune_keeps_its_own_result (shown : Session) (opened : List Session) (tabs : List Tab) :
    prune shown opened (prune shown opened tabs) = prune shown opened tabs := by
  simp [prune, pruneWith, List.filter_filter]

theorem matching_ids_alone_keeps_a_closed_tab_of_another_server :
    let shown : Session := ⟨1, 1⟩
    let farCopy : Session := ⟨2, 7⟩
    let closed : Session := ⟨1, 7⟩
    Tab.session closed ∈ pruneWith sameId shown [farCopy] [.session shown, .session closed] ∧
      Tab.session closed ∉ prune shown [farCopy] [.session shown, .session closed] := by
  decide
