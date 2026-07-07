// Package store is the only path to the SQLite database: reads, journaled
// mutations (mutate.go), and day finalization (finalize.go).
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"habit/internal/domain"
)

// Schema notes vs the spec (§7): habit gains paused_at and entry.status
// gains 'pause' (the spec UIs pause but its schema had nowhere to store it);
// scalar columns use NOT NULL DEFAULT instead of NULL for plain Go scans;
// journal stores one op (the inverse is the op with before/after swapped);
// freeze balance is SUM(freeze_ledger.delta), not a meta row to keep in sync.
const schemaV1 = `
CREATE TABLE grp (
  id       INTEGER PRIMARY KEY,
  name     TEXT NOT NULL UNIQUE,
  builtin  INTEGER NOT NULL DEFAULT 0,
  position INTEGER NOT NULL,
  reminder TEXT NOT NULL DEFAULT ''
);

CREATE TABLE habit (
  id          INTEGER PRIMARY KEY,
  slug        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  kind        TEXT NOT NULL CHECK (kind IN ('check','quantified')),
  target      REAL NOT NULL DEFAULT 0,
  unit        TEXT NOT NULL DEFAULT '',
  step        REAL NOT NULL DEFAULT 1,
  schedule    TEXT NOT NULL CHECK (schedule IN ('daily','weekly')),
  per_week    INTEGER NOT NULL DEFAULT 0,
  group_id    INTEGER NOT NULL REFERENCES grp(id),
  position    INTEGER NOT NULL,
  reminder    TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  archived_at TEXT,
  paused_at   TEXT
);

CREATE TABLE tag (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);
CREATE TABLE habit_tag (
  habit_id INTEGER NOT NULL REFERENCES habit(id),
  tag_id   INTEGER NOT NULL REFERENCES tag(id),
  PRIMARY KEY (habit_id, tag_id)
);

CREATE TABLE entry (
  habit_id    INTEGER NOT NULL REFERENCES habit(id),
  day         TEXT NOT NULL,
  status      TEXT NOT NULL CHECK (status IN ('done','partial','skip','freeze','pause')),
  amount      REAL NOT NULL DEFAULT 0,
  skip_reason TEXT NOT NULL DEFAULT '',
  note        TEXT NOT NULL DEFAULT '',
  logged_at   TEXT NOT NULL,
  source      TEXT NOT NULL DEFAULT 'tui',
  PRIMARY KEY (habit_id, day)
) WITHOUT ROWID;

CREATE INDEX entry_by_day ON entry(day);

CREATE TABLE streak_cache (
  habit_id INTEGER PRIMARY KEY REFERENCES habit(id),
  current  INTEGER NOT NULL,
  best     INTEGER NOT NULL,
  last_day TEXT NOT NULL DEFAULT ''
);

CREATE TABLE freeze_ledger (
  id       INTEGER PRIMARY KEY,
  day      TEXT NOT NULL,
  delta    INTEGER NOT NULL,
  habit_id INTEGER,
  reason   TEXT NOT NULL
);

CREATE TABLE journal (
  id   INTEGER PRIMARY KEY,
  at   TEXT NOT NULL,
  desc TEXT NOT NULL DEFAULT '',
  op   TEXT NOT NULL
);

INSERT INTO grp (name, builtin, position, reminder) VALUES
  ('Morning', 1, 1, '08:00'),
  ('Afternoon', 1, 2, '15:00'),
  ('Evening', 1, 3, '20:30');
`

type Opts struct {
	RolloverHour  int // logical day boundary
	WeekStart     time.Weekday
	DisableFreeze bool // config freeze_tokens = false: no earn, no auto-spend
}

// DefaultOpts matches config.Default(); callers with a real config pass
// their own values.
func DefaultOpts() Opts {
	return Opts{RolloverHour: 3, WeekStart: time.Monday}
}

type Store struct {
	db   *sql.DB
	opt  Opts
	path string
}

