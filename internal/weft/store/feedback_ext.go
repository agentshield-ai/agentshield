package store

import (
	"database/sql"
	"sync"
)

// feedback.queue_id links feedback written while reviewing an annotation
// queue to that queue (NULL otherwise), as in Weave.
func init() {
	registerMigration(func(db *sql.DB) error {
		ok, err := HasColumn(db, "feedback", "queue_id")
		if err != nil || ok {
			return err
		}
		if _, err := db.Exec(`ALTER TABLE feedback ADD COLUMN queue_id TEXT`); err != nil {
			return err
		}
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS feedback_queue ON feedback(project_id, queue_id)`)
		return err
	})
}

// Feedback-created hooks let feature modules (automations) react to new
// feedback no matter which code path wrote it.
var (
	feedbackHookMu sync.RWMutex
	feedbackHooks  = map[*Store]map[int]func(Feedback){}
	feedbackHookID int
)

// OnFeedbackCreated registers fn to run (synchronously, after commit) for
// every feedback row this store creates. It returns a function that removes
// the hook. fn must not block.
func (s *Store) OnFeedbackCreated(fn func(Feedback)) (remove func()) {
	feedbackHookMu.Lock()
	defer feedbackHookMu.Unlock()
	feedbackHookID++
	id := feedbackHookID
	if feedbackHooks[s] == nil {
		feedbackHooks[s] = map[int]func(Feedback){}
	}
	feedbackHooks[s][id] = fn
	return func() {
		feedbackHookMu.Lock()
		defer feedbackHookMu.Unlock()
		delete(feedbackHooks[s], id)
		if len(feedbackHooks[s]) == 0 {
			delete(feedbackHooks, s)
		}
	}
}

func (s *Store) fireFeedbackCreated(f *Feedback) {
	feedbackHookMu.RLock()
	hooks := make([]func(Feedback), 0, len(feedbackHooks[s]))
	for _, h := range feedbackHooks[s] {
		hooks = append(hooks, h)
	}
	feedbackHookMu.RUnlock()
	for _, h := range hooks {
		h(*f)
	}
}
