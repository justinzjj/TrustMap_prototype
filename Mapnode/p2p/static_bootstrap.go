package p2p

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

type StaticBootstrapConfig struct {
	RetryInterval time.Duration
	DialTimeout   time.Duration
}

type StaticBootstrapper struct {
	host   host.Host
	peers  []peer.AddrInfo
	config StaticBootstrapConfig
	ready  func(bool)
}

func NewStaticBootstrapper(h host.Host, peers []peer.AddrInfo, config StaticBootstrapConfig, ready func(bool)) (*StaticBootstrapper, error) {
	if h == nil || ready == nil {
		return nil, errors.New("static bootstrap requires host and readiness callback")
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = 100 * time.Millisecond
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = 2 * time.Second
	}
	seen := make(map[peer.ID]struct{}, len(peers))
	copyPeers := make([]peer.AddrInfo, len(peers))
	for index, info := range peers {
		if info.ID == "" || info.ID == h.ID() || len(info.Addrs) == 0 {
			return nil, errors.New("invalid static bootstrap peer")
		}
		if _, exists := seen[info.ID]; exists {
			return nil, errors.New("duplicate static bootstrap peer")
		}
		seen[info.ID] = struct{}{}
		copyPeers[index] = peer.AddrInfo{ID: info.ID, Addrs: append([]ma.Multiaddr(nil), info.Addrs...)}
	}
	return &StaticBootstrapper{host: h, peers: copyPeers, config: config, ready: ready}, nil
}

func (bootstrapper *StaticBootstrapper) Run(ctx context.Context) error {
	if bootstrapper == nil || bootstrapper.host == nil || bootstrapper.ready == nil {
		return errors.New("static bootstrapper is not initialized")
	}
	if len(bootstrapper.peers) == 0 {
		bootstrapper.ready(true)
		<-ctx.Done()
		return ctx.Err()
	}
	ticker := time.NewTicker(bootstrapper.config.RetryInterval)
	defer ticker.Stop()
	for {
		bootstrapper.connectRound(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (bootstrapper *StaticBootstrapper) connectRound(ctx context.Context) {
	connected := false
	for _, info := range bootstrapper.peers {
		if bootstrapper.host.Network().Connectedness(info.ID) == network.Connected {
			connected = true
			break
		}
	}
	bootstrapper.ready(connected)
	var wait sync.WaitGroup
	for _, info := range bootstrapper.peers {
		if bootstrapper.host.Network().Connectedness(info.ID) == network.Connected {
			continue
		}
		wait.Add(1)
		go func(info peer.AddrInfo) {
			defer wait.Done()
			dialCtx, cancel := context.WithTimeout(ctx, bootstrapper.config.DialTimeout)
			defer cancel()
			if bootstrapper.host.Connect(dialCtx, info) == nil {
				bootstrapper.ready(true)
			}
		}(info)
	}
	wait.Wait()
}
