structure Msg where
  id : Nat
  done : Bool
  prs : List Nat
  deriving DecidableEq, Repr

def found (msgs : List Msg) : List Nat := msgs.flatMap Msg.prs

def stableOf (msgs : List Msg) : List Msg := msgs.takeWhile Msg.done

def lastId (msgs : List Msg) : Option Nat := msgs.getLast?.map Msg.id

structure Scan where
  boundary : Option Nat
  kept : List Nat

def known (s : Scan) (log : List Msg) : Bool :=
  match s.boundary with
  | none => true
  | some b => (log.map Msg.id).contains b

def after (boundary : Option Nat) (log : List Msg) : List Msg :=
  match boundary with
  | none => log
  | some b => log.filter (fun m => decide (b < m.id))

def look (s : Scan) (log : List Msg) : List Nat × Scan :=
  let base : Scan := if known s log then s else ⟨none, []⟩
  let msgs := after base.boundary log
  let stable := stableOf msgs
  (base.kept ++ found msgs, ⟨(lastId stable).or base.boundary, base.kept ++ found stable⟩)

structure Server where
  log : List Msg
  next : Nat

inductive Change where
  | add (done : Bool) (prs : List Nat)
  | update (id : Nat) (done : Bool) (prs : List Nat)
  | revert (keep : Nat)

def edit (id : Nat) (done : Bool) (prs : List Nat) (m : Msg) : Msg :=
  if m.id = id ∧ m.done = false then ⟨m.id, done, prs⟩ else m

def Change.apply : Change → Server → Server
  | .add done prs, s => ⟨s.log ++ [⟨s.next, done, prs⟩], s.next + 1⟩
  | .update id done prs, s => ⟨s.log.map (edit id done prs), s.next⟩
  | .revert keep, s => ⟨s.log.take keep, s.next⟩

def Ordered (msgs : List Msg) : Prop := msgs.Pairwise (fun a b => a.id < b.id)

def WellFormed (s : Server) : Prop := Ordered s.log ∧ ∀ m ∈ s.log, m.id < s.next

def Remembers (scan : Scan) (s : Server) : Prop :=
  ∃ seen : List Msg,
    (∀ m ∈ seen, m.done = true) ∧ Ordered seen ∧ (∀ m ∈ seen, m.id < s.next) ∧
    scan.kept = found seen ∧ scan.boundary = lastId seen ∧
    (seen <+: s.log ∨ ∀ b, scan.boundary = some b → b ∉ s.log.map Msg.id)

theorem found_append (a b : List Msg) : found (a ++ b) = found a ++ found b := by
  simp [found, List.flatMap_append]

theorem lastId_concat (q : List Msg) (x : Msg) : lastId (q ++ [x]) = some x.id := by
  simp [lastId]

theorem after_past_seen (q r : List Msg) (x : Msg) (ordered : Ordered (q ++ [x] ++ r)) :
    after (some x.id) (q ++ [x] ++ r) = r := by
  have parts := List.pairwise_append.mp ordered
  have front := List.pairwise_append.mp parts.1
  have newer : ∀ m ∈ r, x.id < m.id := fun m mem => parts.2.2 x (by simp) m mem
  have older : ∀ m ∈ q, m.id < x.id := fun m mem => front.2.2 m mem x (by simp)
  show (q ++ [x] ++ r).filter (fun m => decide (x.id < m.id)) = r
  rw [List.filter_append]
  have dropFront : (q ++ [x]).filter (fun m => decide (x.id < m.id)) = [] := by
    apply List.filter_eq_nil_iff.mpr
    intro m mem
    simp only [List.mem_append, List.mem_singleton] at mem
    rcases mem with inQ | isX
    · have := older m inQ
      simp only [decide_eq_true_eq]
      omega
    · subst isX
      simp
  have keepRest : r.filter (fun m => decide (x.id < m.id)) = r :=
    List.filter_eq_self.mpr (fun m mem => by simpa using newer m mem)
  rw [dropFront, keepRest]
  rfl

theorem stableOf_after_done (seen rest : List Msg) (allDone : ∀ m ∈ seen, m.done = true) :
    stableOf (seen ++ rest) = seen ++ stableOf rest := by
  simp [stableOf, List.takeWhile_append_of_pos allDone]

