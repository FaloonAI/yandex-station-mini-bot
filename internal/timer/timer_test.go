package timer

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAddTimerAndTrigger(t *testing.T) {
	mgr := NewManager()
	defer mgr.Close()

	var triggered int32
	done := make(chan struct{})

	id := mgr.AddTimer(20*time.Millisecond, "Тестовый чай", func() {
		atomic.AddInt32(&triggered, 1)
		close(done)
	})

	if id == "" {
		t.Fatal("expected non-empty timer ID")
	}

	active := mgr.ListActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active timer, got %d", len(active))
	}
	if active[0].ID != id {
		t.Errorf("expected timer ID %s, got %s", id, active[0].ID)
	}
	if active[0].Label != "Тестовый чай" {
		t.Errorf("expected label 'Тестовый чай', got '%s'", active[0].Label)
	}
	if active[0].IsAlarm {
		t.Errorf("expected IsAlarm to be false for timer")
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timer trigger timed out")
	}

	if atomic.LoadInt32(&triggered) != 1 {
		t.Errorf("expected triggered count 1, got %d", atomic.LoadInt32(&triggered))
	}

	// Wait briefly for deletion from manager
	time.Sleep(10 * time.Millisecond)
	if count := mgr.Count(); count != 0 {
		t.Errorf("expected 0 active timers after trigger, got %d", count)
	}
}

func TestAddAlarm(t *testing.T) {
	mgr := NewManager()
	defer mgr.Close()

	var triggered int32
	done := make(chan struct{})

	target := time.Now().Add(25 * time.Millisecond)
	id := mgr.AddAlarm(target, "Утренний подъем", func() {
		atomic.AddInt32(&triggered, 1)
		close(done)
	})

	if id == "" {
		t.Fatal("expected non-empty alarm ID")
	}

	active := mgr.ListActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alarm, got %d", len(active))
	}
	if !active[0].IsAlarm {
		t.Errorf("expected IsAlarm to be true")
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("alarm trigger timed out")
	}

	if atomic.LoadInt32(&triggered) != 1 {
		t.Errorf("expected alarm triggered count 1, got %d", atomic.LoadInt32(&triggered))
	}
}

func TestCancelTimer(t *testing.T) {
	mgr := NewManager()
	defer mgr.Close()

	var triggered int32
	id := mgr.AddTimer(100*time.Millisecond, "Отменяемый", func() {
		atomic.AddInt32(&triggered, 1)
	})

	if !mgr.Cancel(id) {
		t.Errorf("expected Cancel to return true for existing timer")
	}

	if mgr.Cancel(id) {
		t.Errorf("expected second Cancel to return false")
	}

	if mgr.Count() != 0 {
		t.Errorf("expected 0 active timers, got %d", mgr.Count())
	}

	time.Sleep(150 * time.Millisecond)
	if atomic.LoadInt32(&triggered) != 0 {
		t.Errorf("cancelled timer should not have triggered")
	}
}

func TestListActiveOrdering(t *testing.T) {
	mgr := NewManager()
	defer mgr.Close()

	id3 := mgr.AddTimer(300*time.Millisecond, "Третий", nil)
	id1 := mgr.AddTimer(100*time.Millisecond, "Первый", nil)
	id2 := mgr.AddTimer(200*time.Millisecond, "Второй", nil)

	active := mgr.ListActive()
	if len(active) != 3 {
		t.Fatalf("expected 3 timers, got %d", len(active))
	}

	if active[0].ID != id1 || active[1].ID != id2 || active[2].ID != id3 {
		t.Errorf("timers not ordered by target time: got [%s, %s, %s]",
			active[0].Label, active[1].Label, active[2].Label)
	}
}

func TestConcurrentAccess(t *testing.T) {
	mgr := NewManager()
	defer mgr.Close()

	const workers = 20
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				id := mgr.AddTimer(50*time.Millisecond, "concurrency-test", func() {})
				_ = mgr.ListActive()
				if j%2 == 0 {
					_ = mgr.Cancel(id)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestPanicSafetyInTrigger(t *testing.T) {
	mgr := NewManager()
	defer mgr.Close()

	done := make(chan struct{})
	mgr.AddTimer(10*time.Millisecond, "Паникующий таймер", func() {
		defer close(done)
		panic("boom!")
	})

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for trigger")
	}

	// Manager should continue functioning normally
	id := mgr.AddTimer(50*time.Millisecond, "Нормальный", nil)
	if id == "" {
		t.Errorf("manager failed to add timer after panic recovery")
	}
}
