package search

// HoldSlot occupies one search slot until the returned function runs.
func HoldSlot(e *Engine) func() {
	s, err := e.acquire()
	if err != nil {
		panic(err)
	}
	return func() { e.release(s) }
}
