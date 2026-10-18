package idx

import (
	"sync"

	"redx/box"
)

// Store is the storage idx wraps. It has exactly the shape of link.Store, so
// a link.Store can be wrapped and the wrapper can be handed to link.NewEngine.
type Store interface {
	Get(a Addr) (*box.Entry, bool, error)
	Put(a Addr, e *box.Entry) error
	Delete(a Addr) (bool, error)
	Scan(file string, fn func(Addr, *box.Entry) bool) error
	Files() []string
}

// ObservedStore wraps a Store and tells its observers about every successful
// write. Puts and deletes through one ObservedStore are serialized, so
// observers see them in the order they were applied to the inner store.
// Writes that bypass the wrapper are not observed (use Index.Rebuild or
// Manager.Load to catch up).
type ObservedStore struct {
	inner Store

	mu  sync.Mutex
	obs []Observer
}

// NewObservedStore wraps inner.
func NewObservedStore(inner Store, obs ...Observer) *ObservedStore {
	return &ObservedStore{inner: inner, obs: obs}
}

// AddObserver registers another observer for later writes.
func (o *ObservedStore) AddObserver(ob Observer) {
	o.mu.Lock()
	o.obs = append(o.obs, ob)
	o.mu.Unlock()
}

func (o *ObservedStore) Get(a Addr) (*box.Entry, bool, error) { return o.inner.Get(a) }
func (o *ObservedStore) Files() []string                      { return o.inner.Files() }
func (o *ObservedStore) Scan(file string, fn func(Addr, *box.Entry) bool) error {
	return o.inner.Scan(file, fn)
}

// Put writes through and, if the write succeeded, notifies observers.
func (o *ObservedStore) Put(a Addr, e *box.Entry) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.inner.Put(a, e); err != nil {
		return err
	}
	for _, ob := range o.obs {
		ob.OnPut(a, e)
	}
	return nil
}

// Delete deletes through and, if a record was removed, notifies observers.
func (o *ObservedStore) Delete(a Addr) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ok, err := o.inner.Delete(a)
	if err != nil {
		return ok, err
	}
	if ok {
		for _, ob := range o.obs {
			ob.OnDelete(a)
		}
	}
	return ok, nil
}

// Locked runs fn while no write through the wrapper can start or finish.
// Use it to read a source sequence number and save a snapshot that belong
// together.
func (o *ObservedStore) Locked(fn func()) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fn()
}
