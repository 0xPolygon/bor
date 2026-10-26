package p2p

// beginWork serializes admission with Close so the wait group cannot gain
// work after shutdown starts waiting, even when it was previously empty.
func (b *BulkSidecar) beginWork() bool {
	b.workMu.Lock()
	defer b.workMu.Unlock()
	if !b.accepting(nil) {
		return false
	}
	b.workWG.Add(1)
	return true
}

func (b *BulkSidecar) startWorker(work func()) {
	if !b.beginWork() {
		return
	}
	go func() {
		defer b.workWG.Done()
		work()
	}()
}
