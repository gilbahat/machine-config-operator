package daemon

import (
	"errors"
	"sync"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPollingConfigDriftMonitor(t *testing.T) {
	p := newPollingConfigDriftMonitor(5 * time.Millisecond)

	var mu sync.Mutex
	results := []error{errors.New("content mismatch for /etc/foo"), errors.New("content mismatch for /etc/foo"), nil, errors.New("content mismatch for /etc/foo"), errors.New("mode mismatch for /etc/bar")}
	calls := 0
	p.validate = func(ConfigDriftMonitorOpts) error {
		mu.Lock()
		defer mu.Unlock()
		if calls >= len(results) {
			return nil
		}
		err := results[calls]
		calls++
		return err
	}

	drifts := make(chan error, 10)
	opts := ConfigDriftMonitorOpts{
		OnDrift:       func(err error) { drifts <- err },
		ErrChan:       make(chan error, 1),
		MachineConfig: &mcfgv1.MachineConfig{},
	}

	assert.Error(t, p.Start(ConfigDriftMonitorOpts{}), "options are validated")
	require.NoError(t, p.Start(opts))
	assert.True(t, p.IsRunning())
	require.NoError(t, p.Start(opts), "starting twice is a no-op")

	var got []string
	timeout := time.After(5 * time.Second)
	for len(got) < 3 {
		select {
		case err := <-drifts:
			var cdErr *configDriftErr
			require.ErrorAs(t, err, &cdErr)
			got = append(got, err.Error())
		case <-timeout:
			t.Fatalf("timed out; drifts so far: %v", got)
		}
	}
	// The repeated identical drift is reported once; after the state recovers
	// the same drift is reported again, and a different drift is reported too.
	assert.Equal(t, []string{
		"content mismatch for /etc/foo",
		"content mismatch for /etc/foo",
		"mode mismatch for /etc/bar",
	}, got)

	done := p.Done()
	p.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Done() was not signalled on Stop()")
	}
	assert.False(t, p.IsRunning())
	p.Stop() // stopping again is a no-op

	select {
	case err := <-drifts:
		t.Fatalf("unexpected extra drift report: %v", err)
	default:
	}
}
