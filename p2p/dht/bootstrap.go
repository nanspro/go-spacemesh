package dht

import (
	"errors"
	"github.com/btcsuite/btcutil/base58"
	"github.com/spacemeshos/go-spacemesh/crypto"
	"github.com/spacemeshos/go-spacemesh/log"
	"github.com/spacemeshos/go-spacemesh/p2p/node"
	"time"
)

const (
	// BootstrapTimeout is the maximum time we allow the bootstrap process to extend
	BootstrapTimeout = 5 * time.Minute
	// LookupIntervals is the time we wait between another kad lookup if bootstrap failed.
	LookupIntervals = 3 * time.Second
	// RefreshInterval is the time we wait between dht refreshes
	RefreshInterval = 5 * time.Minute
)

var (
	// ErrZeroConnections - we can't start the node without connecting
	ErrZeroConnections = errors.New("can't bootstrap minimum connections set to 0")
	// ErrConnectToBootNode is returned when a connection with the boot node is failed.
	ErrConnectToBootNode = errors.New("failed to read or connect to any boot node")
	// ErrFoundOurself is returned when a node sends us ourselves.
	ErrFoundOurself = errors.New("found ourselves in the routing table")
	// ErrFailedToBoot is returned when we exceed the BootstrapTimeout
	ErrFailedToBoot = errors.New("failed to bootstrap within time limit")
)

// Bootstrap issues a bootstrap by inserting the preloaded nodes to the routing table then querying them with our
// ID with a FindNode (using `dht.Lookup`). the process involves updating all returned nodes to the routing table
// while all the nodes that receive our query will add us to their routing tables and send us as response to a `FindNode`.
func (d *KadDHT) Bootstrap() error {
	d.local.Debug("Starting node bootstrap ", d.local.String())

	c := d.config.RandomConnections
	if c <= 0 {
		return ErrZeroConnections
	}
	// register bootstrap nodes
	bn := 0
	for _, n := range d.config.BootstrapNodes {
		node, err := node.NewNodeFromString(n)
		if err != nil {
			// TODO : handle errors
			continue
		}
		d.rt.Update(node)
		bn++
		log.Info("added new bootstrap node %v", node)
	}

	if bn == 0 {
		return ErrConnectToBootNode
	}

	d.local.Debug("lookup using %d preloaded bootnodes ", bn)

	timeout := time.NewTimer(BootstrapTimeout)
	i := 0
	searchFor := d.local.PublicKey().String() //me
	// TODO: Issue a healthcheck / refresh loop every x interval.
BOOTLOOP:
	for {
		go func() {
			_, err := d.Lookup(searchFor)
			d.reschan <- err
		}()

		select {
		case <-timeout.C:
			return ErrFailedToBoot
		case err, chState := <-d.reschan:
			i++
			if err == nil {
				return ErrFoundOurself
			}
			if chState == false { //channel closed
				return ErrFailedToBoot
			}
			// We want to have lookup failed error
			// no one should return us ourselves.
			req := make(chan int)
			d.rt.Size(req)
			size := <-req
			if size >= c { // Don't count bootstrap nodes
				break BOOTLOOP
			}
			d.local.Warning("%d lookup didn't bootstrap the routing table", i)
			d.local.Warning("RT now has %d peers", size)

			if size > bn {
				d.local.Warning("Looking up ourselves didn't fill routing table, trying random address")
				randompeer, err := crypto.GetRandomBytes(32)
				if err == nil {
					searchFor = base58.Encode(randompeer)
				}
			}
			time.Sleep(LookupIntervals)
		}
	}
	// if we finished looking up ourselves extend the search with another random ID
	go func() {
		randompeer, _ := crypto.GetRandomBytes(32)
		_, err := d.Lookup(base58.Encode(randompeer))
		d.reschan <- err
	}()

	timeout.Reset(BootstrapTimeout / 2)

	select {
	case <-timeout.C:
		return ErrFailedToBoot
	case err, chState := <-d.reschan:

		if err == nil {
			return ErrFoundOurself
		}
		if chState == false { //channel closed
			return ErrFailedToBoot
		}

		break

	}

	return nil // succeed
}

func (d *KadDHT) Close() {
	close(d.reschan)
}

func (d *KadDHT) healthLoop() {
	tick := time.NewTicker(RefreshInterval)
	for range tick.C {
		err := d.refresh()
		if err != nil {
			d.local.Error("DHT Refresh failed, trying again")
		}
	}
}

func (d *KadDHT) refresh() error {
	reschan := make(chan error)

	go func() {
		_, err := d.Lookup(d.local.PublicKey().String())
		reschan <- err
	}()

	select {
	case err := <-reschan:
		if err == nil {
			return ErrFoundOurself
		}
		// should be - ErrLookupFailed
		// We want to have lookup failed error
		// no one should return us ourselves.
	}

	return nil
}