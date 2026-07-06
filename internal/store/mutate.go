package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"habit/internal/domain"
)

var (
	ErrNothingToUndo = errors.New("nothing to undo")
	ErrNothingToRedo = errors.New("nothing to redo")
)

// Change is one row's before/after snapshot; nil means "row absent".
// Undo applies Before, redo applies After — an op is its own inverse with
// the sides swapped, so the journal stores just the forward op.
type Change struct {
	Table  string          `json:"table"` // "habit" | "entry" | "ledger"
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
}

type Op struct {
	Desc    string   `json:"desc"`
	Changes []Change `json:"changes"`
}

type ledgerRow struct {
	ID      int64      `json:"id"`
	Day     domain.Day `json:"day"`
	Delta   int        `json:"delta"`
	HabitID int64      `json:"habit_id"`
	Reason  string     `json:"reason"` // "earned" | "auto-spend"
}

func snap(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func chg(table string, before, after any) Change {
	c := Change{Table: table}
	// The string(...) == "null" guard catches typed-nil pointers, which slip
	// past an interface nil check and would corrupt the snapshot.
	if b := snap(before); before != nil && string(b) != "null" {
		c.Before = b
	}
	if a := snap(after); after != nil && string(a) != "null" {
		c.After = a
	}
	return c
}

// mutate commits op's changes plus its journal row in ONE transaction —
// data and undo journal can never disagree, which is the whole crash-safety
// story. Any redo tail above the cursor is discarded first.
func (s *Store) mutate(op Op) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cursor := metaInt(tx, "undo_cursor")
	if _, err := tx.Exec(`DELETE FROM journal WHERE id > ?`, cursor); err != nil {
		return err
	}
	if err := s.applyChanges(tx, op.Changes, false); err != nil {
		return err
	}
	res, err := tx.Exec(`INSERT INTO journal (at, desc, op) VALUES (?, ?, ?)`,
		fmtTime(time.Now()), op.Desc, string(snap(op)))
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	if err := metaSet(tx, "undo_cursor", fmt.Sprint(id)); err != nil {
		return err
	}
	return tx.Commit()
}

// applyChanges applies each change (reverse=true walks them backwards and
// swaps before/after), then refreshes streak caches for touched habits.
func (s *Store) applyChanges(tx *sql.Tx, changes []Change, reverse bool) error {
	touched := map[int64]bool{}
	for i := range changes {
		c := changes[i]
		if reverse {
			c = changes[len(changes)-1-i]
			c.Before, c.After = c.After, c.Before
		}
		habitID, err := applyChange(tx, c)
		if err != nil {
			return err
		}
		if habitID != 0 {
			touched[habitID] = true
		}
	}
	for id := range touched {
		if err := recomputeStreak(tx, id, s.Today(), s.opt.WeekStart); err != nil {
			return err
		}
	}
	return nil
}

// applyChange writes one row to its target state and reports the habit
// whose streak it may have moved.
func applyChange(tx *sql.Tx, c Change) (int64, error) {
	switch c.Table {
	case "entry":
		var e domain.Entry
		if c.After == nil {
			json.Unmarshal(c.Before, &e)
			_, err := tx.Exec(`DELETE FROM entry WHERE habit_id = ? AND day = ?`, e.HabitID, e.Day)
			return e.HabitID, err
		}
		json.Unmarshal(c.After, &e)
		_, err := tx.Exec(`INSERT OR REPLACE INTO entry (`+entryCols+`) VALUES (?,?,?,?,?,?,?,?)`,
			e.HabitID, e.Day, e.Status, e.Amount, e.SkipReason, e.Note, fmtTime(e.LoggedAt), e.Source)
		return e.HabitID, err

	case "habit":
		var h domain.Habit
		if c.After == nil {
			json.Unmarshal(c.Before, &h)
			// Clear dependents first (streak_cache and tags reference habit.id).
			for _, del := range []string{
				`DELETE FROM streak_cache WHERE habit_id = ?`,
				`DELETE FROM habit_tag WHERE habit_id = ?`,
				`DELETE FROM habit WHERE id = ?`,
			} {
				if _, err := tx.Exec(del, h.ID); err != nil {
					return 0, err
				}
			}
			return h.ID, nil
		}
		json.Unmarshal(c.After, &h)
		if _, err := tx.Exec(`INSERT OR REPLACE INTO habit (`+habitCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			h.ID, h.Slug, h.Name, h.Kind, h.Target, h.Unit, h.Step, h.Schedule, h.PerWeek,
			h.GroupID, h.Position, h.Reminder, fmtTime(h.CreatedAt),
			fmtTimePtr(h.ArchivedAt), fmtTimePtr(h.PausedAt)); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM habit_tag WHERE habit_id = ?`, h.ID); err != nil {
			return 0, err
		}
		for _, tag := range h.Tags {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO tag (name) VALUES (?)`, tag); err != nil {
				return 0, err
			}
			if _, err := tx.Exec(`INSERT INTO habit_tag (habit_id, tag_id) SELECT ?, id FROM tag WHERE name = ?`, h.ID, tag); err != nil {
				return 0, err
			}
		}
		return h.ID, nil

	case "ledger":
		var l ledgerRow
		if c.After == nil {
			json.Unmarshal(c.Before, &l)
			_, err := tx.Exec(`DELETE FROM freeze_ledger WHERE id = ?`, l.ID)
			return 0, err
		}
		json.Unmarshal(c.After, &l)
		_, err := tx.Exec(`INSERT OR REPLACE INTO freeze_ledger (id, day, delta, habit_id, reason) VALUES (?,?,?,?,?)`,
			l.ID, l.Day, l.Delta, l.HabitID, l.Reason)
		return 0, err
	}
	return 0, fmt.Errorf("unknown op table %q", c.Table)
}