theorem stableOf_done (msgs : List Msg) : ∀ m ∈ stableOf msgs, m.done = true := by
  intro m mem
  induction msgs with
  | nil => simp [stableOf] at mem
  | cons y ys rest =>
    by_cases d : y.done = true
    · simp only [stableOf, List.takeWhile_cons_of_pos d, List.mem_cons] at mem
      rcases mem with isY | inRest
      · rw [isY]; exact d
      · exact rest inRest
    · simp [stableOf, List.takeWhile_cons_of_neg d] at mem

theorem stableOf_prefix (msgs : List Msg) : stableOf msgs <+: msgs := by
  simp [stableOf, List.takeWhile_prefix]

theorem ordered_of_prefix {a b : List Msg} (pre : a <+: b) (ordered : Ordered b) : Ordered a :=
  ordered.sublist pre.sublist

theorem lastId_or (seen rest : List Msg) :
    (lastId rest).or (lastId seen) = lastId (seen ++ rest) := by
  cases rest with
  | nil => simp [lastId]
  | cons y ys =>
    simp only [lastId, List.getLast?_append, List.getLast?_eq_some_getLast (List.cons_ne_nil y ys)]
    simp

theorem full_look (scan : Scan) (log : List Msg) (unknown : known scan log = false) :
    look scan log = (found log, ⟨lastId (stableOf log), found (stableOf log)⟩) := by
  simp [look, unknown, after]

theorem look_from_seen (seen rest : List Msg) (scan : Scan) (ordered : Ordered (seen ++ rest))
    (allDone : ∀ m ∈ seen, m.done = true) (kept : scan.kept = found seen) (boundary : scan.boundary = lastId seen) :
    look scan (seen ++ rest) =
      (found (seen ++ rest), ⟨lastId (seen ++ stableOf rest), found (seen ++ stableOf rest)⟩) := by
  rcases List.eq_nil_or_concat seen with empty | ⟨q, x, split⟩
  · subst empty
    have noBoundary : scan.boundary = none := by simpa [lastId] using boundary
    simp [look, known, noBoundary, after, kept, found]
  · subst split
    simp only [List.concat_eq_append] at ordered allDone kept boundary ⊢
    have isKnown : known scan (q ++ [x] ++ rest) = true := by
      simp [known, boundary, lastId_concat]
    have newer := after_past_seen q rest x ordered
    have stable := lastId_or (q ++ [x]) (stableOf rest)
    rw [lastId_concat] at stable
    simp only [look, isKnown, ↓reduceIte]
    rw [boundary, lastId_concat, newer, kept, stable, ← found_append, ← found_append]

theorem seen_empty_of_no_boundary (seen : List Msg) (none : lastId seen = none) : seen = [] := by
  cases seen with
  | nil => rfl
  | cons y ys => simp [lastId] at none

theorem seen_prefix_of_known (scan : Scan) (s : Server) (seen : List Msg) (boundary : scan.boundary = lastId seen)
    (located : seen <+: s.log ∨ ∀ b, scan.boundary = some b → b ∉ s.log.map Msg.id)
    (isKnown : known scan s.log = true) : seen <+: s.log := by
  rcases located with pre | gone
  · exact pre
  · cases hb : scan.boundary with
    | none =>
      rw [hb] at boundary
      rw [seen_empty_of_no_boundary seen boundary.symm]
      exact List.nil_prefix
    | some b =>
      simp [known, hb] at isKnown
      exact absurd isKnown (by simpa using gone b hb)

theorem look_correct (scan : Scan) (s : Server) (wf : WellFormed s) (mem : Remembers scan s) :
    (look scan s.log).1 = found s.log := by
  obtain ⟨seen, allDone, _, _, kept, boundary, located⟩ := mem
  by_cases isKnown : known scan s.log = true
  · obtain ⟨rest, split⟩ := seen_prefix_of_known scan s seen boundary located isKnown
    have ordered := wf.1
    rw [← split] at ordered ⊢
    rw [look_from_seen seen rest scan ordered allDone kept boundary]
  · rw [full_look scan s.log (by simpa using isKnown)]

