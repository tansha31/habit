package store

import (
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"habit/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), DefaultOpts())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustCreate(t *testing.T, s *Store, h domain.Habit) domain.Habit {
	t.Helper()
	if h.Kind == "" {
		h.Kind = domain.Check
	}
	if h.Schedule == "" {
		h.Schedule = domain.Daily
	}
	if h.GroupID == 0 {
		h.GroupID = 1 // Morning (seeded)
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now().AddDate(0, 0, -30) // exists for backfilled days
	}
	if err := s.CreateHabit(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

func mustDone(t *testing.T, s *Store, habitID int64, day domain.Day) {
	t.Helper()
	err := s.SetEntry(domain.Entry{HabitID: habitID, Day: day,
		Status: domain.StatusDone, LoggedAt: time.Now(), Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
}

func streakOf(t *testing.T, s *Store, habitID int64) domain.Streak {
	t.Helper()
	m, err := s.Streaks()
	if err != nil {
		t.Fatal(err)
	}
	return m[habitID]
}

func balance(t *testing.T, s *Store) int {
	t.Helper()
	n, err := s.FreezeBalance()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenMigrateWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path, DefaultOpts())
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, err %v", mode, err)
	}
	groups, err := s.Groups()
	if err != nil || len(groups) != 3 {
		t.Fatalf("seeded groups = %d, err %v", len(groups), err)
	}
	s.Close()
	// Re-open must not re-migrate.
	s2, err := Open(path, DefaultOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if g, _ := s2.Groups(); len(g) != 3 {
		t.Fatalf("re-open groups = %d", len(g))
	}
}

func TestHabitRoundTrip(t *testing.T) {
	s := testStore(t)
	h := mustCreate(t, s, domain.Habit{Name: "Read fiction", Kind: domain.Quantified,
		Target: 20, Unit: "min", Step: 5, Tags: []string{"reading", "deep-work"}})
	if h.Slug != "read-fiction" || h.ID == 0 || h.Position == 0 {
		t.Fatalf("create left gaps: %+v", h)
	}
	got, err := s.HabitBySlug("read-fiction")
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Target != 20 || got.Unit != "min" || !reflect.DeepEqual(got.Tags, []string{"deep-work", "reading"}) {
		t.Fatalf("round trip: %+v", got)
	}
	// Duplicate slug must be rejected, not silently replaced.
	dup := domain.Habit{Name: "Read Fiction!", Kind: domain.Check, Schedule: domain.Daily, GroupID: 1}
	if err := s.CreateHabit(&dup); err == nil {
		t.Fatal("duplicate slug accepted")
	}
}

func TestEntryStreakAndUndo(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Meditate"})
	mustDone(t, s, h.ID, today.AddDays(-1))
	mustDone(t, s, h.ID, today)

	if st := streakOf(t, s, h.ID); st.Current != 2 || st.Best != 2 {
		t.Fatalf("streak = %+v", st)
	}

	desc, err := s.Undo()
	if err != nil || desc != "done meditate" {
		t.Fatalf("undo: %q, %v", desc, err)
	}
	if e, _ := entryQ(s.db, h.ID, today); e != nil {
		t.Fatal("entry survived undo")
	}
	if st := streakOf(t, s, h.ID); st.Current != 1 {
		t.Fatalf("streak after undo = %+v", st)
	}

	if desc, err := s.Redo(); err != nil || desc != "done meditate" {
		t.Fatalf("redo: %q, %v", desc, err)
	}
	if st := streakOf(t, s, h.ID); st.Current != 2 {
		t.Fatalf("streak after redo = %+v", st)
	}

	// Unwind everything: entry, entry, create.
	for i := 0; i < 3; i++ {
		if _, err := s.Undo(); err != nil {
			t.Fatalf("undo #%d: %v", i+1, err)
		}
	}
	if hb, _ := s.HabitBySlug("meditate"); hb != nil {
		t.Fatal("habit survived undoing its creation")
	}
	if _, err := s.Undo(); err != ErrNothingToUndo {
		t.Fatalf("want ErrNothingToUndo, got %v", err)
	}

	// Redo the create, then a fresh mutation must truncate the redo tail.
	if _, err := s.Redo(); err != nil {
		t.Fatal(err)
	}
	mustDone(t, s, h.ID, today)
	if _, err := s.Redo(); err != ErrNothingToRedo {
		t.Fatalf("redo after new mutation: %v", err)
	}
}

func TestFreezeEarnAndUndo(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Walk"})
	for i := 0; i < 10; i++ {
		mustDone(t, s, h.ID, today.AddDays(i-9))
	}
	if b := balance(t, s); b != 1 {
		t.Fatalf("balance after 10 completions = %d", b)
	}
	// Undoing the 10th completion returns the token with it.
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, s); b != 0 {
		t.Fatalf("balance after undo = %d", b)
	}
}

func TestUpdateHabitDescAndUndo(t *testing.T) {
	s := testStore(t)
	h := mustCreate(t, s, domain.Habit{Name: "Gym"})
	loaded, _ := s.HabitBySlug("gym")
	now := time.Now()
	loaded.ArchivedAt = &now
	if err := s.UpdateHabit(*loaded); err != nil {
		t.Fatal(err)
	}
	if hb, _ := s.HabitBySlug("gym"); hb != nil {
		t.Fatal("archived habit still visible")
	}
	desc, err := s.Undo()
	if err != nil || desc != "archive gym" {
		t.Fatalf("undo archive: %q, %v", desc, err)
	}
	if hb, _ := s.HabitBySlug("gym"); hb == nil || hb.ID != h.ID {
		t.Fatal("archive not reverted")
	}
}

func TestFinalizeFreezeSpend(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Meditate"})
	// 10 dones ending two days ago: streak 10 on the line, 1 token banked.
	for i := 0; i < 10; i++ {
		mustDone(t, s, h.ID, today.AddDays(i-11))
	}
	if err := metaSet(s.db, "last_finalized", string(today.AddDays(-2))); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeThrough(today); err != nil {
		t.Fatal(err)
	}
	e, _ := entryQ(s.db, h.ID, today.AddDays(-1))
	if e == nil || e.Status != domain.StatusFreeze || e.Source != "freeze-auto" {
		t.Fatalf("missed day not frozen: %+v", e)
	}
	if b := balance(t, s); b != 0 {
		t.Fatalf("token not spent, balance = %d", b)
	}
	if st := streakOf(t, s, h.ID); st.Current != 10 {
		t.Fatalf("streak not preserved: %+v", st)
	}
	// Idempotent: running again must not double-finalize.
	if err := s.FinalizeThrough(today); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, s); b != 0 {
		t.Fatal("second finalize changed the ledger")
	}
}

func TestFinalizeStreakDiesWithoutToken(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Journal"})
	// 7 dones ending two days ago: streak 7 at risk, but no token earned.
	for i := 0; i < 7; i++ {
		mustDone(t, s, h.ID, today.AddDays(i-8))
	}
	metaSet(s.db, "last_finalized", string(today.AddDays(-2)))
	if err := s.FinalizeThrough(today); err != nil {
		t.Fatal(err)
	}
	if e, _ := entryQ(s.db, h.ID, today.AddDays(-1)); e != nil {
		t.Fatalf("no token, yet day resolved: %+v", e)
	}
	if st := streakOf(t, s, h.ID); st.Current != 0 || st.Best != 7 {
		t.Fatalf("dead streak cache stale: %+v", st)
	}
}

func TestFinalizePausedHabit(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	mustCreate(t, s, domain.Habit{Name: "Gym"})
	loaded, _ := s.HabitBySlug("gym")
	now := time.Now()
	loaded.PausedAt = &now
	if err := s.UpdateHabit(*loaded); err != nil {
		t.Fatal(err)
	}
	metaSet(s.db, "last_finalized", string(today.AddDays(-3)))
	if err := s.FinalizeThrough(today); err != nil {
		t.Fatal(err)
	}
	for _, d := range []domain.Day{today.AddDays(-2), today.AddDays(-1)} {
		e, _ := entryQ(s.db, loaded.ID, d)
		if e == nil || e.Status != domain.StatusPause {
			t.Fatalf("day %s: %+v", d, e)
		}
	}
}

// A habit created in the local evening lands on the NEXT calendar date in
// UTC; the creation-day guard and ActiveOn must use the local date or every
// same-day log is rejected for users west of UTC.
func TestSetEntryAllowsSameDayForEveningCreatedHabit(t *testing.T) {
	oldLocal := time.Local
	time.Local = time.FixedZone("UTC-5", -5*3600)
	t.Cleanup(func() { time.Local = oldLocal })

	s := testStore(t)
	today := s.Today()
	d := today.Time() // UTC midnight of today's calendar date
	created := time.Date(d.Year(), d.Month(), d.Day(), 20, 0, 0, 0, time.Local)
	h := mustCreate(t, s, domain.Habit{Name: "Evening", CreatedAt: created.UTC()})
	mustDone(t, s, h.ID, today) // must not be rejected as pre-creation
}

func TestBackfillRefundPlusEarnRespectsCap(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Meditate"})
	// 19 dones ending two days ago (earns 1 at #10), topped up to the cap of 3.
	for i := 0; i < 19; i++ {
		mustDone(t, s, h.ID, today.AddDays(i-20))
	}
	if _, err := s.db.Exec(`INSERT INTO freeze_ledger (day, delta, habit_id, reason) VALUES (?, 2, ?, 'earned')`,
		today.AddDays(-3), h.ID); err != nil {
		t.Fatal(err)
	}
	metaSet(s.db, "last_finalized", string(today.AddDays(-2)))
	if err := s.FinalizeThrough(today); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, s); b != 2 {
		t.Fatalf("setup: balance = %d, want 2 (one auto-spent)", b)
	}
	// Backfilling the frozen day is the 20th done: refund (+1 → 3) plus an
	// earn would breach FreezeCap — the earn must see the refunded balance.
	// The refund and earn rows must also get DISTINCT ledger IDs: both were
	// preallocated from MAX(id)+1 pre-commit, so the second INSERT OR REPLACE
	// used to silently swallow the first.
	mustDone(t, s, h.ID, today.AddDays(-1))
	if b := balance(t, s); b != 3 {
		t.Fatalf("balance = %d, want 3 (cap)", b)
	}
	var refunds, earns int
	s.db.QueryRow(`SELECT COUNT(*) FROM freeze_ledger WHERE reason = 'refund'`).Scan(&refunds)
	s.db.QueryRow(`SELECT COUNT(*) FROM freeze_ledger WHERE reason = 'earned' AND day = ?`, today.AddDays(-1)).Scan(&earns)
	if refunds != 1 || earns != 0 {
		t.Fatalf("refund rows = %d, earn rows on frozen day = %d; want 1, 0", refunds, earns)
	}
}

