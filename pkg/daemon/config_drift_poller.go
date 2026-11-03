package daemon

import (
	"fmt"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// defaultConfigDriftPollInterval is how often the polling monitor re-validates
// the on-disk state.
const defaultConfigDriftPollInterval = time.Minute

// pollingConfigDriftMonitor is a ConfigDriftMonitor for hosts without a file
// notification API that fsnotify supports (AIX: fsnotify has no AHAFS
// backend). It periodically re-runs the same on-disk validation the inotify
// watcher runs on every file event, and reports a given drift once until the
// state changes, so a lasting drift does not flood the API server.
type pollingConfigDriftMonitor struct {
	interval time.Duration
	// validate is validateOnDiskState; replaceable in tests.
	validate func(ConfigDriftMonitorOpts) error

	mu      sync.Mutex
	running bool
	quit    chan struct{}
	wg      sync.WaitGroup
	stopCh  chan struct{}
}

func newPollingConfigDriftMonitor(interval time.Duration) *pollingConfigDriftMonitor {
	return &pollingConfigDriftMonitor{
		interval: interval,
		validate: func(o ConfigDriftMonitorOpts) error {
			return validateOnDiskState(o.MachineConfig, o.SystemdPath)
		},
		stopCh: make(chan struct{}),
	}
}

func (p *pollingConfigDriftMonitor) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *pollingConfigDriftMonitor) Start(opts ConfigDriftMonitorOpts) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return nil
	}
	switch {
	case opts.OnDrift == nil:
		return fmt.Errorf("no ondrift function attached")
	case opts.ErrChan == nil:
		return fmt.Errorf("no error channel provided")
	case opts.MachineConfig == nil:
		return fmt.Errorf("no machine config provided")
	}
	if opts.SystemdPath == "" {
		opts.SystemdPath = pathSystemd
	}

	p.quit = make(chan struct{})
	p.running = true
	p.wg.Add(1)
	go p.loop(opts, p.quit)
	klog.Infof("Config Drift Monitor started (polling every %s)", p.interval)
	return nil
}

func (p *pollingConfigDriftMonitor) loop(opts ConfigDriftMonitorOpts, quit <-chan struct{}) {
	defer p.wg.Done()
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	lastReported := ""
	for {
		select {
		case <-quit:
			return
		case <-ticker.C:
		}
		err := p.validate(opts)
		if err == nil {
			lastReported = ""
			continue
		}
		if msg := err.Error(); msg != lastReported {
			lastReported = msg
			opts.OnDrift(&configDriftErr{err})
		}
	}
}

func (p *pollingConfigDriftMonitor) Done() <-chan struct{} {
	out := make(chan struct{})
	go func() {
		<-p.stopCh
		out <- struct{}{}
		close(out)
	}()
	return out
}

func (p *pollingConfigDriftMonitor) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return
	}
	close(p.quit)
	p.wg.Wait()
	p.running = false
	klog.Info("Config Drift Monitor has shut down")
	p.stopCh <- struct{}{}
}