// DefaultPath honors HABIT_DB, else the spec location in Application Support.
func DefaultPath() string {
	if p := os.Getenv("HABIT_DB"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "habit", "habit.db")
}

func Open(path string, opt Opts) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, opt: opt, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenRO opens the database read-only — habitd's only access path (§9:
// "the agent can never corrupt state"). No migration, no finalization.
func OpenRO(path string, opt Opts) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	return &Store{db: db, opt: opt, path: path}, nil
}

func (s *Store) Close() error      { return s.db.Close() }
func (s *Store) Path() string      { return s.path }
func (s *Store) Opt() Opts         { return s.opt }
func (s *Store) Today() domain.Day { return domain.LogicalDay(time.Now(), s.opt.RolloverHour) }

// SetOpt applies changed config live (rollover, week start, freeze toggle).
// ponytail: unsynchronized — worst case one in-flight command computes with
// the old options for a frame.
func (s *Store) SetOpt(o Opts) { s.opt = o }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	if metaInt(s.db, "schema_version") >= 1 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schemaV1); err != nil {
		return err
	}
	if err := metaSet(tx, "schema_version", "1"); err != nil {
		return err
	}
	return tx.Commit()
}

// dbq is satisfied by both *sql.DB and *sql.Tx.
type dbq interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func metaGet(q dbq, key string) string {
	var v string
	q.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	return v
}

func metaInt(q dbq, key string) int64 {
	var v int64
	fmt.Sscanf(metaGet(q, key), "%d", &v)
	return v
}

func metaSet(q dbq, key, value string) error {
	_, err := q.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)`, key, value)
	return err
}

// ---- time <-> TEXT ----

func fmtTime(t time.Time) string { return t.Format(time.RFC3339) }

func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func parseTimePtr(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTime(s.String)
	return &t
}

// ---- reads ----

const habitCols = `id, slug, name, kind, target, unit, step, schedule, per_week,
	group_id, position, reminder, created_at, archived_at, paused_at`

func scanHabits(rows *sql.Rows) ([]domain.Habit, error) {
	defer rows.Close()
	var out []domain.Habit
	for rows.Next() {
		var h domain.Habit
		var created string
		var archived, paused sql.NullString
		if err := rows.Scan(&h.ID, &h.Slug, &h.Name, &h.Kind, &h.Target, &h.Unit,
			&h.Step, &h.Schedule, &h.PerWeek, &h.GroupID, &h.Position, &h.Reminder,
			&created, &archived, &paused); err != nil {
			return nil, err
		}
		h.CreatedAt = parseTime(created)
		h.ArchivedAt = parseTimePtr(archived)
		h.PausedAt = parseTimePtr(paused)
		out = append(out, h)
	}
	return out, rows.Err()
}

func habitsQ(q dbq, includeArchived bool) ([]domain.Habit, error) {
	where := ""
	if !includeArchived {
		where = "WHERE archived_at IS NULL"
	}
	rows, err := q.Query(`SELECT ` + habitCols + ` FROM habit ` + where + ` ORDER BY group_id, position`)
	if err != nil {
		return nil, err
	}
	habits, err := scanHabits(rows)
	if err != nil {
		return nil, err
	}
	return habits, loadTags(q, habits)
}

// loadTags attaches tags to habits in one query.
func loadTags(q dbq, habits []domain.Habit) error {
	trows, err := q.Query(`SELECT ht.habit_id, t.name FROM habit_tag ht JOIN tag t ON t.id = ht.tag_id ORDER BY t.name`)
	if err != nil {
		return err
	}
	defer trows.Close()
	tags := map[int64][]string{}
	for trows.Next() {
		var id int64
		var name string
		if err := trows.Scan(&id, &name); err != nil {
			return err
		}
		tags[id] = append(tags[id], name)
	}
	for i := range habits {
		habits[i].Tags = tags[habits[i].ID]
	}
	return trows.Err()
}

func (s *Store) Habits(includeArchived bool) ([]domain.Habit, error) {
	return habitsQ(s.db, includeArchived)
}

func habitByIDQ(q dbq, id int64) (*domain.Habit, error) {
	rows, err := q.Query(`SELECT `+habitCols+` FROM habit WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	hs, err := scanHabits(rows)
	if err != nil || len(hs) == 0 {
		return nil, err
	}
	if err := loadTags(q, hs); err != nil {
		return nil, err
	}
	return &hs[0], nil
}

