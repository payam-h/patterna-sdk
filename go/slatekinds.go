package patterna

import "sync"

const defaultSlateMemory = 4096

// slateKinds remembers, for the most recent slates, which items are not documents, so
// ReportInteractions can refuse an interaction without Kind before it is sent. Bounded FIFO.
type slateKinds struct {
	mu    sync.Mutex
	limit int
	m     map[string]map[string]string // request id -> item id -> kind (non-documents only)
	order []string
	next  int
}

func newSlateKinds(limit int) *slateKinds {
	if limit < 0 {
		return nil
	}
	return &slateKinds{limit: limit, m: make(map[string]map[string]string, limit), order: make([]string, limit)}
}

func (s *slateKinds) remember(sl *Slate) {
	if s == nil || sl == nil || sl.RequestID == "" {
		return
	}
	var kinds map[string]string
	for _, it := range sl.Items {
		if it.Kind != "" && it.Kind != string(KindDocument) {
			if kinds == nil {
				kinds = map[string]string{}
			}
			kinds[it.ItemID] = it.Kind
		}
	}
	if kinds == nil {
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
	s.m[sl.RequestID] = kinds
}

func (s *slateKinds) kind(requestID, itemID string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[requestID][itemID]
}