theorem look_remembers (scan : Scan) (s : Server) (wf : WellFormed s) (mem : Remembers scan s) :
    Remembers (look scan s.log).2 s := by
  obtain ⟨seen, allDone, _, _, kept, boundary, located⟩ := mem
  by_cases isKnown : known scan s.log = true
  · obtain ⟨rest, split⟩ := seen_prefix_of_known scan s seen boundary located isKnown
    have ordered := wf.1
    rw [← split] at ordered
    have pre : seen ++ stableOf rest <+: s.log := by
      rw [← split]
      obtain ⟨tail, rebuilt⟩ := stableOf_prefix rest
      exact ⟨tail, by rw [List.append_assoc, rebuilt]⟩
    rw [← split, look_from_seen seen rest scan ordered allDone kept boundary, split]
    refine ⟨seen ++ stableOf rest, ?_, ordered_of_prefix pre wf.1, fun m mem => wf.2 m (pre.subset mem), rfl, rfl,
      Or.inl pre⟩
    intro m mem
    rcases List.mem_append.mp mem with inSeen | inStable
    · exact allDone m inSeen
    · exact stableOf_done rest m inStable
  · rw [full_look scan s.log (by simpa using isKnown)]
    exact ⟨stableOf s.log, stableOf_done s.log, ordered_of_prefix (stableOf_prefix s.log) wf.1,
      fun m mem => wf.2 m ((stableOf_prefix s.log).subset mem), rfl, rfl, Or.inl (stableOf_prefix s.log)⟩

theorem edit_id (id : Nat) (done : Bool) (prs : List Nat) (m : Msg) : (edit id done prs m).id = m.id := by
  unfold edit
  split <;> rfl

theorem edit_finished (id : Nat) (done : Bool) (prs : List Nat) (m : Msg) (finished : m.done = true) :
    edit id done prs m = m := by
  simp [edit, finished]

theorem ids_after_update (id : Nat) (done : Bool) (prs : List Nat) (log : List Msg) :
    (log.map (edit id done prs)).map Msg.id = log.map Msg.id := by
  simp only [List.map_map]
  exact List.map_congr_left (fun m _ => edit_id id done prs m)

theorem change_wellFormed (c : Change) (s : Server) (wf : WellFormed s) : WellFormed (c.apply s) := by
  cases c with
  | add done prs =>
    refine ⟨List.pairwise_append.mpr ⟨wf.1, by simp, fun a mem b isNew => ?_⟩, fun m mem => ?_⟩
    · simp only [List.mem_singleton] at isNew
      subst isNew
      exact wf.2 a mem
    · rcases List.mem_append.mp mem with old | isNew
      · exact Nat.lt_succ_of_lt (wf.2 m old)
      · simp only [List.mem_singleton] at isNew
        subst isNew
        exact Nat.lt_succ_self s.next
  | update id done prs =>
    refine ⟨?_, fun m mem => ?_⟩
    · exact List.pairwise_map.mpr (wf.1.imp (fun {a b} lt => by simpa [edit_id] using lt))
    · obtain ⟨original, inLog, edited⟩ := List.mem_map.mp mem
      rw [← edited, edit_id]
      exact wf.2 original inLog
  | revert keep =>
    exact ⟨wf.1.sublist (List.take_sublist keep s.log), fun m mem => wf.2 m (List.mem_of_mem_take mem)⟩

theorem change_remembers (c : Change) (scan : Scan) (s : Server) (mem : Remembers scan s) :
    Remembers scan (c.apply s) := by
  obtain ⟨seen, allDone, ordered, below, kept, boundary, located⟩ := mem
  have boundaryBelow : ∀ b, scan.boundary = some b → b < s.next := by
    intro b hb
    rw [boundary] at hb
    rcases List.eq_nil_or_concat seen with empty | ⟨q, x, split⟩
    · subst empty
      simp [lastId] at hb
    · subst split
      simp only [List.concat_eq_append, lastId_concat, Option.some.injEq] at hb
      subst hb
      exact below x (by simp)
  cases c with
  | add done prs =>
    refine ⟨seen, allDone, ordered, fun m inSeen => Nat.lt_succ_of_lt (below m inSeen), kept, boundary, ?_⟩
    rcases located with pre | gone
    · exact Or.inl (List.prefix_append_of_prefix pre)
    · refine Or.inr (fun b hb inLog => ?_)
      simp only [Change.apply, List.map_append, List.map_cons, List.map_nil, List.mem_append,
        List.mem_singleton] at inLog
      rcases inLog with old | isNew
      · exact gone b hb old
      · have := boundaryBelow b hb
        omega
  | update id done prs =>
    refine ⟨seen, allDone, ordered, below, kept, boundary, ?_⟩
    rcases located with ⟨rest, split⟩ | gone
    · refine Or.inl ⟨rest.map (edit id done prs), ?_⟩
      simp only [Change.apply, ← split, List.map_append]
      rw [List.map_congr_left (fun m inSeen => edit_finished id done prs m (allDone m inSeen))]
      simp
    · refine Or.inr (fun b hb inLog => gone b hb ?_)
      simpa [Change.apply, edit_id] using inLog
  | revert keep =>
    refine ⟨seen, allDone, ordered, below, kept, boundary, ?_⟩
    rcases located with ⟨rest, split⟩ | gone
    · by_cases long : seen.length ≤ keep
      · refine Or.inl ⟨rest.take (keep - seen.length), ?_⟩
        simp only [Change.apply, ← split, List.take_append, List.take_of_length_le long]
      · refine Or.inr (fun b hb inLog => ?_)
        rw [boundary] at hb
        rcases List.eq_nil_or_concat seen with empty | ⟨q, x, cut⟩
        · subst empty
          simp [lastId] at hb
        · subst cut
          simp only [List.concat_eq_append, lastId_concat, Option.some.injEq] at hb ordered long split
          subst hb
          simp only [Change.apply, ← split] at inLog
          have short : keep ≤ q.length := by simp at long; omega
          rw [List.take_append_of_le_length (by simp; omega), List.take_append_of_le_length short] at inLog
          obtain ⟨m, inTake, sameId⟩ := List.mem_map.mp inLog
          have inQ := List.mem_of_mem_take inTake
          have lower := (List.pairwise_append.mp ordered).2.2 m inQ x (by simp)
          omega
    · refine Or.inr (fun b hb inLog => gone b hb ?_)
      obtain ⟨m, inTake, sameId⟩ := List.mem_map.mp inLog
      exact List.mem_map.mpr ⟨m, List.mem_of_mem_take inTake, sameId⟩