func TestBackfillSkipOverFreezeKeepsTokenSpent(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Meditate"})
	for i := 0; i < 10; i++ {
		mustDone(t, s, h.ID, today.AddDays(i-11))
	}
	metaSet(s.db, "last_finalized", string(today.AddDays(-2)))
	if err := s.FinalizeThrough(today); err != nil {
		t.Fatal(err)
	}
	// Skip breaks the streak the token protected — no refund for that.
	err := s.SetEntry(domain.Entry{HabitID: h.ID, Day: today.AddDays(-1),
		Status: domain.StatusSkip, SkipReason: "tired", LoggedAt: time.Now(), Source: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if b := balance(t, s); b != 0 {
		t.Fatalf("balance = %d, want 0 (token stays spent)", b)
	}
}

func TestClearFreezeEntryRefundsToken(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Meditate"})
	for i := 0; i < 10; i++ {
		mustDone(t, s, h.ID, today.AddDays(i-11))
	}
	metaSet(s.db, "last_finalized", string(today.AddDays(-2)))
	if err := s.FinalizeThrough(today); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearEntry(h.ID, today.AddDays(-1)); err != nil {
		t.Fatal(err)
	}
	if e, _ := entryQ(s.db, h.ID, today.AddDays(-1)); e != nil {
		t.Fatalf("freeze entry survived clear: %+v", e)
	}
	if b := balance(t, s); b != 1 {
		t.Fatalf("balance = %d, want 1 (auto-spend undone with the entry)", b)
	}
	// Undo restores the freeze entry and takes the refund back.
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if e, _ := entryQ(s.db, h.ID, today.AddDays(-1)); e == nil || e.Status != domain.StatusFreeze {
		t.Fatalf("undo did not restore freeze entry: %+v", e)
	}
	if b := balance(t, s); b != 0 {
		t.Fatalf("balance after undo = %d, want 0", b)
	}
}

func TestSetEntryRejectsFuture(t *testing.T) {
	s := testStore(t)
	h := mustCreate(t, s, domain.Habit{Name: "Meditate"})
	err := s.SetEntry(domain.Entry{HabitID: h.ID, Day: s.Today().AddDays(1),
		Status: domain.StatusDone, LoggedAt: time.Now(), Source: "cli"})
	if err == nil {
		t.Fatal("future entry accepted")
	}
}

func TestSetEntryRejectsPreCreation(t *testing.T) {
	s := testStore(t)
	h := mustCreate(t, s, domain.Habit{Name: "Meditate", CreatedAt: time.Now()})
	err := s.SetEntry(domain.Entry{HabitID: h.ID, Day: s.Today().AddDays(-1),
		Status: domain.StatusDone, LoggedAt: time.Now(), Source: "cli"})
	if err == nil {
		t.Fatal("pre-creation entry accepted")
	}
}

func TestBackfillStampsSource(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Meditate"})
	mustDone(t, s, h.ID, today.AddDays(-1))
	mustDone(t, s, h.ID, today)
	if e, _ := entryQ(s.db, h.ID, today.AddDays(-1)); e == nil || e.Source != "backfill" {
		t.Fatalf("backfilled entry source = %+v", e)
	}
	if e, _ := entryQ(s.db, h.ID, today); e == nil || e.Source != "cli" {
		t.Fatalf("same-day entry source = %+v", e)
	}
}

func TestBackfillRefundsAutoSpentFreeze(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Meditate"})
	// 10 dones ending two days ago: 1 token banked, then auto-spent on the miss.
	for i := 0; i < 10; i++ {
		mustDone(t, s, h.ID, today.AddDays(i-11))
	}
	metaSet(s.db, "last_finalized", string(today.AddDays(-2)))
	if err := s.FinalizeThrough(today); err != nil {
		t.Fatal(err)
	}
	if b := balance(t, s); b != 0 {
		t.Fatalf("setup: balance = %d, want 0", b)
	}

	// Backfill the frozen day as actually done: freeze wasn't needed.
	mustDone(t, s, h.ID, today.AddDays(-1))
	if e, _ := entryQ(s.db, h.ID, today.AddDays(-1)); e == nil || e.Status != domain.StatusDone {
		t.Fatalf("freeze entry not replaced: %+v", e)
	}
	if b := balance(t, s); b != 1 {
		t.Fatalf("token not refunded, balance = %d", b)
	}
	if st := streakOf(t, s, h.ID); st.Current != 11 {
		t.Fatalf("streak = %+v, want 11", st)
	}

	// Undo returns the freeze entry AND takes the refund back.
	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if e, _ := entryQ(s.db, h.ID, today.AddDays(-1)); e == nil || e.Status != domain.StatusFreeze {
		t.Fatalf("undo did not restore freeze entry: %+v", e)
	}
	if b := balance(t, s); b != 0 {
		t.Fatalf("undo did not revert refund, balance = %d", b)
	}
}

func TestBackfillResurrectsStreak(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Journal"})
	// 3 dones ending two days ago: streak dies on the missed day (no token).
	for i := 0; i < 3; i++ {
		mustDone(t, s, h.ID, today.AddDays(i-4))
	}
	metaSet(s.db, "last_finalized", string(today.AddDays(-2)))
	if err := s.FinalizeThrough(today); err != nil {
		t.Fatal(err)
	}
	if st := streakOf(t, s, h.ID); st.Current != 0 {
		t.Fatalf("setup: streak = %+v, want dead", st)
	}
	mustDone(t, s, h.ID, today.AddDays(-1))
	if st := streakOf(t, s, h.ID); st.Current != 4 {
		t.Fatalf("streak after backfill = %+v, want 4", st)
	}
}

func TestConcurrentReadWhileWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	writer, err := Open(path, DefaultOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(path, DefaultOpts())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	h := mustCreate(t, writer, domain.Habit{Name: "Meditate"})
	today := writer.Today()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			mustDone(t, writer, h.ID, today.AddDays(-i))
		}
	}()
	for i := 0; i < 30; i++ {
		if _, err := reader.Snapshot(reader.Today()); err != nil {
			t.Errorf("concurrent read: %v", err)
		}
	}
	wg.Wait()
	entries, err := reader.EntriesFor(h.ID)
	if err != nil || len(entries) != 30 {
		t.Fatalf("entries = %d, err %v", len(entries), err)
	}
}

func TestEnsureGroup(t *testing.T) {
	s := testStore(t)

	g, err := s.EnsureGroup("Workout")
	if err != nil {
		t.Fatal(err)
	}
	if g.ID == 0 || g.Name != "Workout" {
		t.Fatalf("create: got %+v", g)
	}

	// Case-insensitive reuse: seeded builtin and the group just created.
	m, err := s.EnsureGroup("morning")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Builtin || m.Name != "Morning" {
		t.Fatalf("seeded reuse: got %+v", m)
	}
	g2, err := s.EnsureGroup("WORKOUT")
	if err != nil {
		t.Fatal(err)
	}
	if g2.ID != g.ID {
		t.Fatalf("reuse: got ID %d, want %d", g2.ID, g.ID)
	}

	// Custom group persists as builtin=0, appended after the seeded three.
	groups, err := s.Groups()
	if err != nil {
		t.Fatal(err)
	}
	last := groups[len(groups)-1]
	if len(groups) != 4 || last.Name != "Workout" || last.Builtin || last.Position != 4 {
		t.Fatalf("groups: got %+v", groups)
	}
}
