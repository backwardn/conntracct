package bpf

import (
	"fmt"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"github.com/ti-mo/gobpf/elf"

	"github.com/ti-mo/conntracct/pkg/kernel"
)

const perfUpdateMap = "perf_acct_update"
const perfDestroyMap = "perf_acct_end"

// Probe is an instance of a BPF probe running in the kernel.
type Probe struct {

	// gobpf/elf objects.
	module      *elf.Module
	perfUpdate  *elf.PerfMap
	perfDestroy *elf.PerfMap

	// Target kernel of the loaded probe.
	kernel kernel.Kernel

	// List of event consumers of the probe.
	consumerMu sync.RWMutex
	consumers  []*Consumer

	// Channel for receiving IDs of lost perf events.
	lostChan chan uint64

	// Communication channels with the perfWorker.
	perfUpdateChan  chan []byte
	perfDestroyChan chan []byte
	errChan         chan error

	// Started status of the probe.
	startMu sync.Mutex
	started bool

	stats *ProbeStats
}

// NewProbe instantiates an Probe using the given Config.
// Loads the BPF program into the kernel but does not attach its kprobes yet.
func NewProbe(cfg Config) (*Probe, error) {

	kr, err := kernelRelease()
	if err != nil {
		return nil, err
	}

	// Select the correct BPF probe from the library.
	br, k, err := Select(kr)
	if err != nil {
		return nil, errors.Wrap(err, "selecting BPF probe")
	}

	// Instantiate Probe with selected target kernel struct.
	ap := Probe{
		kernel: k,
		stats:  &ProbeStats{},
	}

	// Scan kallsyms before attempting BPF load to avoid arcane error output from eBPF attach.
	err = checkProbeKsyms(k.Probes)
	if err != nil {
		return nil, err
	}

	// Load the module from the bytes.Reader and insert into the kernel.
	ap.module = elf.NewModuleFromReader(br)
	if err := ap.module.Load(nil); err != nil {
		// Error string from go-bpf can contain many NUL characters and need to be trimmed.
		err = errors.New(strings.TrimRight(err.Error(), "\x00"))
		return nil, errors.Wrap(err, fmt.Sprintf("failed to load ELF binary version %s", k.Version))
	}

	// Apply probe configuration.
	if err := configureProbe(ap.module, cfg); err != nil {
		return nil, errors.Wrap(err, "configuring BPF probe")
	}

	return &ap, nil
}

// Start attaches the BPF program's kprobes and starts polling the perf ring buffer.
func (ap *Probe) Start() error {

	ap.startMu.Lock()
	defer ap.startMu.Unlock()

	if ap.started {
		return errProbeStarted
	}

	// Enable all kprobes in target kernel's probe list.
	for _, p := range ap.kernel.Probes {
		if err := ap.module.EnableKprobe(p, 0); err != nil {
			return errors.Wrap(err, "enabling kprobe")
		}
	}

	ap.perfUpdateChan = make(chan []byte, 1024)
	ap.perfDestroyChan = make(chan []byte, 1024)
	ap.lostChan = make(chan uint64)
	ap.errChan = make(chan error)

	// Set up perf maps with an event and lost channel.
	um, err := elf.InitPerfMap(ap.module, perfUpdateMap, ap.perfUpdateChan, ap.lostChan)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("InitPerfMap %s", perfUpdateMap))
	}
	ap.perfUpdate = um

	dm, err := elf.InitPerfMap(ap.module, perfDestroyMap, ap.perfDestroyChan, ap.lostChan)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("InitPerfMap %s", perfUpdateMap))
	}
	ap.perfDestroy = dm

	// Start the event message decoder and fanout worker.
	go ap.perfWorker()

	// Start worker counting the amount of lost messages.
	go ap.lostWorker()

	// Start polling the BPF perf ring buffer, into update and destroy chans.
	um.PollStart()
	dm.PollStart()

	ap.started = true

	return nil
}

// Stop stops the BPF program and releases all its related resources.
// Closes all Probe's channels. Can only be called after Start().
func (ap *Probe) Stop() error {

	ap.startMu.Lock()
	defer ap.startMu.Unlock()

	if !ap.started {
		return errProbeNotStarted
	}

	// Releases all gobpf-internal resources, including the perfMap poller.
	if err := ap.module.Close(); err != nil {
		return err
	}

	close(ap.lostChan)
	close(ap.perfUpdateChan)
	close(ap.perfDestroyChan)
	close(ap.errChan)

	return nil
}

// Kernel returns the target kernel structure of the selected probe.
func (ap *Probe) Kernel() kernel.Kernel {
	return ap.kernel
}

// ErrChan returns an initialized Probe's unbuffered error channel.
// The error channel is unbuffered because it doesn't make sense to have
// stale error data. If there is no ready consumer on the channel, errors
// are dropped.
// Returns nil if the Probe has not been Start()ed yet.
func (ap *Probe) ErrChan() chan error {
	return ap.errChan
}

// Stats returns a snapshot copy of the Probe's statistics.
func (ap *Probe) Stats() ProbeStats {
	return ap.stats.Get()
}

// sendError safely sends a message on the Probe's unbuffered errChan.
// If there is no ready channel receiver, sendError is a no-op. A return value
// of true means the error was successfully sent on the channel.
func (ap *Probe) sendError(err error) bool {
	select {
	case ap.errChan <- err:
		return true
	default:
		return false
	}
}

// perfWorker reads binary events from the Probe's event channel,
// unmarshals the events into Events and sends them on all registered
// consumers' event channels. Exits if perfUpdateChan or perfDestroyChan are closed.
func (ap *Probe) perfWorker() {

	var eb []byte
	var ok, update bool

	for {
		select {
		case eb, ok = <-ap.perfUpdateChan:
			update = true
			ap.stats.incrPerfEventsUpdate()
		case eb, ok = <-ap.perfDestroyChan:
			update = false
			ap.stats.incrPerfEventsDestroy()
		}

		if !ok {
			// Channel closed.
			return
		}

		var ae Event
		if err := ae.unmarshalBinary(eb); err != nil {
			ap.sendError(errors.Wrap(err, "error unmarshaling Event byte array"))
		}

		// Fanout to all registered consumers.
		ap.fanoutEvent(ae, update)
	}
}

// lostWorker increments the Probe's lost field for every message
// received on its lostChan. Exits if lostChan is closed.
func (ap *Probe) lostWorker() {

	for {
		_, ok := <-ap.lostChan
		if !ok {
			// Channel closed.
			return
		}

		ap.stats.incrPerfEventsLost()
	}
}

// fanoutEvent sends the given Event to all registered consumers.
// The update flag specifies whether the event is an update (true) or destroy
// (false) event.
func (ap *Probe) fanoutEvent(ae Event, update bool) {

	// Take a read lock on the consumers so we don't send to closed or already
	// unregistered consumer channels.
	ap.consumerMu.RLock()

	for _, c := range ap.consumers {
		// Require the update/destroy condition of the event to match
		// the requested event type of the consumer.
		if (update && c.WantUpdate()) || (!update && c.WantDestroy()) {
			// Non-blocking send to the consumer's event channel.
			select {
			case c.events <- ae:
				c.stats.setQueueLength(len(c.events))
				c.stats.incrEventsReceived()
			default:
				// If the channel can't be written to immediately,
				// increment the consumer's lost counter.
				c.stats.incrEventsLost()
			}
		}
	}

	ap.consumerMu.RUnlock()
}
