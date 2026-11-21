inductive Value where
  | str (s : String)
  | int (i : Int)
  | bool (b : Bool)
  | noneValue
  | list (elems : List Value)
  | dict (entries : List (Value × Value))
  | obj (kind : String) (id : Nat)
  | box (inner : Value)
  | fail (message : String)

inductive FailureUnbox where
  | opaque
  | message

def Value.isBox : Value → Bool
  | .box _ => true
  | _ => false

def Value.isFail : Value → Bool
  | .fail _ => true
  | _ => false

def unbox (policy : FailureUnbox) : Value → Value
  | .box inner => inner
  | .fail message =>
    match policy with
    | .opaque => .fail message
    | .message => .str message
  | v => v

mutual
def starEq : Value → Value → Bool
  | .str a, .str b => a == b
  | .int a, .int b => a == b
  | .bool a, .bool b => a == b
  | .noneValue, .noneValue => true
  | .list xs, .list ys => listEq xs ys
  | .dict xs, .dict ys => xs.length == ys.length && dictWithin xs ys
  | .obj kind id, .obj kind' id' => kind == kind' && id == id'
  | .box a, .box b => starEq a b
  | .fail a, .fail b => a == b
  | _, _ => false
termination_by a b => sizeOf a + sizeOf b

def listEq : List Value → List Value → Bool
  | [], [] => true
  | x :: xs, y :: ys => starEq x y && listEq xs ys
  | _, _ => false
termination_by xs ys => sizeOf xs + sizeOf ys

def dictWithin : List (Value × Value) → List (Value × Value) → Bool
  | [], _ => true
  | (k, v) :: rest, ys => entryMatches k v ys && dictWithin rest ys
termination_by xs ys => sizeOf xs + sizeOf ys

