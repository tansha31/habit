package store

import (
	"testing"

	"habit/internal/domain"
)

func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestResetLogs(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h1 := mustCreate(t, s, domain.Habit{Name: "Meditate"})
	h2 := mustCreate(t, s, domain.Habit{Name: "Read"})
	mustDone(t, s, h1.ID, today.AddDays(-1))
	mustDone(t, s, h1.ID, today)
	mustDone(t, s, h2.ID, today)
	// Simulate a banked freeze token so the ledger has something to clear.
	if _, err := s.db.Exec(`INSERT INTO freeze_ledger (day, delta, habit_id, reason) VALUES (?, 1, ?, 'earned')`,
		today, h1.ID); err != nil {
		t.Fatal(err)
	}
	if balance(t, s) != 1 {
		t.Fatalf("precondition: balance = %d, want 1", balance(t, s))
	}

	if err := s.Reset(ResetLogs); err != nil {
		t.Fatal(err)
	}

	if n := count(t, s, "entry"); n != 0 {
		t.Fatalf("entries after reset = %d, want 0", n)
	}
	if n := count(t, s, "journal"); n != 0 {
		t.Fatalf("journal after reset = %d, want 0", n)
	}
	if b := balance(t, s); b != 0 {
		t.Fatalf("freeze balance after reset = %d, want 0", b)
	}
	// Habits survive, streaks reset to zero (cache rebuilt, not stale).
	habits, err := s.Habits(false)
	if err != nil || len(habits) != 2 {
		t.Fatalf("habits after log reset = %d, err %v", len(habits), err)
	}
	if st := streakOf(t, s, h1.ID); st.Current != 0 {
		t.Fatalf("streak after reset = %+v, want current 0", st)
	}
	// Journal is gone, so there is nothing to undo.
	if _, err := s.Undo(); err != ErrNothingToUndo {
		t.Fatalf("undo after reset = %v, want ErrNothingToUndo", err)
	}
}

func TestResetAll(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := mustCreate(t, s, domain.Habit{Name: "Meditate", Tags: []string{"calm"}})
	mustDone(t, s, h.ID, today)
	// A user-created group (builtin=0) — none exist today, but ResetAll must
	// drop them once the feature lands while keeping the built-ins.
	if _, err := s.db.Exec(`INSERT INTO grp (name, builtin, position, reminder) VALUES ('Custom', 0, 4, '')`); err != nil {
		t.Fatal(err)
	}

	if err := s.Reset(ResetAll); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"habit", "habit_tag", "tag", "entry", "freeze_ledger", "journal"} {
		if n := count(t, s, table); n != 0 {
			t.Fatalf("%s after full reset = %d, want 0", table, n)
		}
	}
	// Built-in groups preserved; the custom one is gone.
	groups, err := s.Groups()
	if err != nil || len(groups) != 3 {
		t.Fatalf("groups after full reset = %d, err %v", len(groups), err)
	}
	// A fresh snapshot still loads cleanly.
	if _, err := s.Snapshot(); err != nil {
		t.Fatalf("snapshot after full reset: %v", err)
	}
}
