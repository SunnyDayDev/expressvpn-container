package state

import "sync"

// Store хранит текущий снимок и рассылает изменения подписчикам.
// Подписчик всегда получает самый свежий снимок: если он не успевает читать,
// промежуточные снимки коалесцируются (канал ёмкости 1, старое вытесняется).
type Store struct {
	mu   sync.RWMutex
	cur  State
	subs map[chan State]struct{}
}

func NewStore(initial State) *Store {
	return &Store{cur: initial, subs: make(map[chan State]struct{})}
}

// Get возвращает копию текущего снимка.
func (s *Store) Get() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

// Update модифицирует снимок под блокировкой и уведомляет подписчиков.
func (s *Store) Update(f func(*State)) {
	s.mu.Lock()
	f(&s.cur)
	cur := s.cur
	subs := make([]chan State, 0, len(s.subs))
	for ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- cur:
		default:
			// Вытесняем устаревший снимок и кладём свежий.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- cur:
			default:
			}
		}
	}
}

// Subscribe возвращает канал снимков; текущий снимок кладётся сразу.
func (s *Store) Subscribe() (<-chan State, func()) {
	ch := make(chan State, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	ch <- s.cur
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
	return ch, cancel
}