// ---- public mutations (all journaled, all undoable) ----

// SetEntry upserts one habit-day entry. A completion that is the habit's
// Nth global "done" may also earn a freeze token in the same op, so undoing
// the completion returns the token.
func (s *Store) SetEntry(e domain.Entry) error {
	before, err := entryQ(s.db, e.HabitID, e.Day)
	if err != nil {
		return err
	}
	h, err := habitByIDQ(s.db, e.HabitID)
	if err != nil {
		return err
	}
	if h == nil {
		return fmt.Errorf("no habit with id %d", e.HabitID)
	}
	op := Op{Desc: descEntry(*h, e), Changes: []Change{chg("entry", before, e)}}

	if e.Status == domain.StatusDone && (before == nil || before.Status != domain.StatusDone) {
		var doneCount int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM entry WHERE status = 'done'`).Scan(&doneCount); err != nil {
			return err
		}
		bal, err := s.FreezeBalance()
		if err != nil {
			return err
		}
		if domain.EarnsFreeze(doneCount+1, bal) {
			op.Changes = append(op.Changes, chg("ledger", nil, ledgerRow{
				ID: nextID(s.db, "freeze_ledger"), Day: e.Day, Delta: 1,
				HabitID: e.HabitID, Reason: "earned",
			}))
			op.Desc += " · ❄ +1"
		}
	}
	return s.mutate(op)
}

// ClearEntry removes a habit-day entry (quantified amount back to zero).
func (s *Store) ClearEntry(habitID int64, day domain.Day) error {
	before, err := entryQ(s.db, habitID, day)
	if err != nil || before == nil {
		return err
	}
	h, _ := habitByIDQ(s.db, habitID)
	slug := fmt.Sprint(habitID)
	if h != nil {
		slug = h.Slug
	}
	return s.mutate(Op{Desc: "clear " + slug, Changes: []Change{chg("entry", before, nil)}})
}

// CreateHabit inserts h (assigning ID, slug, position, created time).
func (s *Store) CreateHabit(h *domain.Habit) error {
	if h.Slug == "" {
		h.Slug = domain.Slugify(h.Name)
	}
	if h.Slug == "" {
		return errors.New("habit needs a name")
	}
	if h.Step == 0 {
		h.Step = 1
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now().UTC()
	}
	if err := s.checkSlugFree(h.Slug, 0); err != nil {
		return err
	}
	// ponytail: MAX+1 preallocation is safe — SQLite allows one writer at a
	// time and mutate() runs in a single transaction.
	h.ID = nextID(s.db, "habit")
	s.db.QueryRow(`SELECT COALESCE(MAX(position),0)+1 FROM habit WHERE group_id = ?`, h.GroupID).Scan(&h.Position)
	return s.mutate(Op{Desc: "new " + h.Slug, Changes: []Change{chg("habit", nil, *h)}})
}

// UpdateHabit replaces a habit row with h (full state, caller loaded it).
// Desc reflects what changed: archive, pause, resume, or plain edit.
func (s *Store) UpdateHabit(h domain.Habit) error {
	before, err := habitByIDQ(s.db, h.ID)
	if err != nil {
		return err
	}
	if before == nil {
		return fmt.Errorf("no habit with id %d", h.ID)
	}
	if err := s.checkSlugFree(h.Slug, h.ID); err != nil {
		return err
	}
	desc := "edit " + h.Slug
	switch {
	case before.ArchivedAt == nil && h.ArchivedAt != nil:
		desc = "archive " + h.Slug
	case before.ArchivedAt != nil && h.ArchivedAt == nil:
		desc = "restore " + h.Slug
	case before.PausedAt == nil && h.PausedAt != nil:
		desc = "pause " + h.Slug
	case before.PausedAt != nil && h.PausedAt == nil:
		desc = "resume " + h.Slug
	}
	return s.mutate(Op{Desc: desc, Changes: []Change{chg("habit", *before, h)}})
}

// SwapPositions reorders two habits within a group as one undo step.
func (s *Store) SwapPositions(a, b domain.Habit) error {
	a2, b2 := a, b
	a2.Position, b2.Position = b.Position, a.Position
	return s.mutate(Op{Desc: "reorder " + a.Slug, Changes: []Change{
		chg("habit", a, a2), chg("habit", b, b2),
	}})
}

// checkSlugFree guards habit writes: rows are applied with INSERT OR
// REPLACE, which would silently swallow an existing habit on a slug
// collision instead of erroring.
func (s *Store) checkSlugFree(slug string, selfID int64) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM habit WHERE slug = ? AND id != ?`, slug, selfID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("a habit with slug %q already exists", slug)
	}
	return nil
}

