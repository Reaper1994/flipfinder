package main

import "sync"

// forEach calls fn(i) for i in [0, n) on at most limit goroutines and waits for
// all of them. fn must only write to state owned by index i; callers collect
// results in a slice and combine them afterwards so output order stays stable.
func forEach(n, limit int, fn func(i int)) {
	limit = max(1, min(limit, n))
	var wg sync.WaitGroup
	idx := make(chan int)
	for range limit {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				fn(i)
			}
		}()
	}
	for i := range n {
		idx <- i
	}
	close(idx)
	wg.Wait()
}
