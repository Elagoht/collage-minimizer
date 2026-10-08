package minimizer

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
)

// TestConcurrentRendersAreCounted renders pages and documents from many
// goroutines at once, as collage does for concurrent requests, while the summary is
// read. Under -race it fails if the byte counters are not safe for that; without
// it, the totals still have to add up.
func TestConcurrentRendersAreCounted(t *testing.T) {
	p := New()
	p.log = slog.New(slog.DiscardHandler)
	page := []byte("<p>\n    <b>Hello</b>   world\n  </p>\n")
	doc := []byte("{ \"a\" : 1 }")
	const workers, rounds = 8, 50

	var wg sync.WaitGroup
	for range workers {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range rounds {
				ev := &collage.AfterRenderEvent{HTML: append([]byte(nil), page...)}
				if err := p.OnAfterRender(context.Background(), ev); err != nil {
					t.Error(err)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range rounds {
				ev := &collage.DocumentRenderedEvent{ContentType: "application/json", Body: append([]byte(nil), doc...)}
				if err := p.OnDocumentRendered(context.Background(), ev); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range rounds {
			_ = p.Shutdown(context.Background())
		}
	}()
	wg.Wait()

	want := int64(workers * rounds * (len(page) + len(doc)))
	if got := p.savedIn.Load(); got != want {
		t.Fatalf("bytes in = %d, want %d", got, want)
	}
	if out := p.savedOut.Load(); out <= 0 || out >= want {
		t.Fatalf("bytes out = %d, want between 0 and %d", out, want)
	}
}
