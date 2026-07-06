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
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), Opts{})
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
	s, err := Open(path, Opts{})
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
	s2, err := Open(path, Opts{})
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

func TestConcurrentReadWhileWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	writer, err := Open(path, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(path, Opts{})
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
		if _, err := reader.Snapshot(); err != nil {
			t.Errorf("concurrent read: %v", err)
		}
	}
	wg.Wait()
	entries, err := reader.EntriesFor(h.ID)
	if err != nil || len(entries) != 30 {
		t.Fatalf("entries = %d, err %v", len(entries), err)
	}
}
