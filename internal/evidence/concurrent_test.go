package evidence

import (
	"fmt"
	"sync"
	"testing"
)

func TestConcurrentEvidenceReceiptsAreAtomic(t *testing.T) {
	old := Dir
	Dir = t.TempDir()
	defer func() { Dir = old }()
	var wg sync.WaitGroup
	errors := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errors <- Put(Record{Project: "/fixture/project", Gate: "deploy", Result: "PASS", Source: fmt.Sprint(i)})
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Errorf("lost concurrent receipt: %v", err)
		}
	}
	if r, ok := Get("/fixture/project", "deploy"); !ok || r.Source == "" {
		t.Fatal("complete receipt missing")
	}
}
