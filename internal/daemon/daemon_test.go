package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"habit/internal/config"
	"habit/internal/domain"
	"habit/internal/store"
)

// testStore builds a rw store (the daemon itself only ever opens ro).
func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"), store.DefaultOpts())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addHabit(t *testing.T, s *store.Store, name, reminder string, groupID int64) domain.Habit {
	t.Helper()
	h := domain.Habit{Name: name, Kind: domain.Check, Schedule: domain.Daily,
		GroupID: groupID, Reminder: reminder, CreatedAt: time.Now().AddDate(0, 0, -30)}
	if err := s.CreateHabit(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

func at(t *testing.T, hm string) time.Time {
	t.Helper()
	tm, err := time.Parse("15:04", hm)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestQuietHours(t *testing.T) {
	cfg := config.Default() // 22:00 → 08:00
	cases := map[string]bool{"23:30": true, "03:00": true, "07:59": true, "08:00": false, "12:00": false, "21:59": false}
	for hm, want := range cases {
		if got := InQuietHours(at(t, hm), cfg); got != want {
			t.Errorf("InQuietHours(%s) = %v, want %v", hm, got, want)
		}
	}
}

func TestDueSelectionAndBatching(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	// Morning group (id 1) has a seeded 08:00 default reminder.
	done := addHabit(t, s, "Meditate", "", 1)
	addHabit(t, s, "Journal", "", 1)
	addHabit(t, s, "Stretch", "09:30", 1) // habit override beats the group default
	addHabit(t, s, "Read", "", 2)         // Afternoon: 15:00 default

	if err := s.SetEntry(domain.Entry{HabitID: done.ID, Day: today,
		Status: domain.StatusDone, LoggedAt: time.Now(), Source: "cli"}); err != nil {
		t.Fatal(err)
	}

	// 09:00: Journal due (group 08:00); Stretch not yet (09:30); Read not (15:00).
	ns, err := Due(s, at(t, "09:00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 || ns[0].Group != "Morning" || ns[0].Body != "Journal" {
		t.Fatalf("09:00 due = %+v", ns)
	}

	// 16:00: Morning batches Journal+Stretch into one; Afternoon has Read.
	ns, err = Due(s, at(t, "16:00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 {
		t.Fatalf("16:00 due = %+v", ns)
	}
	if ns[0].Body != "2 habits left: Journal, Stretch" {
		t.Errorf("batch body = %q", ns[0].Body)
	}
	if ns[1].Group != "Afternoon" || ns[1].Body != "Read" {
		t.Errorf("afternoon = %+v", ns[1])
	}
}

func TestRunOncePostsEachGroupOncePerDay(t *testing.T) {
	s := testStore(t)
	addHabit(t, s, "Journal", "", 1)
	cfg := config.Default()

	var posted []Notification
	collect := func(n Notification) error { posted = append(posted, n); return nil }

	if err := RunOnce(s, cfg, at(t, "12:00"), collect); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 {
		t.Fatalf("first run posted %d", len(posted))
	}
	// Second wake, same day: state file suppresses the repeat.
	if err := RunOnce(s, cfg, at(t, "12:30"), collect); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 {
		t.Fatalf("second run reposted: %d", len(posted))
	}
	// Quiet hours: nothing even for a new group.
	if err := RunOnce(s, cfg, at(t, "23:00"), collect); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 {
		t.Fatalf("quiet hours posted: %d", len(posted))
	}
}

func TestStreakOnTheLine(t *testing.T) {
	s := testStore(t)
	today := s.Today()
	h := addHabit(t, s, "Meditate", "", 1)
	for i := 1; i <= 8; i++ {
		if err := s.SetEntry(domain.Entry{HabitID: h.ID, Day: today.AddDays(-i),
			Status: domain.StatusDone, LoggedAt: time.Now(), Source: "cli"}); err != nil {
			t.Fatal(err)
		}
	}
	ns, err := Due(s, at(t, "12:00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 || ns[0].Body != "Meditate · ◆ 8-day streak on the line" {
		t.Fatalf("body = %+v", ns)
	}
}
