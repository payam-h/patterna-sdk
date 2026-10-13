package patterna

import "sync"

const defaultSlateMemory = 4096

// slateKinds remembers, for the most recent slates, which items are not documents, so
// ReportInteractions can refuse an interaction without Kind before it is sent, and which slates
// were answered shown=false, so Abandon refuses them (the engine would answer 409 not_shown).
// Bounded FIFO.
type slateKinds struct {
	mu    sync.Mutex
	limit int
	m     map[string]slateMemo
	order []string
	next  int
}

type slateMemo struct {
	kinds    map[string]string // item id -> kind (non-documents only)
	notShown bool
}

func newSlateKinds(limit int) *slateKinds {
	if limit < 0 {
		return nil
	}
	return &slateKinds{limit: limit, m: make(map[string]slateMemo, limit), order: make([]string, limit)}
}

func (s *slateKinds) remember(sl *Slate) {
	if s == nil || sl == nil || sl.RequestID == "" {
		return
	}
	memo := slateMemo{notShown: !sl.Visible()}
	for _, it := range sl.Items {
		if it.Kind != "" && it.Kind != string(KindDocument) {
			if memo.kinds == nil {
				memo.kinds = map[string]string{}
			}
			memo.kinds[it.ItemID] = it.Kind
		}
	}
	if memo.kinds == nil && !memo.notShown {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[sl.RequestID]; !ok {
		if old := s.order[s.next]; old != "" {
			delete(s.m, old)
		}
		s.order[s.next] = sl.RequestID
		s.next = (s.next + 1) % s.limit
	}
	s.m[sl.RequestID] = memo
}

func (s *slateKinds) kind(requestID, itemID string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[requestID].kinds[itemID]
}

// notShown reports whether this client received requestID as a shown=false answer.
func (s *slateKinds) notShown(requestID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[requestID].notShown
}