theorem nothing_remembered (s : Server) : Remembers ⟨none, []⟩ s :=
  ⟨[], by simp, List.Pairwise.nil, by simp, rfl, rfl, Or.inl List.nil_prefix⟩

inductive Event where
  | look
  | change (c : Change)

def EveryLookReadsAll : List Event → Server → Scan → Prop
  | [], _, _ => True
  | .look :: rest, s, scan => (look scan s.log).1 = found s.log ∧ EveryLookReadsAll rest s (look scan s.log).2
  | .change c :: rest, s, scan => EveryLookReadsAll rest (c.apply s) scan

theorem every_look_reads_all_from (events : List Event) :
    ∀ (s : Server) (scan : Scan), WellFormed s → Remembers scan s → EveryLookReadsAll events s scan := by
  induction events with
  | nil => intros; trivial
  | cons event rest more =>
    intro s scan wf mem
    cases event with
    | look => exact ⟨look_correct scan s wf mem, more s _ wf (look_remembers scan s wf mem)⟩
    | change c => exact more _ scan (change_wellFormed c s wf) (change_remembers c scan s mem)

theorem every_look_lists_what_a_full_read_would (events : List Event) :
    EveryLookReadsAll events ⟨[], 0⟩ ⟨none, []⟩ :=
  every_look_reads_all_from events ⟨[], 0⟩ ⟨none, []⟩ ⟨List.Pairwise.nil, by simp⟩
    (nothing_remembered ⟨[], 0⟩)

def lookTrustingBoundary (s : Scan) (log : List Msg) : List Nat × Scan :=
  let msgs := after s.boundary log
  let stable := stableOf msgs
  (s.kept ++ found msgs, ⟨(lastId stable).or s.boundary, s.kept ++ found stable⟩)

def lookPastUnfinished (s : Scan) (log : List Msg) : List Nat × Scan :=
  let base : Scan := if known s log then s else ⟨none, []⟩
  let msgs := after base.boundary log
  let stable := msgs.filter Msg.done
  (base.kept ++ found msgs, ⟨(lastId stable).or base.boundary, base.kept ++ found stable⟩)

theorem trusting_the_boundary_keeps_a_reverted_pull_request :
    let first := look ⟨none, []⟩ [⟨0, true, [7]⟩]
    let afterRevert := [(⟨1, true, []⟩ : Msg)]
    (lookTrustingBoundary first.2 afterRevert).1 = [7] ∧ found afterRevert = [] ∧
      (look first.2 afterRevert).1 = [] := by
  decide

theorem a_boundary_past_an_unfinished_reply_misses_its_pull_request :
    let first := [(⟨0, false, []⟩ : Msg), ⟨1, true, []⟩]
    let finished := [(⟨0, true, [7]⟩ : Msg), ⟨1, true, []⟩]
    (lookPastUnfinished (lookPastUnfinished ⟨none, []⟩ first).2 finished).1 = [] ∧ found finished = [7] ∧
      (look (look ⟨none, []⟩ first).2 finished).1 = [7] := by
  decide
