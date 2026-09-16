package operation_setting_test

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// TestPartnerPersistReentrantDispatch guards the production deadlock:
// model.UpdateOption dispatches back into LoadPartnerSettingFromJSONString on
// the same goroutine, so persist must run without holding partnerSettingMu.
func TestPartnerPersistDoesNotSelfDeadlock(t *testing.T) {
	reentrant := func(key, value string) error {
		operation_setting.LoadPartnerSettingFromJSONString(value)
		return nil
	}
	if _, err := operation_setting.UpsertPartnerMembers("dl-test", []int{1}, reentrant); err != nil {
		t.Fatalf("setup: %v", err)
	}
	done := make(chan error, 4)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			_, err := operation_setting.UpsertPartnerMembers("dl-test", []int{10 + n}, reentrant)
			done <- err
		}(i)
		go func() {
			defer wg.Done()
			_, err := operation_setting.UpdatePartnerContent("dl-test", "c", "n", reentrant)
			done <- err
		}()
	}
	wg.Wait()
	close(done)
	for err := range done {
		if err != nil {
			t.Fatalf("persist: %v", err)
		}
	}
}