// HabitBySlug returns nil, nil when the slug is unknown.
func (s *Store) HabitBySlug(slug string) (*domain.Habit, error) {
	rows, err := s.db.Query(`SELECT `+habitCols+` FROM habit WHERE slug = ? AND archived_at IS NULL`, slug)
	if err != nil {
		return nil, err
	}
	hs, err := scanHabits(rows)
	if err != nil || len(hs) == 0 {
		return nil, err
	}
	if err := loadTags(s.db, hs); err != nil {
		return nil, err
	}
	return &hs[0], nil
}

func (s *Store) Groups() ([]domain.Group, error) {
	rows, err := s.db.Query(`SELECT id, name, builtin, position, reminder FROM grp ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Group
	for rows.Next() {
		var g domain.Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Builtin, &g.Position, &g.Reminder); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// EnsureGroup finds or creates a group by name (case-preserving).
// ponytail: group creation is not journaled — an empty group left behind by
// undoing a habit is harmless.
func (s *Store) EnsureGroup(name string) (domain.Group, error) {
	var g domain.Group
	err := s.db.QueryRow(`SELECT id, name, builtin, position, reminder FROM grp WHERE name = ? COLLATE NOCASE`, name).
		Scan(&g.ID, &g.Name, &g.Builtin, &g.Position, &g.Reminder)
	if err == nil {
		return g, nil
	}
	if err != sql.ErrNoRows {
		return g, err
	}
	res, err := s.db.Exec(`INSERT INTO grp (name, builtin, position) VALUES (?, 0, (SELECT COALESCE(MAX(position),0)+1 FROM grp))`, name)
	if err != nil {
		return g, err
	}
	g.ID, _ = res.LastInsertId()
	g.Name = name
	return g, nil
}

func scanEntries(rows *sql.Rows) ([]domain.Entry, error) {
	defer rows.Close()
	var out []domain.Entry
	for rows.Next() {
		var e domain.Entry
		var logged string
		if err := rows.Scan(&e.HabitID, &e.Day, &e.Status, &e.Amount,
			&e.SkipReason, &e.Note, &logged, &e.Source); err != nil {
			return nil, err
		}
		e.LoggedAt = parseTime(logged)
		out = append(out, e)
	}
	return out, rows.Err()
}

const entryCols = `habit_id, day, status, amount, skip_reason, note, logged_at, source`

// EntriesRange returns all entries with from <= day <= to, ordered by day.
func (s *Store) EntriesRange(from, to domain.Day) ([]domain.Entry, error) {
	rows, err := s.db.Query(`SELECT `+entryCols+` FROM entry WHERE day BETWEEN ? AND ? ORDER BY day`, from, to)
	if err != nil {
		return nil, err
	}
	return scanEntries(rows)
}

func entriesForQ(q dbq, habitID int64) ([]domain.Entry, error) {
	rows, err := q.Query(`SELECT `+entryCols+` FROM entry WHERE habit_id = ? ORDER BY day`, habitID)
	if err != nil {
		return nil, err
	}
	return scanEntries(rows)
}

// EntriesFor returns one habit's full history, ordered by day.
func (s *Store) EntriesFor(habitID int64) ([]domain.Entry, error) {
	return entriesForQ(s.db, habitID)
}

func entryQ(q dbq, habitID int64, day domain.Day) (*domain.Entry, error) {
	rows, err := q.Query(`SELECT `+entryCols+` FROM entry WHERE habit_id = ? AND day = ?`, habitID, day)
	if err != nil {
		return nil, err
	}
	es, err := scanEntries(rows)
	if err != nil || len(es) == 0 {
		return nil, err
	}
	return &es[0], nil
}

// MetaGet / MetaSet expose the meta key-value table for non-journaled app
// state (frecency, UI prefs). Not undoable by design.
func (s *Store) MetaGet(key string) string       { return metaGet(s.db, key) }
func (s *Store) MetaSet(key, value string) error { return metaSet(s.db, key, value) }

// Entry returns one habit-day entry, or nil, nil when absent.
func (s *Store) Entry(habitID int64, day domain.Day) (*domain.Entry, error) {
	return entryQ(s.db, habitID, day)
}

func freezeBalanceQ(q dbq) (int, error) {
	var n int
	err := q.QueryRow(`SELECT COALESCE(SUM(delta), 0) FROM freeze_ledger`).Scan(&n)
	return n, err
}

func (s *Store) FreezeBalance() (int, error) { return freezeBalanceQ(s.db) }

func (s *Store) Streaks() (map[int64]domain.Streak, error) {
	rows, err := s.db.Query(`SELECT habit_id, current, best, last_day FROM streak_cache`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]domain.Streak{}
	for rows.Next() {
		var id int64
		var st domain.Streak
		if err := rows.Scan(&id, &st.Current, &st.Best, &st.LastDay); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// Snapshot is the single batched load behind the dashboard (spec §6.2):
// groups, active habits, a 14-day entry window, streaks, freeze balance.
type Snapshot struct {
	Today   domain.Day
	Groups  []domain.Group
	Habits  []domain.Habit
	Entries []domain.Entry // window today-13 … today; covers sparkline + this week
	Streaks map[int64]domain.Streak
	Freeze  int
}

func (s *Store) Snapshot() (*Snapshot, error) {
	today := s.Today()
	snap := &Snapshot{Today: today}
	var err error
	if snap.Groups, err = s.Groups(); err != nil {
		return nil, err
	}
	if snap.Habits, err = s.Habits(false); err != nil {
		return nil, err
	}
	if snap.Entries, err = s.EntriesRange(today.AddDays(-13), today); err != nil {
		return nil, err
	}
	if snap.Streaks, err = s.Streaks(); err != nil {
		return nil, err
	}
	if snap.Freeze, err = s.FreezeBalance(); err != nil {
		return nil, err
	}
	return snap, nil
}

// Doctor runs an integrity check and rebuilds every streak cache.
func (s *Store) Doctor() (string, error) {
	var integrity string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	habits, err := habitsQ(tx, true)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(`DELETE FROM streak_cache`); err != nil {
		return "", err
	}
	for _, h := range habits {
		if err := recomputeStreak(tx, h.ID, s.Today(), s.opt.WeekStart); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return fmt.Sprintf("integrity: %s · streak cache rebuilt for %d habits", integrity, len(habits)), nil
}

// recomputeStreak refreshes one habit's streak_cache row from its entries
// (full recompute per the domain ponytail note). Missing habit → row removed.
func recomputeStreak(q dbq, habitID int64, today domain.Day, weekStart time.Weekday) error {
	h, err := habitByIDQ(q, habitID)
	if err != nil {
		return err
	}
	if h == nil {
		_, err = q.Exec(`DELETE FROM streak_cache WHERE habit_id = ?`, habitID)
		return err
	}
	entries, err := entriesForQ(q, habitID)
	if err != nil {
		return err
	}
	st := domain.ComputeStreak(*h, entries, today, weekStart)
	_, err = q.Exec(`INSERT OR REPLACE INTO streak_cache (habit_id, current, best, last_day) VALUES (?, ?, ?, ?)`,
		habitID, st.Current, st.Best, st.LastDay)
	return err
}