def entryMatches (k v : Value) : List (Value × Value) → Bool
  | [] => false
  | (k', w) :: rest => if starEq k k' then starEq v w else entryMatches k v rest
termination_by ys => sizeOf k + sizeOf v + sizeOf ys
end

mutual
def ordered : Value → Value → Option Ordering
  | .str a, .str b => some (compare a b)
  | .int a, .int b => some (compare a b)
  | .bool a, .bool b => some (compare a.toNat b.toNat)
  | .list xs, .list ys => orderedList xs ys
  | .box a, .box b => ordered a b
  | _, _ => none

def orderedList : List Value → List Value → Option Ordering
  | [], [] => some .eq
  | [], _ :: _ => some .lt
  | _ :: _, [] => some .gt
  | x :: xs, y :: ys => if starEq x y then orderedList xs ys else ordered x y
end

inductive CmpOp where
  | eq
  | ne
  | lt
  | le
  | gt
  | ge

def compareValues : CmpOp → Value → Value → Option Bool
  | .eq, a, b => some (starEq a b)
  | .ne, a, b => some (!starEq a b)
  | .lt, a, b => (ordered a b).map (· == .lt)
  | .le, a, b => (ordered a b).map (· != .gt)
  | .gt, a, b => (ordered a b).map (· == .gt)
  | .ge, a, b => (ordered a b).map (· != .lt)

def isInfix (needle : List Char) : List Char → Bool
  | [] => needle.isEmpty
  | c :: cs => needle.isPrefixOf (c :: cs) || isInfix needle cs

def memberValues (needle : Value) : Value → Option Bool
  | .list elems => some (elems.any fun e => starEq e needle)
  | .dict entries => some (entries.any fun entry => starEq needle entry.1)
  | .str haystack =>
    match needle with
    | .str s => some (isInfix s.toList haystack.toList)
    | _ => none
  | .box inner => memberValues needle inner
  | _ => none

def memberOp (negated : Bool) (needle container : Value) : Option Bool :=
  (memberValues needle container).map (· != negated)

def Value.elems : Value → List Value
  | .list es => es
  | .dict entries => entries.map Prod.fst
  | _ => []

def unboxElems (policy : FailureUnbox) : Value → Value
  | .list es => .list (es.map (unbox policy))
  | .dict entries => .dict (entries.map fun entry => (unbox policy entry.1, entry.2))
  | v => v

def compareRewritten (policy : FailureUnbox) (op : CmpOp) (a b : Value) : Option Bool :=
  compareValues op (unbox policy a) (unbox policy b)

def memberRewritten (policy : FailureUnbox) (negated : Bool) (needle container : Value) : Option Bool :=
  memberOp negated (unbox policy needle) (unboxElems policy (unbox policy container))

def truth : Value → Bool
  | .str s => !s.isEmpty
  | .int i => i != 0
  | .bool b => b
  | .noneValue => false
  | .list es => !es.isEmpty
  | .dict entries => !entries.isEmpty
  | .obj _ _ => true
  | .box inner => truth inner
  | .fail _ => false

inductive Expr where
  | lit (v : Value)
  | var (name : String)
  | call (tool : String) (arg : Expr)
  | cmp (op : CmpOp) (lhs rhs : Expr)
  | member (negated : Bool) (needle container : Expr)
  | unbox (e : Expr)
  | unboxElems (e : Expr)
  | not (e : Expr)
  | and (lhs rhs : Expr)
  | or (lhs rhs : Expr)
  | ite (cond yes no : Expr)

structure World where
  env : String → Value
  host : String → Value → Value
  policy : FailureUnbox

structure Run where
  log : List String
  value : Option Value

def Run.bind : Run → (List String → Value → Run) → Run
  | ⟨log, some v⟩, k => k log v
  | ⟨log, none⟩, _ => ⟨log, none⟩

def eval (w : World) : Expr → List String → Run
  | .lit v, log => ⟨log, some v⟩
  | .var name, log => ⟨log, some (w.env name)⟩
  | .call tool arg, log =>
    (eval w arg log).bind fun log v => ⟨log ++ [tool], some (w.host tool v)⟩
  | .cmp op lhs rhs, log =>
    (eval w lhs log).bind fun log a =>
      (eval w rhs log).bind fun log b => ⟨log, (compareValues op a b).map .bool⟩
  | .member negated needle container, log =>
    (eval w needle log).bind fun log n =>
      (eval w container log).bind fun log c => ⟨log, (memberOp negated n c).map .bool⟩
  | .unbox e, log => (eval w e log).bind fun log v => ⟨log, some (unbox w.policy v)⟩
  | .unboxElems e, log =>
    (eval w e log).bind fun log v => ⟨log, some (unboxElems w.policy (unbox w.policy v))⟩
  | .not e, log => (eval w e log).bind fun log v => ⟨log, some (.bool (!truth v))⟩
  | .and lhs rhs, log =>
    (eval w lhs log).bind fun log a => if truth a then eval w rhs log else ⟨log, some a⟩
  | .or lhs rhs, log =>
    (eval w lhs log).bind fun log a => if truth a then ⟨log, some a⟩ else eval w rhs log
  | .ite cond yes no, log =>
    (eval w cond log).bind fun log v => if truth v then eval w yes log else eval w no log

def rewrite : Expr → Expr
  | .lit v => .lit v
  | .var name => .var name
  | .call tool arg => .call tool (rewrite arg)
  | .cmp op lhs rhs => .cmp op (.unbox (rewrite lhs)) (.unbox (rewrite rhs))
  | .member negated needle container =>
    .member negated (.unbox (rewrite needle)) (.unboxElems (rewrite container))
  | .unbox e => .unbox (rewrite e)
  | .unboxElems e => .unboxElems (rewrite e)
  | .not e => .not (rewrite e)
  | .and lhs rhs => .and (rewrite lhs) (rewrite rhs)
  | .or lhs rhs => .or (rewrite lhs) (rewrite rhs)
  | .ite cond yes no => .ite (rewrite cond) (rewrite yes) (rewrite no)

def erase : Expr → Expr
  | .lit v => .lit v
  | .var name => .var name
  | .call tool arg => .call tool (erase arg)
  | .cmp op lhs rhs => .cmp op (erase lhs) (erase rhs)
  | .member negated needle container => .member negated (erase needle) (erase container)
  | .unbox e => erase e
  | .unboxElems e => erase e
  | .not e => .not (erase e)
  | .and lhs rhs => .and (erase lhs) (erase rhs)
  | .or lhs rhs => .or (erase lhs) (erase rhs)
  | .ite cond yes no => .ite (erase cond) (erase yes) (erase no)

def scriptSource : Expr → Bool
  | .lit _ => true
  | .var _ => true
  | .call _ arg => scriptSource arg
  | .cmp _ lhs rhs => scriptSource lhs && scriptSource rhs
  | .member _ needle container => scriptSource needle && scriptSource container
  | .unbox _ => false
  | .unboxElems _ => false
  | .not e => scriptSource e
  | .and lhs rhs => scriptSource lhs && scriptSource rhs
  | .or lhs rhs => scriptSource lhs && scriptSource rhs
  | .ite cond yes no => scriptSource cond && scriptSource yes && scriptSource no

def unboxedExactlyAtComparisons : Expr → Bool
  | .lit _ => true
  | .var _ => true
  | .call _ arg => unboxedExactlyAtComparisons arg
  | .cmp _ (.unbox lhs) (.unbox rhs) => unboxedExactlyAtComparisons lhs && unboxedExactlyAtComparisons rhs
  | .cmp _ _ _ => false
  | .member _ (.unbox needle) (.unboxElems container) =>
    unboxedExactlyAtComparisons needle && unboxedExactlyAtComparisons container
  | .member _ _ _ => false
  | .unbox _ => false
  | .unboxElems _ => false
  | .not e => unboxedExactlyAtComparisons e
  | .and lhs rhs => unboxedExactlyAtComparisons lhs && unboxedExactlyAtComparisons rhs
  | .or lhs rhs => unboxedExactlyAtComparisons lhs && unboxedExactlyAtComparisons rhs
  | .ite cond yes no =>
    unboxedExactlyAtComparisons cond && unboxedExactlyAtComparisons yes && unboxedExactlyAtComparisons no

def Settled (policy : FailureUnbox) (v : Value) : Prop :=
  unbox policy v = v ∧ ∀ e ∈ v.elems, unbox policy e = e

def literalsSettled (policy : FailureUnbox) : Expr → Prop
  | .lit v => Settled policy v
  | .var _ => True
  | .call _ arg => literalsSettled policy arg
  | .cmp _ lhs rhs => literalsSettled policy lhs ∧ literalsSettled policy rhs
  | .member _ needle container => literalsSettled policy needle ∧ literalsSettled policy container
  | .unbox e => literalsSettled policy e
  | .unboxElems e => literalsSettled policy e
  | .not e => literalsSettled policy e
  | .and lhs rhs => literalsSettled policy lhs ∧ literalsSettled policy rhs
  | .or lhs rhs => literalsSettled policy lhs ∧ literalsSettled policy rhs
  | .ite cond yes no => literalsSettled policy cond ∧ literalsSettled policy yes ∧ literalsSettled policy no

@[simp] theorem unbox_box (policy : FailureUnbox) (inner : Value) : unbox policy (.box inner) = inner := rfl
@[simp] theorem unbox_fail_opaque (m : String) : unbox .opaque (.fail m) = .fail m := rfl
@[simp] theorem unbox_fail_message (m : String) : unbox .message (.fail m) = .str m := rfl
@[simp] theorem unbox_str (policy : FailureUnbox) (s : String) : unbox policy (.str s) = .str s := rfl
@[simp] theorem unbox_int (policy : FailureUnbox) (i : Int) : unbox policy (.int i) = .int i := rfl
@[simp] theorem unbox_bool (policy : FailureUnbox) (b : Bool) : unbox policy (.bool b) = .bool b := rfl
@[simp] theorem unbox_noneValue (policy : FailureUnbox) : unbox policy .noneValue = .noneValue := rfl
@[simp] theorem unbox_list (policy : FailureUnbox) (es : List Value) : unbox policy (.list es) = .list es := rfl
@[simp] theorem unbox_dict (policy : FailureUnbox) (entries : List (Value × Value)) :
    unbox policy (.dict entries) = .dict entries := rfl
@[simp] theorem unbox_obj (policy : FailureUnbox) (kind : String) (id : Nat) :
    unbox policy (.obj kind id) = .obj kind id := rfl

@[simp] theorem unboxElems_list (policy : FailureUnbox) (es : List Value) :
    unboxElems policy (.list es) = .list (es.map (unbox policy)) := rfl
@[simp] theorem unboxElems_dict (policy : FailureUnbox) (entries : List (Value × Value)) :
    unboxElems policy (.dict entries) = .dict (entries.map fun entry => (unbox policy entry.1, entry.2)) := rfl
@[simp] theorem unboxElems_str (policy : FailureUnbox) (s : String) : unboxElems policy (.str s) = .str s := rfl

theorem unbox_opaque_of_not_box {v : Value} (h : v.isBox = false) : unbox .opaque v = v := by
  cases v <;> simp_all [Value.isBox]

theorem unbox_idem (policy : FailureUnbox) (v : Value)
    (notNested : ∀ inner, v = .box inner → inner.isBox = false ∧ inner.isFail = false) :
    unbox policy (unbox policy v) = unbox policy v ∧ (unbox policy v).isBox = false := by
  cases v with
  | box inner =>
    obtain ⟨notBox, notFail⟩ := notNested inner rfl
    cases inner <;> simp_all [Value.isBox, Value.isFail]
  | fail m => cases policy <;> simp [Value.isBox]
  | _ => simp [Value.isBox]

theorem map_unbox_self {policy : FailureUnbox} :
    ∀ {es : List Value}, (∀ e ∈ es, unbox policy e = e) → es.map (unbox policy) = es
  | [], _ => rfl
  | e :: es, h => by
    rw [List.map_cons, h e (List.mem_cons_self ..), map_unbox_self (fun x hx => h x (List.mem_cons_of_mem _ hx))]

theorem map_unbox_keys_self {policy : FailureUnbox} :
    ∀ {entries : List (Value × Value)}, (∀ k ∈ entries.map Prod.fst, unbox policy k = k) →
      entries.map (fun entry => (unbox policy entry.1, entry.2)) = entries
  | [], _ => rfl
  | (k, v) :: rest, h => by
    rw [List.map_cons, h k (List.mem_cons_self ..),
      map_unbox_keys_self (fun x hx => h x (List.mem_cons_of_mem _ hx))]

theorem unboxElems_settled {policy : FailureUnbox} {v : Value} (h : ∀ e ∈ v.elems, unbox policy e = e) :
    unboxElems policy v = v := by
  cases v with
  | list es => simp only [Value.elems] at h; simp only [unboxElems_list, map_unbox_self h]
  | dict entries => simp only [Value.elems] at h; simp only [unboxElems_dict, map_unbox_keys_self h]
  | _ => rfl

theorem starEq_fail_left (m : String) {x : Value} (h : x.isFail = false) : starEq (.fail m) x = false := by
  cases x <;> simp_all [starEq, Value.isFail]

theorem starEq_fail_right (m : String) {x : Value} (h : x.isFail = false) : starEq x (.fail m) = false := by
  cases x <;> simp_all [starEq, Value.isFail]

theorem ordered_fail_left (m : String) (x : Value) : ordered (.fail m) x = none := by
  cases x <;> rfl

theorem ordered_fail_right (m : String) (x : Value) : ordered x (.fail m) = none := by
  cases x <;> rfl

theorem compare_conservative (policy : FailureUnbox) (op : CmpOp) (a b : Value)
    (plainA : unbox policy a = a) (plainB : unbox policy b = b) :
    compareRewritten policy op a b = compareValues op a b := by
  simp [compareRewritten, plainA, plainB]

theorem compare_conservative_without_boxes (op : CmpOp) (a b : Value) (noBoxA : a.isBox = false)
    (noBoxB : b.isBox = false) :
    compareRewritten .opaque op a b = compareValues op a b :=
  compare_conservative _ _ _ _ (unbox_opaque_of_not_box noBoxA) (unbox_opaque_of_not_box noBoxB)

theorem member_conservative (policy : FailureUnbox) (negated : Bool) (needle container : Value)
    (plainNeedle : unbox policy needle = needle)
    (plainContainer : unbox policy container = container ∨ container.isBox = true)
    (plainElems : ∀ e ∈ (unbox policy container).elems, unbox policy e = e) :
    memberRewritten policy negated needle container = memberOp negated needle container := by
  unfold memberRewritten memberOp
  rw [plainNeedle, unboxElems_settled plainElems]
  rcases plainContainer with same | isBox
  · rw [same]
  · cases container <;> simp_all [Value.isBox, memberValues]

theorem member_conservative_without_boxes (negated : Bool) (needle container : Value)
    (noBoxNeedle : needle.isBox = false) (noBoxContainer : container.isBox = false)
    (noBoxElems : ∀ e ∈ container.elems, e.isBox = false) :
    memberRewritten .opaque negated needle container = memberOp negated needle container := by
  have plainContainer := unbox_opaque_of_not_box noBoxContainer
  apply member_conservative _ _ _ _ (unbox_opaque_of_not_box noBoxNeedle) (.inl plainContainer)
  rw [plainContainer]
  exact fun e he => unbox_opaque_of_not_box (noBoxElems e he)

theorem compare_box_left (policy : FailureUnbox) (op : CmpOp) (inner b : Value) (plainB : unbox policy b = b) :
    compareRewritten policy op (.box inner) b = compareValues op inner b := by
  simp [compareRewritten, plainB]

theorem compare_box_right (policy : FailureUnbox) (op : CmpOp) (a inner : Value) (plainA : unbox policy a = a) :
    compareRewritten policy op a (.box inner) = compareValues op a inner := by
  simp [compareRewritten, plainA]

theorem compare_box_box (policy : FailureUnbox) (op : CmpOp) (a b : Value) :
    compareRewritten policy op (.box a) (.box b) = compareValues op a b ∧
    compareRewritten policy op (.box a) (.box b) = compareValues op (.box a) (.box b) := by
  cases op <;> simp [compareRewritten, compareValues, starEq, ordered]

theorem ne_is_not_eq (policy : FailureUnbox) (a b : Value) :
    compareRewritten policy .ne a b = (compareRewritten policy .eq a b).map (!·) := by
  simp [compareRewritten, compareValues]

theorem box_text_eq (policy : FailureUnbox) (s s' : String) :
    compareRewritten policy .eq (.box (.str s)) (.str s') = some (s == s') ∧
    compareRewritten policy .eq (.str s') (.box (.str s)) = some (s' == s) := by
  simp [compareRewritten, compareValues, starEq]

theorem box_text_compares_as_text (policy : FailureUnbox) (op : CmpOp) (s s' : String) :
    compareRewritten policy op (.box (.str s)) (.str s') = compareValues op (.str s) (.str s') ∧
    compareRewritten policy op (.str s) (.box (.str s')) = compareValues op (.str s) (.str s') ∧
    compareRewritten policy op (.box (.str s)) (.box (.str s')) = compareValues op (.str s) (.str s') :=
  ⟨compare_box_left _ _ _ _ rfl, compare_box_right _ _ _ _ rfl, (compare_box_box _ _ _ _).1⟩

theorem text_order_is_string_order (s s' : String) :
    compareValues .lt (.str s) (.str s') = some (compare s s' == .lt) ∧
    compareValues .le (.str s) (.str s') = some (compare s s' != .gt) ∧
    compareValues .gt (.str s) (.str s') = some (compare s s' == .gt) ∧
    compareValues .ge (.str s) (.str s') = some (compare s s' != .lt) := by
  simp [compareValues, ordered]

theorem old_eq_box_text_false : compareValues .eq (.box (.str "a")) (.str "a") = some false := by
  simp [compareValues, starEq]

theorem new_eq_box_text_true (policy : FailureUnbox) :
    compareRewritten policy .eq (.box (.str "a")) (.str "a") = some true := by
  simp [compareRewritten, compareValues, starEq]

theorem old_lt_box_text_raises : compareValues .lt (.box (.str "a")) (.str "b") = none := by
  simp [compareValues, ordered]

theorem new_lt_box_text_true (policy : FailureUnbox) :
    compareRewritten policy .lt (.box (.str "a")) (.str "b") = some true := by
  simp [compareRewritten, compareValues, ordered]
  decide

theorem member_elements_list (policy : FailureUnbox) (negated : Bool) (needle : Value) (es : List Value) :
    memberRewritten policy negated needle (.list es) =
      some ((es.any fun e => starEq (unbox policy e) (unbox policy needle)) != negated) := by
  simp [memberRewritten, memberOp, memberValues, List.any_map, Function.comp_def]

theorem member_keys_dict (policy : FailureUnbox) (negated : Bool) (needle : Value)
    (entries : List (Value × Value)) :
    memberRewritten policy negated needle (.dict entries) =
      some ((entries.any fun entry => starEq (unbox policy needle) (unbox policy entry.1)) != negated) := by
  simp [memberRewritten, memberOp, memberValues, List.any_map, Function.comp_def]

theorem not_in_is_not_in (policy : FailureUnbox) (needle container : Value) :
    memberRewritten policy true needle container = (memberRewritten policy false needle container).map (!·) := by
  simp only [memberRewritten, memberOp]
  cases memberValues (unbox policy needle) (unboxElems policy (unbox policy container)) <;> simp

theorem member_box_list (policy : FailureUnbox) (negated : Bool) (inner : Value) (es : List Value)
    (plainElems : ∀ e ∈ es, unbox policy e = e) :
    memberRewritten policy negated (.box inner) (.list es) = memberOp negated inner (.list es) := by
  simp only [memberRewritten, unbox_box, unbox_list, unboxElems_list, map_unbox_self plainElems]

theorem member_box_dict (policy : FailureUnbox) (negated : Bool) (inner : Value) (entries : List (Value × Value))
    (plainKeys : ∀ k ∈ entries.map Prod.fst, unbox policy k = k) :
    memberRewritten policy negated (.box inner) (.dict entries) = memberOp negated inner (.dict entries) := by
  simp only [memberRewritten, unbox_box, unbox_dict, unboxElems_dict, map_unbox_keys_self plainKeys]

theorem member_box_text (policy : FailureUnbox) (negated : Bool) (s haystack : String) :
    memberRewritten policy negated (.box (.str s)) (.str haystack) = memberOp negated (.str s) (.str haystack) ∧
    memberRewritten policy negated (.str s) (.box (.str haystack)) = memberOp negated (.str s) (.str haystack) := by
  simp [memberRewritten]

theorem member_boxed_elements (policy : FailureUnbox) (negated : Bool) (needle : Value) (es : List Value)
    (plainNeedle : unbox policy needle = needle) :
    memberRewritten policy negated needle (.list (es.map .box)) = memberOp negated needle (.list es) := by
  simp only [memberRewritten, unbox_list, unboxElems_list, plainNeedle, List.map_map]
  have boxThenUnbox : unbox policy ∘ Value.box = id := rfl
  rw [boxThenUnbox, List.map_id]

theorem old_box_not_in_list : memberOp false (.box (.str "a")) (.list [.str "a"]) = some false := by
  simp [memberOp, memberValues, starEq]

theorem new_box_in_list (policy : FailureUnbox) :
    memberRewritten policy false (.box (.str "a")) (.list [.str "a"]) = some true := by
  simp [memberRewritten, memberOp, memberValues, starEq]

theorem old_text_not_in_list_of_boxes : memberOp false (.str "a") (.list [.box (.str "a")]) = some false := by
  simp [memberOp, memberValues, starEq]

theorem new_text_in_list_of_boxes (policy : FailureUnbox) :
    memberRewritten policy false (.str "a") (.list [.box (.str "a")]) = some true := by
  simp [memberRewritten, memberOp, memberValues, starEq]

theorem old_box_in_text_raises : memberOp false (.box (.str "b")) (.str "abc") = none := rfl

theorem new_box_in_text (policy : FailureUnbox) :
    memberRewritten policy false (.box (.str "b")) (.str "abc") = some true := by
  simp [memberRewritten, memberOp, memberValues, isInfix]

theorem opaque_failure_never_equals_plain (m : String) (v : Value) (notFailure : (unbox .opaque v).isFail = false) :
    compareRewritten .opaque .eq (.fail m) v = some false ∧
    compareRewritten .opaque .eq v (.fail m) = some false ∧
    compareRewritten .opaque .ne (.fail m) v = some true ∧
    compareRewritten .opaque .ne v (.fail m) = some true := by
  simp [compareRewritten, compareValues, starEq_fail_left m notFailure, starEq_fail_right m notFailure]

theorem opaque_failure_ordering_raises (m : String) (v : Value) (op : CmpOp) (notEq : op ≠ .eq) (notNe : op ≠ .ne) :
    compareRewritten .opaque op (.fail m) v = none ∧ compareRewritten .opaque op v (.fail m) = none := by
  cases op <;> simp_all [compareRewritten, compareValues, ordered_fail_left, ordered_fail_right]

theorem opaque_failure_never_member_of_plain_list (m : String) (negated : Bool) (es : List Value)
    (noFailures : ∀ e ∈ es, (unbox .opaque e).isFail = false) :
    memberRewritten .opaque negated (.fail m) (.list es) = some negated := by
  rw [member_elements_list]
  have noMatch : (es.any fun e => starEq (unbox .opaque e) (.fail m)) = false := by
    simp only [List.any_eq_false]
    intro e he
    simp [starEq_fail_right m (noFailures e he)]
  simp [noMatch]

theorem opaque_failure_never_member_of_plain_dict (m : String) (negated : Bool) (entries : List (Value × Value))
    (noFailures : ∀ entry ∈ entries, (unbox .opaque entry.1).isFail = false) :
    memberRewritten .opaque negated (.fail m) (.dict entries) = some negated := by
  rw [member_keys_dict]
  have noMatch : (entries.any fun entry => starEq (.fail m) (unbox .opaque entry.1)) = false := by
    simp only [List.any_eq_false]
    intro entry he
    simp [starEq_fail_left m (noFailures entry he)]
  simp [noMatch]

theorem opaque_failure_in_text_raises (m haystack : String) (negated : Bool) :
    memberRewritten .opaque negated (.fail m) (.str haystack) = none := rfl

theorem message_failure_compares_as_message (op : CmpOp) (m : String) (v : Value) :
    compareRewritten .message op (.fail m) v = compareRewritten .message op (.str m) v ∧
    compareRewritten .message op v (.fail m) = compareRewritten .message op v (.str m) := by
  simp [compareRewritten]

theorem message_failure_member_as_message (negated : Bool) (m : String) (v : Value) :
    memberRewritten .message negated (.fail m) v = memberRewritten .message negated (.str m) v ∧
    memberRewritten .message negated v (.fail m) = memberRewritten .message negated v (.str m) := by
  simp [memberRewritten]

theorem message_failure_equals_only_its_message (m : String) (v : Value) :
    compareRewritten .message .eq (.fail m) v = some true ↔ unbox .message v = .str m := by
  simp only [compareRewritten, compareValues, Option.some.injEq]
  cases unbox .message v <;> simp [starEq] <;> exact eq_comm

theorem rewrite_unboxes_exactly_at_comparisons (e : Expr) (source : scriptSource e = true) :
    unboxedExactlyAtComparisons (rewrite e) = true := by
  induction e <;> simp_all [scriptSource, rewrite, unboxedExactlyAtComparisons]

theorem rewrite_only_adds_unbox (e : Expr) (source : scriptSource e = true) : erase (rewrite e) = e := by
  induction e <;> simp_all [scriptSource, rewrite, erase]

theorem rewrite_is_the_only_such_tree (e : Expr) (shape : unboxedExactlyAtComparisons e = true) :
    e = rewrite (erase e) := by
  match e, shape with
  | .lit _, _ => rfl
  | .var _, _ => rfl
  | .call tool arg, shape =>
    simp only [unboxedExactlyAtComparisons] at shape
    simp only [erase, rewrite]
    rw [← rewrite_is_the_only_such_tree arg shape]
  | .cmp op lhs rhs, shape =>
    cases lhs <;> cases rhs <;> simp [unboxedExactlyAtComparisons] at shape
    rename_i l r
    simp only [erase, rewrite]
    rw [← rewrite_is_the_only_such_tree l shape.1, ← rewrite_is_the_only_such_tree r shape.2]
  | .member negated needle container, shape =>
    cases needle <;> cases container <;> simp [unboxedExactlyAtComparisons] at shape
    rename_i n c
    simp only [erase, rewrite]
    rw [← rewrite_is_the_only_such_tree n shape.1, ← rewrite_is_the_only_such_tree c shape.2]
  | .unbox _, shape => simp [unboxedExactlyAtComparisons] at shape
  | .unboxElems _, shape => simp [unboxedExactlyAtComparisons] at shape
  | .not e, shape =>
    simp only [unboxedExactlyAtComparisons] at shape
    simp only [erase, rewrite]
    rw [← rewrite_is_the_only_such_tree e shape]
  | .and lhs rhs, shape =>
    simp only [unboxedExactlyAtComparisons, Bool.and_eq_true] at shape
    simp only [erase, rewrite]
    rw [← rewrite_is_the_only_such_tree lhs shape.1, ← rewrite_is_the_only_such_tree rhs shape.2]
  | .or lhs rhs, shape =>
    simp only [unboxedExactlyAtComparisons, Bool.and_eq_true] at shape
    simp only [erase, rewrite]
    rw [← rewrite_is_the_only_such_tree lhs shape.1, ← rewrite_is_the_only_such_tree rhs shape.2]
  | .ite cond yes no, shape =>
    simp only [unboxedExactlyAtComparisons, Bool.and_eq_true] at shape
    simp only [erase, rewrite]
    rw [← rewrite_is_the_only_such_tree cond shape.1.1, ← rewrite_is_the_only_such_tree yes shape.1.2,
      ← rewrite_is_the_only_such_tree no shape.2]
termination_by sizeOf e

@[simp] theorem Run.bind_some (log : List String) (v : Value) (k : List String → Value → Run) :
    Run.bind ⟨log, some v⟩ k = k log v := rfl

theorem Run.bind_assoc (r : Run) (f g : List String → Value → Run) :
    (r.bind f).bind g = r.bind fun log v => (f log v).bind g := by
  rcases r with ⟨log, _ | v⟩ <;> rfl

theorem Run.bind_congr {r : Run} {k k' : List String → Value → Run}
    (same : ∀ v, r.value = some v → k r.log v = k' r.log v) : r.bind k = r.bind k' := by
  rcases r with ⟨log, _ | v⟩
  · rfl
  · exact same v rfl

theorem Run.value_bind {r : Run} {k : List String → Value → Run} {v : Value} (h : (r.bind k).value = some v) :
    ∃ v₀, r.value = some v₀ ∧ (k r.log v₀).value = some v := by
  rcases r with ⟨log, _ | v₀⟩
  · simp [Run.bind] at h
  · exact ⟨v₀, rfl, h⟩

theorem settled_bool (policy : FailureUnbox) (b : Bool) : Settled policy (.bool b) := by
  simp [Settled, Value.elems]

theorem settled_of_bool_result {policy : FailureUnbox} {result : Option Bool} {v : Value}
    (h : result.map Value.bool = some v) : Settled policy v := by
  cases result <;> simp at h
  subst h
  exact settled_bool _ _

theorem eval_settled (w : World) (envSettled : ∀ name, Settled w.policy (w.env name))
    (hostSettled : ∀ tool v, Settled w.policy (w.host tool v)) (e : Expr) (source : scriptSource e = true)
    (lits : literalsSettled w.policy e) : ∀ log v, (eval w e log).value = some v → Settled w.policy v := by
  induction e with
  | lit value =>
    intro log v h
    simp only [eval, Option.some.injEq] at h
    exact h ▸ lits
  | var name =>
    intro log v h
    simp only [eval, Option.some.injEq] at h
    exact h ▸ envSettled name
  | call tool arg _ =>
    intro log v h
    obtain ⟨_, _, h⟩ := Run.value_bind h
    simp only [Option.some.injEq] at h
    exact h ▸ hostSettled _ _
  | cmp op lhs rhs _ _ =>
    intro log v h
    obtain ⟨_, _, h⟩ := Run.value_bind h
    obtain ⟨_, _, h⟩ := Run.value_bind h
    exact settled_of_bool_result h
  | member negated needle container _ _ =>
    intro log v h
    obtain ⟨_, _, h⟩ := Run.value_bind h
    obtain ⟨_, _, h⟩ := Run.value_bind h
    exact settled_of_bool_result h
  | unbox _ _ => simp [scriptSource] at source
  | unboxElems _ _ => simp [scriptSource] at source
  | not e _ =>
    intro log v h
    obtain ⟨_, _, h⟩ := Run.value_bind h
    simp only [Option.some.injEq] at h
    exact h ▸ settled_bool _ _
  | and lhs rhs ihl ihr =>
    simp only [scriptSource, Bool.and_eq_true] at source
    intro log v h
    obtain ⟨a, ha, h⟩ := Run.value_bind h
    split at h
    · exact ihr source.2 lits.2 _ _ h
    · simp only [Option.some.injEq] at h
      exact h ▸ ihl source.1 lits.1 _ _ ha
  | or lhs rhs ihl ihr =>
    simp only [scriptSource, Bool.and_eq_true] at source
    intro log v h
    obtain ⟨a, ha, h⟩ := Run.value_bind h
    split at h
    · simp only [Option.some.injEq] at h
      exact h ▸ ihl source.1 lits.1 _ _ ha
    · exact ihr source.2 lits.2 _ _ h
  | ite cond yes no _ ihy ihn =>
    simp only [scriptSource, Bool.and_eq_true] at source
    intro log v h
    obtain ⟨_, _, h⟩ := Run.value_bind h
    split at h
    · exact ihy source.1.2 lits.2.1 _ _ h
    · exact ihn source.2 lits.2.2 _ _ h

theorem eval_unbox_adds_no_effect (w : World) (e : Expr) (log : List String) :
    (eval w (.unbox e) log).log = (eval w e log).log ∧ (eval w (.unboxElems e) log).log = (eval w e log).log := by
  simp only [eval]
  rcases eval w e log with ⟨_, _ | _⟩ <;> exact ⟨rfl, rfl⟩

theorem eval_rewritten_comparison (w : World) (op : CmpOp) (lhs rhs : Expr) (log : List String) :
    eval w (rewrite (.cmp op lhs rhs)) log =
      (eval w (rewrite lhs) log).bind fun log a =>
        (eval w (rewrite rhs) log).bind fun log b => ⟨log, (compareRewritten w.policy op a b).map .bool⟩ := by
  simp only [rewrite, eval, Run.bind_assoc, Run.bind_some, compareRewritten]

theorem eval_rewritten_member (w : World) (negated : Bool) (needle container : Expr) (log : List String) :
    eval w (rewrite (.member negated needle container)) log =
      (eval w (rewrite needle) log).bind fun log n =>
        (eval w (rewrite container) log).bind fun log c =>
          ⟨log, (memberRewritten w.policy negated n c).map .bool⟩ := by
  simp only [rewrite, eval, Run.bind_assoc, Run.bind_some, memberRewritten]

theorem rewrite_conservative (w : World) (envSettled : ∀ name, Settled w.policy (w.env name))
    (hostSettled : ∀ tool v, Settled w.policy (w.host tool v)) (e : Expr) (source : scriptSource e = true)
    (lits : literalsSettled w.policy e) : ∀ log, eval w (rewrite e) log = eval w e log := by
  have settled := eval_settled w envSettled hostSettled
  induction e with
  | lit _ => intro log; rfl
  | var _ => intro log; rfl
  | call tool arg ih =>
    intro log
    simp only [rewrite, eval, ih source lits]
  | cmp op lhs rhs ihl ihr =>
    simp only [scriptSource, Bool.and_eq_true] at source
    intro log
    rw [eval_rewritten_comparison]
    simp only [eval, ihl source.1 lits.1, ihr source.2 lits.2]
    apply Run.bind_congr
    intro a ha
    apply Run.bind_congr
    intro b hb
    rw [compare_conservative _ _ _ _ (settled lhs source.1 lits.1 _ _ ha).1
      (settled rhs source.2 lits.2 _ _ hb).1]
  | member negated needle container ihn ihc =>
    simp only [scriptSource, Bool.and_eq_true] at source
    intro log
    rw [eval_rewritten_member]
    simp only [eval, ihn source.1 lits.1, ihc source.2 lits.2]
    apply Run.bind_congr
    intro n hn
    apply Run.bind_congr
    intro c hc
    have needleSettled := settled needle source.1 lits.1 _ _ hn
    have containerSettled := settled container source.2 lits.2 _ _ hc
    simp only [memberRewritten, needleSettled.1, containerSettled.1, unboxElems_settled containerSettled.2]
  | unbox _ _ => simp [scriptSource] at source
  | unboxElems _ _ => simp [scriptSource] at source
  | not e ih =>
    intro log
    simp only [rewrite, eval, ih source lits]
  | and lhs rhs ihl ihr =>
    simp only [scriptSource, Bool.and_eq_true] at source
    intro log
    simp only [rewrite, eval, ihl source.1 lits.1, ihr source.2 lits.2]
  | or lhs rhs ihl ihr =>
    simp only [scriptSource, Bool.and_eq_true] at source
    intro log
    simp only [rewrite, eval, ihl source.1 lits.1, ihr source.2 lits.2]
  | ite cond yes no ihc ihy ihn =>
    simp only [scriptSource, Bool.and_eq_true] at source
    intro log
    simp only [rewrite, eval, ihc source.1.1 lits.1, ihy source.1.2 lits.2.1, ihn source.2 lits.2.2]

theorem rewrite_can_change_later_effects :
    let w : World := ⟨fun _ => .box (.str "a"), fun _ v => v, .opaque⟩
    let e : Expr := .ite (.cmp .eq (.var "x") (.lit (.str "a"))) (.call "notify" (.lit .noneValue)) (.lit .noneValue)
    (eval w e []).log = [] ∧ (eval w (rewrite e) []).log = ["notify"] := by
  simp [eval, rewrite, compareValues, starEq, truth]

#print axioms unbox_idem
#print axioms compare_conservative
#print axioms compare_conservative_without_boxes
#print axioms member_conservative
#print axioms member_conservative_without_boxes
#print axioms compare_box_left
#print axioms compare_box_right
#print axioms compare_box_box
#print axioms ne_is_not_eq
#print axioms box_text_eq
#print axioms box_text_compares_as_text
#print axioms text_order_is_string_order
#print axioms old_eq_box_text_false
#print axioms new_eq_box_text_true
#print axioms old_lt_box_text_raises
#print axioms new_lt_box_text_true
#print axioms member_elements_list
#print axioms member_keys_dict
#print axioms not_in_is_not_in
#print axioms member_box_list
#print axioms member_box_dict
#print axioms member_box_text
#print axioms member_boxed_elements
#print axioms old_box_not_in_list
#print axioms new_box_in_list
#print axioms old_text_not_in_list_of_boxes
#print axioms new_text_in_list_of_boxes
#print axioms old_box_in_text_raises
#print axioms new_box_in_text
#print axioms opaque_failure_never_equals_plain
#print axioms opaque_failure_ordering_raises
#print axioms opaque_failure_never_member_of_plain_list
#print axioms opaque_failure_never_member_of_plain_dict
#print axioms opaque_failure_in_text_raises
#print axioms message_failure_compares_as_message
#print axioms message_failure_member_as_message
#print axioms message_failure_equals_only_its_message
#print axioms rewrite_unboxes_exactly_at_comparisons
#print axioms rewrite_only_adds_unbox
#print axioms rewrite_is_the_only_such_tree
#print axioms eval_unbox_adds_no_effect
#print axioms eval_rewritten_comparison
#print axioms eval_rewritten_member
#print axioms rewrite_conservative
#print axioms rewrite_can_change_later_effects