func nextID(q dbq, table string) int64 {
	var id int64
	q.QueryRow(`SELECT COALESCE(MAX(id),0)+1 FROM ` + table).Scan(&id)
	return id
}

func descEntry(h domain.Habit, e domain.Entry) string {
	switch e.Status {
	case domain.StatusSkip:
		if e.SkipReason != "" && e.SkipReason != "none" {
			return fmt.Sprintf("skip %s (%s)", h.Slug, e.SkipReason)
		}
		return "skip " + h.Slug
	case domain.StatusPartial:
		return fmt.Sprintf("log %s %g/%g %s", h.Slug, e.Amount, h.Target, h.Unit)
	default:
		if h.Kind == domain.Quantified {
			return fmt.Sprintf("done %s %g %s", h.Slug, e.Amount, h.Unit)
		}
		return "done " + h.Slug
	}
}

// ---- undo / redo ----

type journalRow struct {
	id int64
	at time.Time
	op Op
}

func (s *Store) journalRow(q dbq, id int64) (*journalRow, error) {
	var at, opJSON string
	err := q.QueryRow(`SELECT at, op FROM journal WHERE id = ?`, id).Scan(&at, &opJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r := &journalRow{id: id, at: parseTime(at)}
	if err := json.Unmarshal([]byte(opJSON), &r.op); err != nil {
		return nil, err
	}
	return r, nil
}

// Undo reverses the op at the cursor and moves the cursor down. Ops from
// previous logical days are out of reach — undo means "revert what I just
// did", not time travel.
func (s *Store) Undo() (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	cursor := metaInt(tx, "undo_cursor")
	if cursor == 0 {
		return "", ErrNothingToUndo
	}
	row, err := s.journalRow(tx, cursor)
	if err != nil {
		return "", err
	}
	if row == nil || domain.LogicalDay(row.at, s.opt.RolloverHour) != s.Today() {
		return "", ErrNothingToUndo
	}
	if err := s.applyChanges(tx, row.op.Changes, true); err != nil {
		return "", err
	}
	var prev int64
	tx.QueryRow(`SELECT COALESCE(MAX(id),0) FROM journal WHERE id < ?`, cursor).Scan(&prev)
	if err := metaSet(tx, "undo_cursor", fmt.Sprint(prev)); err != nil {
		return "", err
	}
	return row.op.Desc, tx.Commit()
}

// Redo re-applies the op just above the cursor, if any survived.
func (s *Store) Redo() (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	cursor := metaInt(tx, "undo_cursor")
	var next int64
	tx.QueryRow(`SELECT COALESCE(MIN(id),0) FROM journal WHERE id > ?`, cursor).Scan(&next)
	if next == 0 {
		return "", ErrNothingToRedo
	}
	row, err := s.journalRow(tx, next)
	if err != nil {
		return "", err
	}
	if err := s.applyChanges(tx, row.op.Changes, false); err != nil {
		return "", err
	}
	if err := metaSet(tx, "undo_cursor", fmt.Sprint(next)); err != nil {
		return "", err
	}
	return row.op.Desc, tx.Commit()
}

// UndoDepth reports how many ops from today are undoable — the TUI shows
// the hint only when this is non-zero.
func (s *Store) UndoDepth() int {
	cursor := metaInt(s.db, "undo_cursor")
	var n int
	// ponytail: counts rows by wall-clock date prefix, close enough for a hint
	dayStart := s.Today()
	s.db.QueryRow(`SELECT COUNT(*) FROM journal WHERE id <= ? AND at >= ?`, cursor, string(dayStart)).Scan(&n)
	return n
}
