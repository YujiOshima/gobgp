// Copyright (C) 2014 Nippon Telegraph and Telephone Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/armon/go-radix"
	"github.com/eapache/channels"
	"github.com/osrg/gobgp/config"
	"github.com/osrg/gobgp/packet/bgp"
	"github.com/osrg/gobgp/packet/bmp"
	"github.com/osrg/gobgp/table"
	"github.com/osrg/gobgp/zebra"
	"github.com/satori/go.uuid"
)

var policyMutex sync.RWMutex

type SenderMsg struct {
	ch  chan *FsmOutgoingMsg
	msg *FsmOutgoingMsg
}

type broadcastMsg interface {
	send()
}

type broadcastGrpcMsg struct {
	req    *Request
	result *Response
	done   bool
}

func (m *broadcastGrpcMsg) send() {
	m.req.ResponseCh <- m.result
	if m.done == true {
		close(m.req.ResponseCh)
	}
}

type broadcastBGPMsg struct {
	message      *bgp.BGPMessage
	peerAS       uint32
	localAS      uint32
	peerAddress  net.IP
	localAddress net.IP
	fourBytesAs  bool
	ch           chan *broadcastBGPMsg
}

func (m *broadcastBGPMsg) send() {
	m.ch <- m
}

type Watchers map[watcherType]watcher

func (ws Watchers) watching(typ watcherEventType) bool {
	for _, w := range ws {
		for _, ev := range w.watchingEventTypes() {
			if ev == typ {
				return true
			}
		}
	}
	return false
}

type TCPListener struct {
	l  *net.TCPListener
	ch chan struct{}
}

func (l *TCPListener) Close() error {
	if err := l.l.Close(); err != nil {
		return err
	}
	t := time.NewTicker(time.Second)
	select {
	case <-l.ch:
	case <-t.C:
		return fmt.Errorf("close timeout")
	}
	return nil
}

// avoid mapped IPv6 address
func NewTCPListener(address string, port uint32, ch chan *net.TCPConn) (*TCPListener, error) {
	proto := "tcp4"
	if ip := net.ParseIP(address); ip == nil {
		return nil, fmt.Errorf("can't listen on %s", address)
	} else if ip.To4() == nil {
		proto = "tcp6"
	}
	addr, err := net.ResolveTCPAddr(proto, net.JoinHostPort(address, strconv.Itoa(int(port))))
	if err != nil {
		return nil, err
	}

	l, err := net.ListenTCP(proto, addr)
	if err != nil {
		return nil, err
	}
	closeCh := make(chan struct{})
	go func() error {
		for {
			conn, err := l.AcceptTCP()
			if err != nil {
				close(closeCh)
				log.Warn(err)
				return err
			}
			ch <- conn
		}
	}()
	return &TCPListener{
		l:  l,
		ch: closeCh,
	}, nil
}

type BgpServer struct {
	bgpConfig     config.Bgp
	fsmincomingCh *channels.InfiniteChannel
	fsmStateCh    chan *FsmMsg
	acceptCh      chan *net.TCPConn
	zapiMsgCh     chan *zebra.Message

	GrpcReqCh     chan *Request
	policy        *table.RoutingPolicy
	broadcastReqs []*Request
	broadcastMsgs []broadcastMsg
	listeners     []*TCPListener
	neighborMap   map[string]*Peer
	globalRib     *table.TableManager
	zclient       *zebra.Client
	roaManager    *roaManager
	shutdown      bool
	watchers      Watchers
}

func NewBgpServer() *BgpServer {
	roaManager, _ := NewROAManager(0)
	return &BgpServer{
		MsgCh:       make(chan []*SenderMsg, 1),
		neighborMap: make(map[string]*Peer),
		watchers:    Watchers(make(map[watcherType]watcher)),
		policy:      table.NewRoutingPolicy(),
		roaManager:  roaManager,
	}
}

func (server *BgpServer) notify2watchers(typ watcherEventType, ev watcherEvent) error {
	for _, watcher := range server.watchers {
		if ch := watcher.notify(typ); ch != nil {
			server.broadcastMsgs = append(server.broadcastMsgs, &broadcastWatcherMsg{
				ch:    ch,
				event: ev,
			})
		}
	}
	return nil
}

func (server *BgpServer) Listeners(addr string) []*net.TCPListener {
	list := make([]*net.TCPListener, 0, len(server.listeners))
	rhs := net.ParseIP(addr).To4() != nil
	for _, l := range server.listeners {
		host, _, _ := net.SplitHostPort(l.l.Addr().String())
		lhs := net.ParseIP(host).To4() != nil
		if lhs == rhs {
			list = append(list, l.l)
		}
	}
	return list
}

func (server *BgpServer) Serve() {
	w, _ := newGrpcIncomingWatcher()
	server.watchers[WATCHER_GRPC_INCOMING] = w

	senderCh := make(chan *SenderMsg, 1<<16)
	go func(ch chan *SenderMsg) {
		w := func(c chan *FsmOutgoingMsg, msg *FsmOutgoingMsg) {
			// nasty but the peer could already become non established state before here.
			defer func() { recover() }()
			c <- msg
		}

		for m := range ch {
			// TODO: must be more clever. Slow peer makes other peers slow too.
			w(m.ch, m.msg)
		}

	}(senderCh)

	broadcastCh := make(chan broadcastMsg, 8)
	go func(ch chan broadcastMsg) {
		for {
			m := <-ch
			m.send()
		}
	}(broadcastCh)

	server.listeners = make([]*TCPListener, 0, 2)
	server.fsmincomingCh = channels.NewInfiniteChannel()
	server.fsmStateCh = make(chan *FsmMsg, 4096)
	var senderMsgs []*SenderMsg

	handleFsmMsg := func(e *FsmMsg) {
		peer, found := server.neighborMap[e.MsgSrc]
		if !found {
			log.Warn("Can't find the neighbor ", e.MsgSrc)
			return
		}
		if e.Version != peer.fsm.version {
			log.Debug("FSM Version inconsistent")
			return
		}
		m := server.handleFSMMessage(peer, e)
		if len(m) > 0 {
			senderMsgs = append(senderMsgs, m...)
		}
	}

	for {
		var firstMsg *SenderMsg
		var sCh chan *SenderMsg
		if len(senderMsgs) > 0 {
			sCh = senderCh
			firstMsg = senderMsgs[0]
		}
		var firstBroadcastMsg broadcastMsg
		var bCh chan broadcastMsg
		if len(server.broadcastMsgs) > 0 {
			bCh = broadcastCh
			firstBroadcastMsg = server.broadcastMsgs[0]
		}

		passConn := func(conn *net.TCPConn) {
			remoteAddr, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
			peer, found := server.neighborMap[remoteAddr]
			if found {
				if peer.fsm.adminState != ADMIN_STATE_UP {
					log.Debug("new connection for non admin-state-up peer ", remoteAddr, peer.fsm.adminState)
					conn.Close()
					return
				}
				localAddrValid := func(laddr net.IP) bool {
					if laddr == nil {
						return true
					}
					l := conn.LocalAddr()
					if l == nil {
						// already closed
						return false
					}

					host, _, _ := net.SplitHostPort(l.String())
					if host != laddr.String() {
						log.WithFields(log.Fields{
							"Topic":           "Peer",
							"Key":             remoteAddr,
							"Configured addr": laddr.String(),
							"Addr":            host,
						}).Info("Mismatched local address")
						return false
					}
					return true
				}(net.ParseIP(peer.fsm.pConf.Transport.Config.LocalAddress))
				if localAddrValid == false {
					conn.Close()
					return
				}
				log.Debug("accepted a new passive connection from ", remoteAddr)
				peer.PassConn(conn)
			} else {
				log.Info("can't find configuration for a new passive connection from ", remoteAddr)
				conn.Close()
			}
		}

		select {
		case msg := <-server.MsgCh:
			if len(msg) > 0 {
				senderMsgs = append(senderMsgs, msg...)
			}
		case conn := <-server.acceptCh:
			passConn(conn)
		default:
		}

		for {
			select {
			case e := <-server.fsmStateCh:
				handleFsmMsg(e)
			default:
				goto CONT
			}
		}
	CONT:

		select {
		case rmsg := <-server.roaManager.ReceiveROA():
			server.roaManager.HandleROAEvent(rmsg)
		case zmsg := <-server.zapiMsgCh:
			m := handleZapiMsg(zmsg, server)
			if len(m) > 0 {
				senderMsgs = append(senderMsgs, m...)
			}
		case conn := <-server.acceptCh:
			passConn(conn)
		case e, ok := <-server.fsmincomingCh.Out():
			if !ok {
				continue
			}
			handleFsmMsg(e.(*FsmMsg))
		case e := <-server.fsmStateCh:
			handleFsmMsg(e)
		case sCh <- firstMsg:
			senderMsgs = senderMsgs[1:]
		case bCh <- firstBroadcastMsg:
			server.broadcastMsgs = server.broadcastMsgs[1:]
		case msg := <-server.MsgCh:
			if len(msg) > 0 {
				senderMsgs = append(senderMsgs, msg...)
			}
		}
	}
}

func newSenderMsg(peer *Peer, paths []*table.Path, notification *bgp.BGPMessage, stayIdle bool) *SenderMsg {
	return &SenderMsg{
		ch: peer.outgoing,
		msg: &FsmOutgoingMsg{
			Paths:        paths,
			Notification: notification,
			StayIdle:     stayIdle,
		},
	}
}

func isASLoop(peer *Peer, path *table.Path) bool {
	for _, as := range path.GetAsList() {
		if as == peer.fsm.pConf.Config.PeerAs {
			return true
		}
	}
	return false
}

func filterpath(peer *Peer, path *table.Path) *table.Path {
	if path == nil {
		return nil
	}
	if _, ok := peer.fsm.rfMap[path.GetRouteFamily()]; !ok {
		return nil
	}

	//iBGP handling
	if peer.isIBGPPeer() {
		ignore := false
		//RFC4684 Constrained Route Distribution
		if peer.fsm.rfMap[bgp.RF_RTC_UC] && path.GetRouteFamily() != bgp.RF_RTC_UC {
			ignore = true
			for _, ext := range path.GetExtCommunities() {
				for _, path := range peer.adjRibIn.PathList([]bgp.RouteFamily{bgp.RF_RTC_UC}, true) {
					rt := path.GetNlri().(*bgp.RouteTargetMembershipNLRI).RouteTarget
					if ext.String() == rt.String() {
						ignore = false
						break
					}
				}
				if !ignore {
					break
				}
			}
		}

		if !path.IsLocal() {
			ignore = true
			info := path.GetSource()
			//if the path comes from eBGP peer
			if info.AS != peer.fsm.pConf.Config.PeerAs {
				ignore = false
			}
			// RFC4456 8. Avoiding Routing Information Loops
			// A router that recognizes the ORIGINATOR_ID attribute SHOULD
			// ignore a route received with its BGP Identifier as the ORIGINATOR_ID.
			if id := path.GetOriginatorID(); peer.fsm.gConf.Config.RouterId == id.String() {
				log.WithFields(log.Fields{
					"Topic":        "Peer",
					"Key":          peer.ID(),
					"OriginatorID": id,
					"Data":         path,
				}).Debug("Originator ID is mine, ignore")
				return nil
			}
			if info.RouteReflectorClient {
				ignore = false
			}
			if peer.isRouteReflectorClient() {
				// RFC4456 8. Avoiding Routing Information Loops
				// If the local CLUSTER_ID is found in the CLUSTER_LIST,
				// the advertisement received SHOULD be ignored.
				for _, clusterId := range path.GetClusterList() {
					if clusterId.Equal(peer.fsm.peerInfo.RouteReflectorClusterID) {
						log.WithFields(log.Fields{
							"Topic":     "Peer",
							"Key":       peer.ID(),
							"ClusterID": clusterId,
							"Data":      path,
						}).Debug("cluster list path attribute has local cluster id, ignore")
						return nil
					}
				}
				ignore = false
			}
		}

		if ignore {
			log.WithFields(log.Fields{
				"Topic": "Peer",
				"Key":   peer.ID(),
				"Data":  path,
			}).Debug("From same AS, ignore.")
			return nil
		}
	}

	if peer.ID() == path.GetSource().Address.String() {
		log.WithFields(log.Fields{
			"Topic": "Peer",
			"Key":   peer.ID(),
			"Data":  path,
		}).Debug("From me, ignore.")
		return nil
	}

	if !peer.isRouteServerClient() && isASLoop(peer, path) {
		return nil
	}
	return path
}

func (server *BgpServer) dropPeerAllRoutes(peer *Peer, families []bgp.RouteFamily) []*SenderMsg {
	ids := make([]string, 0, len(server.neighborMap))
	msgs := make([]*SenderMsg, 0, len(server.neighborMap))
	if peer.isRouteServerClient() {
		for _, targetPeer := range server.neighborMap {
			if !targetPeer.isRouteServerClient() || targetPeer == peer || targetPeer.fsm.state != bgp.BGP_FSM_ESTABLISHED {
				continue
			}
			ids = append(ids, targetPeer.TableID())
		}
	} else {
		ids = append(ids, table.GLOBAL_RIB_NAME)
	}
	for _, rf := range families {
		best, _ := server.globalRib.DeletePathsByPeer(ids, peer.fsm.peerInfo, rf)

		if !peer.isRouteServerClient() {
			server.broadcastBests(best[table.GLOBAL_RIB_NAME])
		}

		for _, targetPeer := range server.neighborMap {
			if peer.isRouteServerClient() != targetPeer.isRouteServerClient() || targetPeer == peer {
				continue
			}
			if paths := targetPeer.processOutgoingPaths(best[targetPeer.TableID()], nil); len(paths) > 0 {
				msgs = append(msgs, newSenderMsg(targetPeer, paths, nil, false))
			}
		}
	}
	return msgs
}

func (server *BgpServer) broadcastBests(bests []*table.Path) {
	for _, path := range bests {
		if path == nil {
			continue
		}
		if !path.IsFromExternal() {
			z := newBroadcastZapiBestMsg(server.zclient, path)
			if z != nil {
				server.broadcastMsgs = append(server.broadcastMsgs, z)
				log.WithFields(log.Fields{
					"Topic":   "Server",
					"Client":  z.client,
					"Message": z.msg,
				}).Debug("Default policy applied and rejected.")
			}
		}

		rf := path.GetRouteFamily()

		result := &Response{
			Data: &Destination{
				Prefix: path.GetNlri().String(),
				Paths:  []*Path{path.ToApiStruct(table.GLOBAL_RIB_NAME)},
			},
		}
		remainReqs := make([]*Request, 0, len(server.broadcastReqs))
		for _, req := range server.broadcastReqs {
			select {
			case <-req.EndCh:
				continue
			default:
			}
			if req.RequestType != REQ_MONITOR_GLOBAL_BEST_CHANGED {
				remainReqs = append(remainReqs, req)
				continue
			}
			if req.RouteFamily == bgp.RouteFamily(0) || req.RouteFamily == rf {
				m := &broadcastGrpcMsg{
					req:    req,
					result: result,
				}
				server.broadcastMsgs = append(server.broadcastMsgs, m)
			}
			remainReqs = append(remainReqs, req)
		}
		server.broadcastReqs = remainReqs
	}
}

func (server *BgpServer) broadcastPeerState(peer *Peer, oldState bgp.FSMState) {
	result := &Response{
		Data: peer,
	}
	remainReqs := make([]*Request, 0, len(server.broadcastReqs))
	for _, req := range server.broadcastReqs {
		select {
		case <-req.EndCh:
			continue
		default:
		}
		ignore := req.RequestType != REQ_MONITOR_NEIGHBOR_PEER_STATE
		ignore = ignore || (req.Name != "" && req.Name != peer.fsm.pConf.Config.NeighborAddress)
		if ignore {
			remainReqs = append(remainReqs, req)
			continue
		}
		m := &broadcastGrpcMsg{
			req:    req,
			result: result,
		}
		server.broadcastMsgs = append(server.broadcastMsgs, m)
		remainReqs = append(remainReqs, req)
	}
	server.broadcastReqs = remainReqs
	newState := peer.fsm.state
	if oldState == bgp.BGP_FSM_ESTABLISHED || newState == bgp.BGP_FSM_ESTABLISHED {
		if server.watchers.watching(WATCHER_EVENT_STATE_CHANGE) {
			_, rport := peer.fsm.RemoteHostPort()
			laddr, lport := peer.fsm.LocalHostPort()
			sentOpen := buildopen(peer.fsm.gConf, peer.fsm.pConf)
			recvOpen := peer.fsm.recvOpen
			ev := &watcherEventStateChangedMsg{
				peerAS:       peer.fsm.peerInfo.AS,
				localAS:      peer.fsm.peerInfo.LocalAS,
				peerAddress:  peer.fsm.peerInfo.Address,
				localAddress: net.ParseIP(laddr),
				peerPort:     rport,
				localPort:    lport,
				peerID:       peer.fsm.peerInfo.ID,
				sentOpen:     sentOpen,
				recvOpen:     recvOpen,
				state:        newState,
				timestamp:    time.Now(),
			}
			server.notify2watchers(WATCHER_EVENT_STATE_CHANGE, ev)
		}
	}
}

func (server *BgpServer) RSimportPaths(peer *Peer, pathList []*table.Path) []*table.Path {
	moded := make([]*table.Path, 0, len(pathList)/2)
	for _, before := range pathList {
		if isASLoop(peer, before) {
			before.Filter(peer.ID(), table.POLICY_DIRECTION_IMPORT)
			continue
		}
		after := server.policy.ApplyPolicy(peer.TableID(), table.POLICY_DIRECTION_IMPORT, before, nil)
		if after == nil {
			before.Filter(peer.ID(), table.POLICY_DIRECTION_IMPORT)
		} else if after != before {
			before.Filter(peer.ID(), table.POLICY_DIRECTION_IMPORT)
			for _, n := range server.neighborMap {
				if n == peer {
					continue
				}
				after.Filter(n.ID(), table.POLICY_DIRECTION_IMPORT)
			}
			moded = append(moded, after)
		}
	}
	return moded
}

func (server *BgpServer) propagateUpdate(peer *Peer, pathList []*table.Path) ([]*SenderMsg, []*table.Path) {
	rib := server.globalRib
	var alteredPathList, withdrawn []*table.Path
	var best map[string][]*table.Path
	msgs := make([]*SenderMsg, 0, len(server.neighborMap))

	if peer != nil && peer.isRouteServerClient() {
		for _, path := range pathList {
			path.Filter(peer.ID(), table.POLICY_DIRECTION_IMPORT)
			path.Filter(table.GLOBAL_RIB_NAME, table.POLICY_DIRECTION_IMPORT)
		}
		moded := make([]*table.Path, 0)
		for _, targetPeer := range server.neighborMap {
			if !targetPeer.isRouteServerClient() || peer == targetPeer {
				continue
			}
			moded = append(moded, server.RSimportPaths(targetPeer, pathList)...)
		}
		isTarget := func(p *Peer) bool {
			return p.isRouteServerClient() && p.fsm.state == bgp.BGP_FSM_ESTABLISHED && !p.fsm.pConf.GracefulRestart.State.LocalRestarting
		}

		ids := make([]string, 0, len(server.neighborMap))
		for _, targetPeer := range server.neighborMap {
			if isTarget(targetPeer) {
				ids = append(ids, targetPeer.TableID())
			}
		}
		best, withdrawn = rib.ProcessPaths(ids, append(pathList, moded...))
	} else {
		for idx, path := range pathList {
			path = server.policy.ApplyPolicy(table.GLOBAL_RIB_NAME, table.POLICY_DIRECTION_IMPORT, path, nil)
			pathList[idx] = path
			// RFC4684 Constrained Route Distribution 6. Operation
			//
			// When a BGP speaker receives a BGP UPDATE that advertises or withdraws
			// a given Route Target membership NLRI, it should examine the RIB-OUTs
			// of VPN NLRIs and re-evaluate the advertisement status of routes that
			// match the Route Target in question.
			//
			// A BGP speaker should generate the minimum set of BGP VPN route
			// updates (advertisements and/or withdrawls) necessary to transition
			// between the previous and current state of the route distribution
			// graph that is derived from Route Target membership information.
			if peer != nil && path != nil && path.GetRouteFamily() == bgp.RF_RTC_UC {
				rt := path.GetNlri().(*bgp.RouteTargetMembershipNLRI).RouteTarget
				fs := make([]bgp.RouteFamily, 0, len(peer.configuredRFlist()))
				for _, f := range peer.configuredRFlist() {
					if f != bgp.RF_RTC_UC {
						fs = append(fs, f)
					}
				}
				var candidates []*table.Path
				if path.IsWithdraw {
					candidates = peer.adjRibOut.PathList(fs, false)
				} else {
					candidates = rib.GetBestPathList(peer.TableID(), fs)
				}
				paths := make([]*table.Path, 0, len(pathList))
				for _, p := range candidates {
					t := false
					for _, ext := range p.GetExtCommunities() {
						if ext.String() == rt.String() {
							t = true
							break
						}
					}
					if t {
						paths = append(paths, p.Clone(path.IsWithdraw))
					}
				}
				msgs = append(msgs, newSenderMsg(peer, paths, nil, false))
			}
		}
		alteredPathList = pathList
		best, withdrawn = rib.ProcessPaths([]string{table.GLOBAL_RIB_NAME}, pathList)
		if len(best[table.GLOBAL_RIB_NAME]) == 0 {
			return nil, alteredPathList
		}
		server.broadcastBests(best[table.GLOBAL_RIB_NAME])
	}

	for _, targetPeer := range server.neighborMap {
		if (peer == nil && targetPeer.isRouteServerClient()) || (peer != nil && peer.isRouteServerClient() != targetPeer.isRouteServerClient()) {
			continue
		}
		if paths := targetPeer.processOutgoingPaths(best[targetPeer.TableID()], withdrawn); len(paths) > 0 {
			msgs = append(msgs, newSenderMsg(targetPeer, paths, nil, false))
		}
	}
	return msgs, alteredPathList
}

func (server *BgpServer) handleFSMMessage(peer *Peer, e *FsmMsg) []*SenderMsg {
	var msgs []*SenderMsg
	switch e.MsgType {
	case FSM_MSG_STATE_CHANGE:
		nextState := e.MsgData.(bgp.FSMState)
		oldState := bgp.FSMState(peer.fsm.pConf.State.SessionState.ToInt())
		peer.fsm.pConf.State.SessionState = config.IntToSessionStateMap[int(nextState)]
		peer.fsm.StateChange(nextState)

		if oldState == bgp.BGP_FSM_ESTABLISHED {
			t := time.Now()
			if t.Sub(time.Unix(peer.fsm.pConf.Timers.State.Uptime, 0)) < FLOP_THRESHOLD {
				peer.fsm.pConf.State.Flops++
			}
			var drop []bgp.RouteFamily
			if peer.fsm.reason == FSM_GRACEFUL_RESTART {
				peer.fsm.pConf.GracefulRestart.State.PeerRestarting = true
				var p []bgp.RouteFamily
				p, drop = peer.forwardingPreservedFamilies()
				peer.StaleAll(p)
			} else {
				drop = peer.configuredRFlist()
			}
			peer.prefixLimitWarned = make(map[bgp.RouteFamily]bool)
			peer.DropAll(drop)
			msgs = server.dropPeerAllRoutes(peer, drop)
		} else if peer.fsm.pConf.GracefulRestart.State.PeerRestarting && nextState == bgp.BGP_FSM_IDLE {
			// RFC 4724 4.2
			// If the session does not get re-established within the "Restart Time"
			// that the peer advertised previously, the Receiving Speaker MUST
			// delete all the stale routes from the peer that it is retaining.
			peer.fsm.pConf.GracefulRestart.State.PeerRestarting = false
			peer.DropAll(peer.configuredRFlist())
			msgs = server.dropPeerAllRoutes(peer, peer.configuredRFlist())
		}

		close(peer.outgoing)
		peer.outgoing = make(chan *FsmOutgoingMsg, 128)
		if nextState == bgp.BGP_FSM_ESTABLISHED {
			// update for export policy
			laddr, _ := peer.fsm.LocalHostPort()
			peer.fsm.pConf.Transport.State.LocalAddress = laddr
			deferralExpiredFunc := func(family bgp.RouteFamily) func() {
				return func() {
					req := NewRequest(REQ_DEFERRAL_TIMER_EXPIRED, peer.ID(), family, nil)
					server.GrpcReqCh <- req
					<-req.ResponseCh
				}
			}
			if !peer.fsm.pConf.GracefulRestart.State.LocalRestarting {
				// When graceful-restart cap (which means intention
				// of sending EOR) and route-target address family are negotiated,
				// send route-target NLRIs first, and wait to send others
				// till receiving EOR of route-target address family.
				// This prevents sending uninterested routes to peers.
				//
				// However, when the peer is graceful restarting, give up
				// waiting sending non-route-target NLRIs since the peer won't send
				// any routes (and EORs) before we send ours (or deferral-timer expires).
				var pathList []*table.Path
				if c := config.GetAfiSafi(peer.fsm.pConf, bgp.RF_RTC_UC); !peer.fsm.pConf.GracefulRestart.State.PeerRestarting && peer.fsm.rfMap[bgp.RF_RTC_UC] && c.RouteTargetMembership.Config.DeferralTime > 0 {
					pathList, _ = peer.getBestFromLocal([]bgp.RouteFamily{bgp.RF_RTC_UC})
					t := c.RouteTargetMembership.Config.DeferralTime
					for _, f := range peer.configuredRFlist() {
						if f != bgp.RF_RTC_UC {
							time.AfterFunc(time.Second*time.Duration(t), deferralExpiredFunc(f))
						}
					}
				} else {
					pathList, _ = peer.getBestFromLocal(peer.configuredRFlist())
				}

				if len(pathList) > 0 {
					peer.adjRibOut.Update(pathList)
					msgs = []*SenderMsg{newSenderMsg(peer, pathList, nil, false)}
				}
			} else {
				// RFC 4724 4.1
				// Once the session between the Restarting Speaker and the Receiving
				// Speaker is re-established, the Restarting Speaker will receive and
				// process BGP messages from its peers.  However, it MUST defer route
				// selection for an address family until it either (a) ...snip...
				// or (b) the Selection_Deferral_Timer referred to below has expired.
				deferral := peer.fsm.pConf.GracefulRestart.Config.DeferralTime
				log.WithFields(log.Fields{
					"Topic": "Peer",
					"Key":   peer.ID(),
				}).Debugf("now syncing, suppress sending updates. start deferral timer(%d)", deferral)
				time.AfterFunc(time.Second*time.Duration(deferral), deferralExpiredFunc(bgp.RouteFamily(0)))
			}
		} else {
			if server.shutdown && nextState == bgp.BGP_FSM_IDLE {
				die := true
				for _, p := range server.neighborMap {
					if p.fsm.state != bgp.BGP_FSM_IDLE {
						die = false
						break
					}
				}
				if die {
					os.Exit(0)
				}
			}
			peer.fsm.pConf.Timers.State.Downtime = time.Now().Unix()
		}
		// clear counter
		if peer.fsm.adminState == ADMIN_STATE_DOWN {
			peer.fsm.pConf.State = config.NeighborState{}
			peer.fsm.pConf.Timers.State = config.TimersState{}
		}
		peer.startFSMHandler(server.fsmincomingCh, server.fsmStateCh)
		server.broadcastPeerState(peer, oldState)
	case FSM_MSG_ROUTE_REFRESH:
		if paths := peer.handleRouteRefresh(e); len(paths) > 0 {
			return []*SenderMsg{newSenderMsg(peer, paths, nil, false)}
		}
	case FSM_MSG_BGP_MESSAGE:
		switch m := e.MsgData.(type) {
		case *bgp.MessageError:
			return []*SenderMsg{newSenderMsg(peer, nil, bgp.NewBGPNotificationMessage(m.TypeCode, m.SubTypeCode, m.Data), false)}
		case *bgp.BGPMessage:
			server.roaManager.validate(e.PathList)
			pathList, eor, notification := peer.handleUpdate(e)
			if notification != nil {
				return []*SenderMsg{newSenderMsg(peer, nil, notification, true)}
			}
			if m.Header.Type == bgp.BGP_MSG_UPDATE && server.watchers.watching(WATCHER_EVENT_UPDATE_MSG) {
				_, y := peer.fsm.capMap[bgp.BGP_CAP_FOUR_OCTET_AS_NUMBER]
				l, _ := peer.fsm.LocalHostPort()
				ev := &watcherEventUpdateMsg{
					message:      m,
					peerAS:       peer.fsm.peerInfo.AS,
					localAS:      peer.fsm.peerInfo.LocalAS,
					peerAddress:  peer.fsm.peerInfo.Address,
					localAddress: net.ParseIP(l),
					peerID:       peer.fsm.peerInfo.ID,
					fourBytesAs:  y,
					timestamp:    e.timestamp,
					payload:      e.payload,
					postPolicy:   false,
					pathList:     pathList,
				}
				server.notify2watchers(WATCHER_EVENT_UPDATE_MSG, ev)
			}

			if len(pathList) > 0 {
				var altered []*table.Path
				msgs, altered = server.propagateUpdate(peer, pathList)
				if server.watchers.watching(WATCHER_EVENT_POST_POLICY_UPDATE_MSG) {
					_, y := peer.fsm.capMap[bgp.BGP_CAP_FOUR_OCTET_AS_NUMBER]
					l, _ := peer.fsm.LocalHostPort()
					ev := &watcherEventUpdateMsg{
						peerAS:       peer.fsm.peerInfo.AS,
						localAS:      peer.fsm.peerInfo.LocalAS,
						peerAddress:  peer.fsm.peerInfo.Address,
						localAddress: net.ParseIP(l),
						peerID:       peer.fsm.peerInfo.ID,
						fourBytesAs:  y,
						timestamp:    e.timestamp,
						postPolicy:   true,
						pathList:     altered,
					}
					for _, u := range table.CreateUpdateMsgFromPaths(altered) {
						payload, _ := u.Serialize()
						ev.payload = payload
						server.notify2watchers(WATCHER_EVENT_POST_POLICY_UPDATE_MSG, ev)
					}
				}
			}

			if len(eor) > 0 {
				rtc := false
				for _, f := range eor {
					if f == bgp.RF_RTC_UC {
						rtc = true
					}
					for i, a := range peer.fsm.pConf.AfiSafis {
						if g, _ := bgp.GetRouteFamily(string(a.Config.AfiSafiName)); f == g {
							peer.fsm.pConf.AfiSafis[i].MpGracefulRestart.State.EndOfRibReceived = true
						}
					}
				}

				// RFC 4724 4.1
				// Once the session between the Restarting Speaker and the Receiving
				// Speaker is re-established, ...snip... it MUST defer route
				// selection for an address family until it either (a) receives the
				// End-of-RIB marker from all its peers (excluding the ones with the
				// "Restart State" bit set in the received capability and excluding the
				// ones that do not advertise the graceful restart capability) or ...snip...
				if peer.fsm.pConf.GracefulRestart.State.LocalRestarting {
					allEnd := func() bool {
						for _, p := range server.neighborMap {
							if !p.recvedAllEOR() {
								return false
							}
						}
						return true
					}()
					if allEnd {
						for _, p := range server.neighborMap {
							p.fsm.pConf.GracefulRestart.State.LocalRestarting = false
							if !p.isGracefulRestartEnabled() {
								continue
							}
							paths, _ := p.getBestFromLocal(p.configuredRFlist())
							if len(paths) > 0 {
								p.adjRibOut.Update(paths)
								msgs = append(msgs, newSenderMsg(p, paths, nil, false))
							}
						}
						log.WithFields(log.Fields{
							"Topic": "Server",
						}).Info("sync finished")

					}

					// we don't delay non-route-target NLRIs when local-restarting
					rtc = false
				}
				if peer.fsm.pConf.GracefulRestart.State.PeerRestarting {
					if peer.recvedAllEOR() {
						peer.fsm.pConf.GracefulRestart.State.PeerRestarting = false
						pathList := peer.adjRibIn.DropStale(peer.configuredRFlist())
						log.WithFields(log.Fields{
							"Topic": "Peer",
							"Key":   peer.fsm.pConf.Config.NeighborAddress,
						}).Debugf("withdraw %d stale routes", len(pathList))
						m, _ := server.propagateUpdate(peer, pathList)
						msgs = append(msgs, m...)
					}

					// we don't delay non-route-target NLRIs when peer is restarting
					rtc = false
				}

				// received EOR of route-target address family
				// outbound filter is now ready, let's flash non-route-target NLRIs
				if c := config.GetAfiSafi(peer.fsm.pConf, bgp.RF_RTC_UC); rtc && c != nil && c.RouteTargetMembership.Config.DeferralTime > 0 {
					log.WithFields(log.Fields{
						"Topic": "Peer",
						"Key":   peer.ID(),
					}).Debug("received route-target eor. flash non-route-target NLRIs")
					families := make([]bgp.RouteFamily, 0, len(peer.configuredRFlist()))
					for _, f := range peer.configuredRFlist() {
						if f != bgp.RF_RTC_UC {
							families = append(families, f)
						}
					}
					if paths, _ := peer.getBestFromLocal(families); len(paths) > 0 {
						peer.adjRibOut.Update(paths)
						msgs = append(msgs, newSenderMsg(peer, paths, nil, false))
					}
				}
			}
		default:
			log.WithFields(log.Fields{
				"Topic": "Peer",
				"Key":   peer.fsm.pConf.Config.NeighborAddress,
				"Data":  e.MsgData,
			}).Panic("unknown msg type")
		}
	}
	return msgs
}

func (server *BgpServer) SetGlobalType(g config.Global) error {
	ch := make(chan *Response)
	server.GrpcReqCh <- &Request{
		RequestType: REQ_START_SERVER,
		Data:        &g,
		ResponseCh:  ch,
	}
	if err := (<-ch).Err(); err != nil {
		return err
	}
	return nil
}

func (server *BgpServer) SetZebraConfig(z config.Zebra) error {
	if !z.Config.Enabled {
		return nil
	}
	ch := make(chan *Response)
	server.GrpcReqCh <- &Request{
		RequestType: REQ_INITIALIZE_ZEBRA,
		Data:        &z.Config,
		ResponseCh:  ch,
	}
	if err := (<-ch).Err(); err != nil {
		return err
	}
	return nil
}

func (server *BgpServer) SetRpkiConfig(c []config.RpkiServer) error {
	ch := make(chan *Response)
	server.GrpcReqCh <- &Request{
		RequestType: REQ_INITIALIZE_RPKI,
		Data:        &server.bgpConfig.Global,
		ResponseCh:  ch,
	}
	if err := (<-ch).Err(); err != nil {
		return err
	}

	for _, s := range c {
		ch := make(chan *Response)
		server.GrpcReqCh <- &Request{
			RequestType: REQ_ADD_RPKI,
			Data: &AddRpkiRequest{
				Address:  s.Config.Address,
				Port:     s.Config.Port,
				Lifetime: s.Config.RecordLifetime,
			},
			ResponseCh: ch,
		}
		if err := (<-ch).Err(); err != nil {
			return err
		}
	}
	return nil
}

func (server *BgpServer) SetBmpConfig(c []config.BmpServer) error {
	for _, s := range c {
		ch := make(chan *Response)
		server.GrpcReqCh <- &Request{
			RequestType: REQ_ADD_BMP,
			Data:        &s.Config,
			ResponseCh:  ch,
		}
		if err := (<-ch).Err(); err != nil {
			return err
		}
	}
	return nil
}

func (server *BgpServer) SetMrtConfig(c []config.Mrt) error {
	for _, s := range c {
		if s.FileName != "" {
			ch := make(chan *Response)
			server.GrpcReqCh <- &Request{
				RequestType: REQ_ENABLE_MRT,
				Data: &EnableMrtRequest{
					DumpType: int32(s.DumpType.ToInt()),
					Filename: s.FileName,
					Interval: s.Interval,
				},
				ResponseCh: ch,
			}
			if err := (<-ch).Err(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (server *BgpServer) PeerAdd(peer config.Neighbor) error {
	ch := make(chan *Response)
	server.GrpcReqCh <- &Request{
		RequestType: REQ_ADD_NEIGHBOR,
		Data:        &peer,
		ResponseCh:  ch,
	}
	return (<-ch).Err()
}

func (server *BgpServer) PeerDelete(peer config.Neighbor) error {
	ch := make(chan *Response)
	server.GrpcReqCh <- &Request{
		RequestType: REQ_DEL_NEIGHBOR,
		Data:        &peer,
		ResponseCh:  ch,
	}
	return (<-ch).Err()
}

func (server *BgpServer) PeerUpdate(peer config.Neighbor) (bool, error) {
	ch := make(chan *Response)
	server.GrpcReqCh <- &Request{
		RequestType: REQ_UPDATE_NEIGHBOR,
		Data:        &peer,
		ResponseCh:  ch,
	}
	res := <-ch
	return res.Data.(bool), res.Err()
}

func (server *BgpServer) Shutdown() {
	server.shutdown = true
	for _, p := range server.neighborMap {
		p.fsm.adminStateCh <- ADMIN_STATE_DOWN
	}
	// TODO: call fsmincomingCh.Close()
}

func (server *BgpServer) UpdatePolicy(policy config.RoutingPolicy) {
	ch := make(chan *Response)
	server.GrpcReqCh <- &Request{
		RequestType: REQ_RELOAD_POLICY,
		Data:        policy,
		ResponseCh:  ch,
	}
	<-ch
}

func (server *BgpServer) setPolicyByConfig(id string, c config.ApplyPolicy) {
	for _, dir := range []table.PolicyDirection{table.POLICY_DIRECTION_IN, table.POLICY_DIRECTION_IMPORT, table.POLICY_DIRECTION_EXPORT} {
		ps, def, err := server.policy.GetAssignmentFromConfig(dir, c)
		if err != nil {
			log.WithFields(log.Fields{
				"Topic": "Policy",
				"Dir":   dir,
			}).Errorf("failed to get policy info: %s", err)
			continue
		}
		server.policy.SetDefaultPolicy(id, dir, def)
		server.policy.SetPolicy(id, dir, ps)
	}
}

func (server *BgpServer) SetRoutingPolicy(pl config.RoutingPolicy) error {
	if err := server.policy.Reload(pl); err != nil {
		log.WithFields(log.Fields{
			"Topic": "Policy",
		}).Errorf("failed to create routing policy: %s", err)
		return err
	}
	server.setPolicyByConfig(table.GLOBAL_RIB_NAME, server.bgpConfig.Global.ApplyPolicy)
	return nil
}

func (server *BgpServer) handlePolicy(pl config.RoutingPolicy) error {
	if err := server.SetRoutingPolicy(pl); err != nil {
		log.WithFields(log.Fields{
			"Topic": "Policy",
		}).Errorf("failed to set new policy: %s", err)
		return err
	}
	for _, peer := range server.neighborMap {
		log.WithFields(log.Fields{
			"Topic": "Peer",
			"Key":   peer.fsm.pConf.Config.NeighborAddress,
		}).Info("call set policy")
		server.setPolicyByConfig(peer.ID(), peer.fsm.pConf.ApplyPolicy)
	}
	return nil
}

func (server *BgpServer) checkNeighborRequest(req *Request) (*Peer, error) {
	remoteAddr := req.Name
	peer, found := server.neighborMap[remoteAddr]
	if !found {
		result := &Response{}
		result.ResponseErr = fmt.Errorf("Neighbor that has %v doesn't exist.", remoteAddr)
		req.ResponseCh <- result
		close(req.ResponseCh)
		return nil, result.ResponseErr
	}
	return peer, nil
}

// EVPN MAC MOBILITY HANDLING
//
// We don't have multihoming function now, so ignore
// ESI comparison.
//
// RFC7432 15. MAC Mobility
//
// A PE detecting a locally attached MAC address for which it had
// previously received a MAC/IP Advertisement route with the same zero
// Ethernet segment identifier (single-homed scenarios) advertises it
// with a MAC Mobility extended community attribute with the sequence
// number set properly.  In the case of single-homed scenarios, there
// is no need for ESI comparison.

func getMacMobilityExtendedCommunity(etag uint32, mac net.HardwareAddr, evpnPaths []*table.Path) *bgp.MacMobilityExtended {
	seqs := make([]struct {
		seq     int
		isLocal bool
	}, 0)

	for _, path := range evpnPaths {
		nlri := path.GetNlri().(*bgp.EVPNNLRI)
		target, ok := nlri.RouteTypeData.(*bgp.EVPNMacIPAdvertisementRoute)
		if !ok {
			continue
		}
		if target.ETag == etag && bytes.Equal(target.MacAddress, mac) {
			found := false
			for _, ec := range path.GetExtCommunities() {
				if t, st := ec.GetTypes(); t == bgp.EC_TYPE_EVPN && st == bgp.EC_SUBTYPE_MAC_MOBILITY {
					seqs = append(seqs, struct {
						seq     int
						isLocal bool
					}{int(ec.(*bgp.MacMobilityExtended).Sequence), path.IsLocal()})
					found = true
					break
				}
			}

			if !found {
				seqs = append(seqs, struct {
					seq     int
					isLocal bool
				}{-1, path.IsLocal()})
			}
		}
	}

	if len(seqs) > 0 {
		newSeq := -2
		var isLocal bool
		for _, seq := range seqs {
			if seq.seq > newSeq {
				newSeq = seq.seq
				isLocal = seq.isLocal
			}
		}

		if !isLocal {
			newSeq += 1
		}

		if newSeq != -1 {
			return &bgp.MacMobilityExtended{
				Sequence: uint32(newSeq),
			}
		}
	}
	return nil
}

func (server *BgpServer) Api2PathList(resource Resource, name string, ApiPathList []*Path) ([]*table.Path, error) {
	var nlri bgp.AddrPrefixInterface
	var nexthop string
	var pi *table.PeerInfo

	paths := make([]*table.Path, 0, len(ApiPathList))

	for _, path := range ApiPathList {
		seen := make(map[bgp.BGPAttrType]bool)

		pattr := make([]bgp.PathAttributeInterface, 0)
		extcomms := make([]bgp.ExtendedCommunityInterface, 0)

		if path.SourceAsn != 0 {
			pi = &table.PeerInfo{
				AS:      path.SourceAsn,
				LocalID: net.ParseIP(path.SourceId),
			}
		} else {
			pi = &table.PeerInfo{
				AS:      server.bgpConfig.Global.Config.As,
				LocalID: net.ParseIP(server.bgpConfig.Global.Config.RouterId).To4(),
			}
		}

		if len(path.Nlri) > 0 {
			nlri = &bgp.IPAddrPrefix{}
			err := nlri.DecodeFromBytes(path.Nlri)
			if err != nil {
				return nil, err
			}
		}

		for _, attr := range path.Pattrs {
			p, err := bgp.GetPathAttribute(attr)
			if err != nil {
				return nil, err
			}

			err = p.DecodeFromBytes(attr)
			if err != nil {
				return nil, err
			}

			if _, ok := seen[p.GetType()]; !ok {
				seen[p.GetType()] = true
			} else {
				return nil, fmt.Errorf("the path attribute apears twice. Type : " + strconv.Itoa(int(p.GetType())))
			}
			switch p.GetType() {
			case bgp.BGP_ATTR_TYPE_NEXT_HOP:
				nexthop = p.(*bgp.PathAttributeNextHop).Value.String()
			case bgp.BGP_ATTR_TYPE_EXTENDED_COMMUNITIES:
				value := p.(*bgp.PathAttributeExtendedCommunities).Value
				if len(value) > 0 {
					extcomms = append(extcomms, value...)
				}
			case bgp.BGP_ATTR_TYPE_MP_REACH_NLRI:
				mpreach := p.(*bgp.PathAttributeMpReachNLRI)
				if len(mpreach.Value) != 1 {
					return nil, fmt.Errorf("include only one route in mp_reach_nlri")
				}
				nlri = mpreach.Value[0]
				nexthop = mpreach.Nexthop.String()
			default:
				pattr = append(pattr, p)
			}
		}

		if nlri == nil || nexthop == "" {
			return nil, fmt.Errorf("not found nlri or nexthop")
		}

		rf := bgp.AfiSafiToRouteFamily(nlri.AFI(), nlri.SAFI())

		if resource == Resource_VRF {
			label, err := server.globalRib.GetNextLabel(name, nexthop, path.IsWithdraw)
			if err != nil {
				return nil, err
			}
			vrf := server.globalRib.Vrfs[name]
			switch rf {
			case bgp.RF_IPv4_UC:
				n := nlri.(*bgp.IPAddrPrefix)
				nlri = bgp.NewLabeledVPNIPAddrPrefix(n.Length, n.Prefix.String(), *bgp.NewMPLSLabelStack(label), vrf.Rd)
			case bgp.RF_IPv6_UC:
				n := nlri.(*bgp.IPv6AddrPrefix)
				nlri = bgp.NewLabeledVPNIPv6AddrPrefix(n.Length, n.Prefix.String(), *bgp.NewMPLSLabelStack(label), vrf.Rd)
			case bgp.RF_EVPN:
				n := nlri.(*bgp.EVPNNLRI)
				switch n.RouteType {
				case bgp.EVPN_ROUTE_TYPE_MAC_IP_ADVERTISEMENT:
					n.RouteTypeData.(*bgp.EVPNMacIPAdvertisementRoute).RD = vrf.Rd
				case bgp.EVPN_INCLUSIVE_MULTICAST_ETHERNET_TAG:
					n.RouteTypeData.(*bgp.EVPNMulticastEthernetTagRoute).RD = vrf.Rd
				}
			default:
				return nil, fmt.Errorf("unsupported route family for vrf: %s", rf)
			}
			extcomms = append(extcomms, vrf.ExportRt...)
		}

		if resource != Resource_VRF && rf == bgp.RF_IPv4_UC {
			pattr = append(pattr, bgp.NewPathAttributeNextHop(nexthop))
		} else {
			pattr = append(pattr, bgp.NewPathAttributeMpReachNLRI(nexthop, []bgp.AddrPrefixInterface{nlri}))
		}

		if rf == bgp.RF_EVPN {
			evpnNlri := nlri.(*bgp.EVPNNLRI)
			if evpnNlri.RouteType == bgp.EVPN_ROUTE_TYPE_MAC_IP_ADVERTISEMENT {
				macIpAdv := evpnNlri.RouteTypeData.(*bgp.EVPNMacIPAdvertisementRoute)
				etag := macIpAdv.ETag
				mac := macIpAdv.MacAddress
				paths := server.globalRib.GetBestPathList(table.GLOBAL_RIB_NAME, []bgp.RouteFamily{bgp.RF_EVPN})
				if m := getMacMobilityExtendedCommunity(etag, mac, paths); m != nil {
					extcomms = append(extcomms, m)
				}
			}
		}

		if len(extcomms) > 0 {
			pattr = append(pattr, bgp.NewPathAttributeExtendedCommunities(extcomms))
		}
		newPath := table.NewPath(pi, nlri, path.IsWithdraw, pattr, time.Now(), path.NoImplicitWithdraw)
		newPath.SetIsFromExternal(path.IsFromExternal)
		paths = append(paths, newPath)

	}
	return paths, nil
}

func (server *BgpServer) AddPathRequest(req *Request) *Response {
	var err error
	var uuidBytes []byte
	paths := make([]*table.Path, 0, 1)
	arg, ok := req.Data.(*AddPathRequest)
	if !ok {
		err = fmt.Errorf("type assertion failed")
	} else {
		paths, err = server.Api2PathList(arg.Resource, arg.VrfId, []*Path{arg.Path})
		if err == nil {
			u := uuid.NewV4()
			uuidBytes = u.Bytes()
			paths[0].SetUUID(uuidBytes)
		}
	}
	if len(paths) > 0 {
		msgs, _ = server.propagateUpdate(nil, pathList)
	}
	return &Response{
		ResponseErr: err,
		Data: &AddPathResponse{
			Uuid: uuidBytes,
		},
	}, nil
}

func (server *BgpServer) handleDeletePathRequest(req *Request) *Response {
	var err error
	paths := make([]*table.Path, 0, 1)
	arg, ok := req.Data.(*DeletePathRequest)
	if !ok {
		err = fmt.Errorf("type assertion failed")
	} else {
		if len(arg.Uuid) > 0 {
			path := func() *table.Path {
				for _, path := range server.globalRib.GetPathList(table.GLOBAL_RIB_NAME, server.globalRib.GetRFlist()) {
					if len(path.UUID()) > 0 && bytes.Equal(path.UUID(), arg.Uuid) {
						return path
					}
				}
				return nil
			}()
			if path != nil {
				paths = append(paths, path.Clone(true))
			} else {
				err = fmt.Errorf("Can't find a specified path")
			}
		} else if arg.Path != nil {
			arg.Path.IsWithdraw = true
			paths, err = server.Api2PathList(arg.Resource, arg.VrfId, []*Path{arg.Path})
		} else {
			// delete all paths
			families := server.globalRib.GetRFlist()
			if arg.Family != 0 {
				families = []bgp.RouteFamily{bgp.RouteFamily(arg.Family)}
			}
			for _, path := range server.globalRib.GetPathList(table.GLOBAL_RIB_NAME, families) {
				paths = append(paths, path.Clone(true))
			}
		}
	}
	if len(pathList) > 0 {
		msgs, _ = server.propagateUpdate(nil, pathList)
	}

	return &Response{
		ResponseErr: err,
		Data:        &DeletePathResponse{},
	}, nil
}

func (server *BgpServer) InjectMrtRequest(req *Request) *Response {
	var err error
	var paths []*table.Path
	result := &Response{}
	arg, ok := req.Data.(*InjectMrtRequest)
	if !ok {
		err = fmt.Errorf("type assertion failed")
	}
	if err == nil {
		paths, err = server.Api2PathList(arg.Resource, arg.VrfId, arg.Paths)
		if err == nil {
			if len(pathList) > 0 {
				msgs, _ = server.propagateUpdate(nil, pathList)
			}
		}
	} else {
		result = &Response{
			ResponseErr: err,
		}
	}
	return result
}

func (server *BgpServer) handleAddVrfRequest(req *Request) ([]*table.Path, error) {
	arg, _ := req.Data.(*AddVrfRequest)
	rib := server.globalRib
	pi := &table.PeerInfo{
		AS:      server.bgpConfig.Global.Config.As,
		LocalID: net.ParseIP(server.bgpConfig.Global.Config.RouterId).To4(),
	}
	return rib.AddVrf(arg.Vrf.Name, arg.Vrf.Rd, arg.Vrf.ImportRt, arg.Vrf.ExportRt, pi)
}

func (server *BgpServer) handleDeleteVrfRequest(req *Request) ([]*table.Path, error) {
	arg, _ := req.Data.(*DeleteVrfRequest)
	rib := server.globalRib
	return rib.DeleteVrf(arg.Vrf.Name)
}
func (server *BgpServer) GetVrfRib(req *Request) []*table.Path {
	arg := req.Data.(*GetRibRequest)
	name := arg.Table.Name
	rib := server.globalRib
	vrfs := rib.Vrfs
	if _, ok := vrfs[name]; !ok {
		result.ResponseErr = fmt.Errorf("vrf %s not found", name)
		break
	}
	var rf bgp.RouteFamily
	switch bgp.RouteFamily(arg.Table.Family) {
	case bgp.RF_IPv4_UC:
		rf = bgp.RF_IPv4_VPN
	case bgp.RF_IPv6_UC:
		rf = bgp.RF_IPv6_VPN
	case bgp.RF_EVPN:
		rf = bgp.RF_EVPN
	default:
		result.ResponseErr = fmt.Errorf("unsupported route family: %s", bgp.RouteFamily(arg.Table.Family))
		break
	}
	paths := rib.GetPathList(table.GLOBAL_RIB_NAME, []bgp.RouteFamily{rf})
	dsts := make([]*Destination, 0, len(paths))
	for _, path := range paths {
		ok := table.CanImportToVrf(vrfs[name], path)
		if !ok {
			continue
		}
		dsts = append(dsts, &Destination{
			Prefix: path.GetNlri().String(),
			Paths:  []*Path{path.ToApiStruct(table.GLOBAL_RIB_NAME)},
		})
	}
	return &Response{
		Data: &GetRibResponse{
			Table: &Table{
				Type:         arg.Table.Type,
				Family:       arg.Table.Family,
				Destinations: dsts,
			},
		},
	}
}
func (server *BgpServer) GetVrf(req *Request) *Response {
	return &Response{Data: &GetVrfResponse{Vrfs: server.globalRib.Vrfs}}
}
func (server *BgpServer) AddVrf(req *Request) *Response {
	msgs, result.ResponseErr = server.handleAddVrfRequest(req)
	return &Response{Data: &AddVrfResponse{}}
}
func (server *BgpServer) DeleteVrf(req *Request) *Response {
	msgs, result.ResponseErr = server.handleDeleteVrfRequest(req)
	return &Response{Data: &DeleteVrfResponse{}}
}
func (server *BgpServer) handleModConfig(req *Request) error {
	var c *config.Global
	switch arg := req.Data.(type) {
	case *StartServerRequest:
		g := arg.Global
		if net.ParseIP(g.RouterId) == nil {
			return fmt.Errorf("invalid router-id format: %s", g.RouterId)
		}
		families := make([]config.AfiSafi, 0, len(g.Families))
		for _, f := range g.Families {
			name := config.AfiSafiType(bgp.RouteFamily(f).String())
			families = append(families, config.AfiSafi{
				Config: config.AfiSafiConfig{
					AfiSafiName: name,
					Enabled:     true,
				},
				State: config.AfiSafiState{
					AfiSafiName: name,
				},
			})
		}
		b := &config.BgpConfigSet{
			Global: config.Global{
				Config: config.GlobalConfig{
					As:               g.As,
					RouterId:         g.RouterId,
					Port:             g.ListenPort,
					LocalAddressList: g.ListenAddresses,
				},
				MplsLabelRange: config.MplsLabelRange{
					MinLabel: g.MplsLabelMin,
					MaxLabel: g.MplsLabelMax,
				},
				AfiSafis: families,
			},
		}
		if err := config.SetDefaultConfigValues(nil, b); err != nil {
			return err
		}
		c = &b.Global
	case *config.Global:
		c = arg
	case *StopServerRequest:
		for k, _ := range server.neighborMap {
			_, err := server.handleDeleteNeighborRequest(&Request{
				Data: &DeleteNeighborRequest{
					Peer: &api.Peer{
						Conf: &api.PeerConf{
							NeighborAddress: k,
						},
					},
				},
			})
			if err != nil {
				return err
			}
		}
		for _, l := range server.listeners {
			l.Close()
		}
		server.bgpConfig.Global = config.Global{}
		return nil
	}

	if server.bgpConfig.Global.Config.As != 0 {
		return fmt.Errorf("gobgp is already started")
	}

	if c.Config.Port > 0 {
		acceptCh := make(chan *net.TCPConn, 4096)
		for _, addr := range c.Config.LocalAddressList {
			l, err := NewTCPListener(addr, uint32(c.Config.Port), acceptCh)
			if err != nil {
				return err
			}
			server.listeners = append(server.listeners, l)
		}
		server.acceptCh = acceptCh
	}

	rfs, _ := config.AfiSafis(c.AfiSafis).ToRfList()
	server.globalRib = table.NewTableManager(rfs, c.MplsLabelRange.MinLabel, c.MplsLabelRange.MaxLabel)

	p := config.RoutingPolicy{}
	if err := server.SetRoutingPolicy(p); err != nil {
		return err
	}
	server.bgpConfig.Global = *c
	// update route selection options
	table.SelectionOptions = c.RouteSelectionOptions.Config
	return nil
}

func sendMultipleResponses(req *Request, results []*Response) {
	defer close(req.ResponseCh)
	for _, r := range results {
		select {
		case req.ResponseCh <- r:
		case <-req.EndCh:
			return
		}
	}
}

func (server *BgpServer) GetNeighbor(req *Request) *Response {
	return &Response{
		Data: &GetNeighborResponse{
			Peers: server.neighborMap,
		},
	}
}

func (server *BgpServer) GetRib(req *Request) *Response {
	arg := grpcReq.Data.(*GetRibRequest)
	if arg.Table.Type != Resource_LOCAL && arg.Table.Type != Resource_GLOBAL {
		return nil, fmt.Errorf("unsupported resource type: %v", arg.Table.Type)
	}
	d := &Table{
		Type:   arg.Table.Type,
		Family: arg.Table.Family,
	}
	rib := server.globalRib
	id := table.GLOBAL_RIB_NAME
	if arg.Table.Type == Resource_LOCAL {
		peer, ok := server.neighborMap[arg.Table.Name]
		if !ok {
			err = fmt.Errorf("Neighbor that has %v doesn't exist.", arg.Table.Name)
			goto ERROR
		}
		if !peer.isRouteServerClient() {
			err = fmt.Errorf("Neighbor %v doesn't have local rib", arg.Table.Name)
			goto ERROR
		}
		id = peer.ID()
	}
	af := bgp.RouteFamily(arg.Table.Family)
	if _, ok := rib.Tables[af]; !ok {
		err = fmt.Errorf("address family: %s not supported", af)
		goto ERROR
	}

	dsts := make([]*Destination, 0, len(rib.Tables[af].GetDestinations()))
	if (af == bgp.RF_IPv4_UC || af == bgp.RF_IPv6_UC) && len(arg.Table.Destinations) > 0 {
		f := func(id, cidr string) (bool, error) {
			_, prefix, err := net.ParseCIDR(cidr)
			if err != nil {
				return false, err
			}
			if dst := rib.Tables[af].GetDestination(prefix.String()); dst != nil {
				if d := dst.ToApiStruct(id); d != nil {
					dsts = append(dsts, d)
				}
				return true, nil
			} else {
				return false, nil
			}
		}
		for _, dst := range arg.Table.Destinations {
			key := dst.Prefix
			if _, err := f(id, key); err != nil {
				if host := net.ParseIP(key); host != nil {
					masklen := 32
					if af == bgp.RF_IPv6_UC {
						masklen = 128
					}
					for i := masklen; i > 0; i-- {
						if y, _ := f(id, fmt.Sprintf("%s/%d", key, i)); y {
							break
						}
					}
				}
			} else if dst.LongerPrefixes {
				_, prefix, _ := net.ParseCIDR(key)
				ones, bits := prefix.Mask.Size()
				for i := ones + 1; i <= bits; i++ {
					prefix.Mask = net.CIDRMask(i, bits)
					f(id, prefix.String())
				}
			}
		}
	} else {
		for _, dst := range rib.Tables[af].GetSortedDestinations() {
			if d := dst.ToApiStruct(id); d != nil {
				dsts = append(dsts, d)
			}
		}
	}
	d.Destinations = dsts
	return &GrpcResponse{
		Data: &GetRibResponse{Table: d},
	}
}
func (server *BgpServer) reqToPeers(req *Request) ([]*Peer, error) {
	peers := make([]*Peer, 0)
	if req.Name == "all" {
		for _, p := range server.neighborMap {
			peers = append(peers, p)
		}
		return peers, nil
	}
	peer, err := server.checkNeighborRequest(req)
	return []*Peer{peer}, err
}
func (server *BgpServer) ResetNeighbor(req *Request) *Response {
	peers, err := reqToPeers(req)
	if err != nil {
		break
	}
	logOp(grpcReq.Name, "Neighbor reset")
	m := bgp.NewBGPNotificationMessage(bgp.BGP_ERROR_CEASE, bgp.BGP_ERROR_SUB_ADMINISTRATIVE_RESET, nil)
	for _, peer := range peers {
		peer.fsm.idleHoldTime = peer.fsm.pConf.Timers.Config.IdleHoldTimeAfterReset
		msgs = append(msgs, newSenderMsg(peer, nil, m, false))
	}
	return &GrpcResponse{Data: &api.ResetNeighborResponse{}}
}
func (server *BgpServer) SoftResetNeighborIN(req *Request) *Response {
	peers, err := reqToPeers(grpcReq)
	if err != nil {
		break
	}
	log.WithFields(log.Fields{"Topic": "Operation", "Key": grpcReq.Name}).Info("Neighbor soft reset in")
	for _, peer := range peers {
		pathList := []*table.Path{}
		families := []bgp.RouteFamily{req.RouteFamily}
		if families[0] == bgp.RouteFamily(0) {
			families = peer.configuredRFlist()
		}
		for _, path := range peer.adjRibIn.PathList(families, false) {
			exResult := path.Filtered(peer.ID())
			path.Filter(peer.ID(), table.POLICY_DIRECTION_NONE)
			if server.policy.ApplyPolicy(peer.ID(), table.POLICY_DIRECTION_IN, path, nil) != nil {
				pathList = append(pathList, path.Clone(false))
			} else {
				path.Filter(peer.ID(), table.POLICY_DIRECTION_IN)
				if exResult != table.POLICY_DIRECTION_IN {
					pathList = append(pathList, path.Clone(true))
				}
			}
		}
		peer.adjRibIn.RefreshAcceptedNumber(families)
		m, _ := server.propagateUpdate(peer, pathList)
		msgs = append(msgs, m...)
	}
	return &Response{Data: &SoftResetNeighborResponse{}}
}
func (server *BgpServer) SoftResetNeighborOUT(req *Request) *Response {
	peers, err := reqToPeers(grpcReq)
	if err != nil {
		break
	}
	log.WithFields(log.Fields{"Topic": "Operation", "Key": req.Name}).Info("Neighbor soft reset out")
	for _, peer := range peers {
		if peer.fsm.state != bgp.BGP_FSM_ESTABLISHED {
			continue
		}

		families := []bgp.RouteFamily{grpcReq.RouteFamily}
		if families[0] == bgp.RouteFamily(0) {
			families = peer.configuredRFlist()
		}

		sentPathList := peer.adjRibOut.PathList(families, false)
		peer.adjRibOut.Drop(families)
		pathList, filtered := peer.getBestFromLocal(families)
		if len(pathList) > 0 {
			peer.adjRibOut.Update(pathList)
			msgs = append(msgs, newSenderMsg(peer, pathList, nil, false))
		}
		if len(filtered) > 0 {
			withdrawnList := make([]*table.Path, 0, len(filtered))
			for _, p := range filtered {
				found := false
				for _, sentPath := range sentPathList {
					if p.GetNlri() == sentPath.GetNlri() {
						found = true
						break
					}
				}
				if found {
					withdrawnList = append(withdrawnList, p.Clone(true))
				}
			}
			msgs = append(msgs, newSenderMsg(peer, withdrawnList, nil, false))
		}
	}
	return &Response{Data: &SoftResetNeighborResponse{}}
}
func (server *BgpServer) SoftResetNeighbor(req *Request) *Response {
	log.WithFields(log.Fields{"Topic": "Operation", "Key": req.Name}).Info("Neighbor soft reset")
	_, err := SoftResetNeighborIN(req)
	if err != nil {
		return nil
	}
	res := SoftResetNeighborOUT(req)
	return res
}
func (server *BgpServer) SoftResetNeighborTIMER_EXPIRED(req *Request) *Response {
	peers, err := reqToPeers(req)
	if err != nil {
		break
	}
	logOp(grpcReq.Name, "Neighbor soft reset time expires")
	for _, peer := range peers {
		if peer.fsm.state != bgp.BGP_FSM_ESTABLISHED {
			continue
		}

		families := []bgp.RouteFamily{grpcReq.RouteFamily}
		if families[0] == bgp.RouteFamily(0) {
			families = peer.configuredRFlist()
		}

		if peer.fsm.pConf.GracefulRestart.State.LocalRestarting {
			peer.fsm.pConf.GracefulRestart.State.LocalRestarting = false
			log.WithFields(log.Fields{
				"Topic":    "Peer",
				"Key":      peer.ID(),
				"Families": families,
			}).Debug("deferral timer expired")
		} else if c := config.GetAfiSafi(peer.fsm.pConf, bgp.RF_RTC_UC); peer.fsm.rfMap[bgp.RF_RTC_UC] && !c.MpGracefulRestart.State.EndOfRibReceived {
			log.WithFields(log.Fields{
				"Topic":    "Peer",
				"Key":      peer.ID(),
				"Families": families,
			}).Debug("route-target deferral timer expired")
		} else {
			continue
		}

		sentPathList := peer.adjRibOut.PathList(families, false)
		peer.adjRibOut.Drop(families)
		pathList, filtered := peer.getBestFromLocal(families)
		if len(pathList) > 0 {
			peer.adjRibOut.Update(pathList)
			msgs = append(msgs, newSenderMsg(peer, pathList, nil, false))
		}
	}
	return &Response{Data: &api.SoftResetNeighborResponse{}}
}

func (server *BgpServer) ShutdownNeighbor(req *Request) *Response {
	peers, err := reqToPeers(Req)
	if err != nil {
		break
	}
	log.WithFields(log.Fields{"Topic": "Operation", "Key": Req.Name}).Info("Neighbor shutdown")
	m := bgp.NewBGPNotificationMessage(bgp.BGP_ERROR_CEASE, bgp.BGP_ERROR_SUB_ADMINISTRATIVE_SHUTDOWN, nil)
	for _, peer := range peers {
		msgs = append(msgs, newSenderMsg(peer, nil, m, false))
	}
	return &GrpcResponse{Data: &api.ShutdownNeighborResponse{}}
}

func (server *BgpServer) EnableNeighbor(req *Request) *Response {
	peer, err1 := server.checkNeighborRequest(grpcReq)
	if err1 != nil {
		return nil, err1
	}
	result := &GrpcResponse{}
	select {
	case peer.fsm.adminStateCh <- ADMIN_STATE_UP:
		log.WithFields(log.Fields{
			"Topic": "Peer",
			"Key":   peer.fsm.pConf.Config.NeighborAddress,
		}).Debug("ADMIN_STATE_UP requested")
	default:
		log.Warning("previous request is still remaining. : ", peer.fsm.pConf.Config.NeighborAddress)
		result.ResponseErr = fmt.Errorf("previous request is still remaining %v", peer.fsm.pConf.Config.NeighborAddress)
	}
	result.Data = &api.EnableNeighborResponse{}
	return result
}

func (server *BgpServer) DisableNeighbor(req *Request) *Response {
	peer, err1 := server.checkNeighborRequest(grpcReq)
	if err1 != nil {
		break
	}
	result := &GrpcResponse{}
	select {
	case peer.fsm.adminStateCh <- ADMIN_STATE_DOWN:
		log.WithFields(log.Fields{
			"Topic": "Peer",
			"Key":   peer.fsm.pConf.Config.NeighborAddress,
		}).Debug("ADMIN_STATE_DOWN requested")
	default:
		log.Warning("previous request is still remaining. : ", peer.fsm.pConf.Config.NeighborAddress)
		result.ResponseErr = fmt.Errorf("previous request is still remaining %v", peer.fsm.pConf.Config.NeighborAddress)
	}
	result.Data = &api.DisableNeighborResponse{}
	return result
}

func (server *BgpServer) GetDefinedSet(req *Request) *Response {
	arg := req.Data.(*GetDefinedSetRequest)
	typ := table.DefinedType(arg.Type)
	set, ok := server.policy.DefinedSetMap[typ]
	if !ok {
		return &GetDefinedSetResponse{}, fmt.Errorf("invalid defined-set type: %d", typ)
	}
	sets := make([]*DefinedSet, 0)
	for _, s := range set {
		sets = append(sets, s.ToApiStruct())
	}
	return &Response{
		Data: GetDefinedSetResponse{Sets: sets},
	}
}
func (server *BgpServer) MonitorBestChanged(req *Request) chan Response {
	server.broadcastReqs = append(server.broadcastReqs, req)
	return req.ResponseCh
}
func (server *BgpServer) MonitorRib(req *Request) chan Response {
	if req.Name != "" {
		if _, err = server.checkNeighborRequest(grpcReq); err != nil {
			return nil
		}
	}
	w := server.watchers[WATCHER_GRPC_INCOMING]
	go w.(*grpcIncomingWatcher).addRequest(req)
	return req.ResponseCh
}
func (server *BgpServer) MonitorPeerState(req *Request) chan Response {
	server.broadcastReqs = append(server.broadcastReqs, req)
	return req.ResponseCh
}
func (server *BgpServer) handleAddNeighbor(c *config.Neighbor) ([]*SenderMsg, error) {
	addr := c.Config.NeighborAddress
	if _, y := server.neighborMap[addr]; y {
		return nil, fmt.Errorf("Can't overwrite the exising peer: %s", addr)
	}

	if server.bgpConfig.Global.Config.Port > 0 {
		for _, l := range server.Listeners(addr) {
			SetTcpMD5SigSockopts(l, addr, c.Config.AuthPassword)
		}
	}
	log.Info("Add a peer configuration for ", addr)

	peer := NewPeer(&server.bgpConfig.Global, c, server.globalRib, server.policy)
	server.setPolicyByConfig(peer.ID(), c.ApplyPolicy)
	if peer.isRouteServerClient() {
		pathList := make([]*table.Path, 0)
		rfList := peer.configuredRFlist()
		for _, p := range server.neighborMap {
			if !p.isRouteServerClient() {
				continue
			}
			pathList = append(pathList, p.getAccepted(rfList)...)
		}
		moded := server.RSimportPaths(peer, pathList)
		if len(moded) > 0 {
			server.globalRib.ProcessPaths(nil, moded)
		}
	}
	server.neighborMap[addr] = peer
	peer.startFSMHandler(server.fsmincomingCh, server.fsmStateCh)
	server.broadcastPeerState(peer, bgp.BGP_FSM_IDLE)
	return nil, nil
}

func (server *BgpServer) handleDelNeighbor(c *config.Neighbor, code, subcode uint8) ([]*SenderMsg, error) {
	addr := c.Config.NeighborAddress
	n, y := server.neighborMap[addr]
	if !y {
		return nil, fmt.Errorf("Can't delete a peer configuration for %s", addr)
	}
	for _, l := range server.Listeners(addr) {
		SetTcpMD5SigSockopts(l, addr, "")
	}
	log.Info("Delete a peer configuration for ", addr)

	n.fsm.sendNotification(code, subcode, nil, "")

	go func(addr string) {
		t := time.AfterFunc(time.Minute*5, func() { log.Fatal("failed to free the fsm.h.t for ", addr) })
		n.fsm.h.t.Kill(nil)
		n.fsm.h.t.Wait()
		t.Stop()
		t = time.AfterFunc(time.Minute*5, func() { log.Fatal("failed to free the fsm.h for ", addr) })
		n.fsm.t.Kill(nil)
		n.fsm.t.Wait()
		t.Stop()
	}(addr)
	delete(server.neighborMap, addr)
	m := server.dropPeerAllRoutes(n, n.configuredRFlist())
	return m, nil
}

func (server *BgpServer) handleUpdateNeighbor(c *config.Neighbor) ([]*SenderMsg, bool, error) {
	addr := c.Config.NeighborAddress
	peer := server.neighborMap[addr]
	policyUpdated := false

	if !peer.fsm.pConf.ApplyPolicy.Equal(&c.ApplyPolicy) {
		log.WithFields(log.Fields{
			"Topic": "Peer",
			"Key":   addr,
		}).Info("Update ApplyPolicy")
		server.setPolicyByConfig(peer.ID(), c.ApplyPolicy)
		peer.fsm.pConf.ApplyPolicy = c.ApplyPolicy
		policyUpdated = true
	}
	original := peer.fsm.pConf

	if !original.Config.Equal(&c.Config) || !original.Transport.Config.Equal(&c.Transport.Config) || config.CheckAfiSafisChange(original.AfiSafis, c.AfiSafis) {
		sub := uint8(bgp.BGP_ERROR_SUB_OTHER_CONFIGURATION_CHANGE)
		if original.Config.AdminDown != c.Config.AdminDown {
			sub = bgp.BGP_ERROR_SUB_ADMINISTRATIVE_SHUTDOWN
			state := "Admin Down"
			if c.Config.AdminDown == false {
				state = "Admin Up"
			}
			log.WithFields(log.Fields{
				"Topic": "Peer",
				"Key":   peer.ID(),
				"State": state,
			}).Info("update admin-state configuration")
		} else if original.Config.PeerAs != c.Config.PeerAs {
			sub = bgp.BGP_ERROR_SUB_PEER_DECONFIGURED
		}
		msgs, err := server.handleDelNeighbor(peer.fsm.pConf, bgp.BGP_ERROR_CEASE, sub)
		if err != nil {
			log.WithFields(log.Fields{
				"Topic": "Peer",
				"Key":   addr,
			}).Error(err)
			return msgs, policyUpdated, err
		}
		msgs2, err := server.handleAddNeighbor(c)
		msgs = append(msgs, msgs2...)
		if err != nil {
			log.WithFields(log.Fields{
				"Topic": "Peer",
				"Key":   addr,
			}).Error(err)
		}
		return msgs, policyUpdated, err
	}

	if !original.Timers.Config.Equal(&c.Timers.Config) {
		log.WithFields(log.Fields{
			"Topic": "Peer",
			"Key":   peer.ID(),
		}).Info("update timer configuration")
		peer.fsm.pConf.Timers.Config = c.Timers.Config
	}

	msgs, err := peer.updatePrefixLimitConfig(c.AfiSafis)
	if err != nil {
		log.WithFields(log.Fields{
			"Topic": "Peer",
			"Key":   addr,
		}).Error(err)
		// rollback to original state
		peer.fsm.pConf = original
		return nil, policyUpdated, err
	}
	return msgs, policyUpdated, nil
}

func (server *BgpServer) AddNeighbor(req *Request) *Response {
	var err error
	arg, ok := req.Data.(*AddNeighborRequest)
	if !ok {
		return Response{
			Data:        &AddNeighborResponse{},
			ResponseErr: fmt.Errorf("AddNeighborRequest type assertion failed"),
		}
	} else {
		server.handleAddNeighbor(arg.Peer)
		return Response{
			Data:        &api.AddNeighborResponse{},
			ResponseErr: err,
		}
	}
}

func (server *BgpServer) DeleteNeighbor(req *Request) *Response {
	arg := req.Data.(*DeleteNeighborRequest)
	msg, err := server.handleDelNeighbor(arg.Peer, bgp.BGP_ERROR_CEASE, bgp.BGP_ERROR_SUB_PEER_DECONFIGURED)
	return &Response{
		Data:        &DeleteNeighborResponse{},
		ResponseErr: err,
	}
}

func (server *BgpServer) handleGrpcAddDefinedSet(req *Request) *Response {
	arg := req.Data.(*AddDefinedSetRequest)
	set := arg.Set
	typ := table.DefinedType(set.Type)
	name := set.Name
	var err error
	m, ok := server.policy.DefinedSetMap[typ]
	if !ok {
		return nil, fmt.Errorf("invalid defined-set type: %d", typ)
	}
	d, ok := m[name]
	s, err := table.NewDefinedSetFromApiStruct(set)
	if err != nil {
		return nil, err
	}
	if ok {
		err = d.Append(s)
	} else {
		m[name] = s
	}
	return &Response{
		ResponseErr: err,
		Data:        &AddDefinedSetResponse{},
	}
}

func (server *BgpServer) GrpcDeleteDefinedSet(req *Request) *Response {
	arg := req.Data.(*DeleteDefinedSetRequest)
	set := arg.Set
	typ := table.DefinedType(set.Type)
	name := set.Name
	var err error
	m, ok := server.policy.DefinedSetMap[typ]
	if !ok {
		return nil, fmt.Errorf("invalid defined-set type: %d", typ)
	}
	d, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("not found defined-set: %s", name)
	}
	s, err := table.NewDefinedSetFromApiStruct(set)
	if err != nil {
		return nil, err
	}
	if arg.All {
		if server.policy.InUse(d) {
			return nil, fmt.Errorf("can't delete. defined-set %s is in use", name)
		}
		delete(m, name)
	} else {
		err = d.Remove(s)
	}
	return &Response{
		ResponseErr: err,
		Data:        DeleteDefinedSetResponse{},
	}
}

func (server *BgpServer) ReplaceDefinedSet(req *Request) *Response {
	arg := req.Data.(*ReplaceDefinedSetRequest)
	set := arg.Set
	typ := table.DefinedType(set.Type)
	name := set.Name
	var err error
	m, ok := server.policy.DefinedSetMap[typ]
	if !ok {
		return nil, fmt.Errorf("invalid defined-set type: %d", typ)
	}
	d, ok := m[name]
	if !ok {
		return nil, fmt.Errorf("not found defined-set: %s", name)
	}
	s, err := table.NewDefinedSetFromApiStruct(set)
	if err != nil {
		return nil, err
	}
	return &Response{
		ResponseErr: d.Replace(s),
		Data:        &ReplaceDefinedSetResponse{},
	}
}

func (server *BgpServer) handleGrpcGetStatement(req *Request) *Response {
	l := make([]*Statement, 0)
	for _, s := range server.policy.StatementMap {
		l = append(l, s.ToApiStruct())
	}
	return &GetStatementResponse{Statements: l}
}

func (server *BgpServer) handleGrpcAddStatement(req *Request) *Response {
	var err error
	arg := req.Data.(*AddStatementRequest)
	s, err := table.NewStatementFromApiStruct(arg.Statement, server.policy.DefinedSetMap)
	if err != nil {
		return nil, err
	}
	m := server.policy.StatementMap
	name := s.Name
	if d, ok := m[name]; ok {
		err = d.Add(s)
	} else {
		m[name] = s
	}
	return &Response{
		Data:        &AddStatementResponse{},
		ResponseErr: err,
	}
}

func (server *BgpServer) handleGrpcDeleteStatement(req *Request) *Response {
	var err error
	arg := req.Data.(*DeleteStatementRequest)
	s, err := table.NewStatementFromApiStruct(arg.Statement, server.policy.DefinedSetMap)
	if err != nil {
		return nil, err
	}
	m := server.policy.StatementMap
	name := s.Name
	if d, ok := m[name]; ok {
		if arg.All {
			if server.policy.StatementInUse(d) {
				err = fmt.Errorf("can't delete. statement %s is in use", name)
			} else {
				delete(m, name)
			}
		} else {
			err = d.Remove(s)
		}
	} else {
		err = fmt.Errorf("not found statement: %s", name)
	}
	return Response{
		Data:        &DeleteStatementResponse{},
		ResponseErr: err,
	}
}

func (server *BgpServer) handleGrpcReplaceStatement(req *Request) *Response {
	var err error
	arg := req.Data.(*ReplaceStatementRequest)
	s, err := table.NewStatementFromApiStruct(arg.Statement, server.policy.DefinedSetMap)
	if err != nil {
		return nil, err
	}
	m := server.policy.StatementMap
	name := s.Name
	if d, ok := m[name]; ok {
		err = d.Replace(s)
	} else {
		err = fmt.Errorf("not found statement: %s", name)
	}
	return &Response{
		Data:        &ReplaceStatementResponse{},
		ResponseErr: err,
	}
}

func (server *BgpServer) GrpcGetPolicy(req *Request) *Response {
	policies := make([]*Policy, 0, len(server.policy.PolicyMap))
	for _, s := range server.policy.PolicyMap {
		policies = append(policies, s.ToApiStruct())
	}
	return &Response{
		Data: &GetPolicyResponse{Policies: policies},
	}
}

func (server *BgpServer) policyInUse(x *table.Policy) bool {
	for _, peer := range server.neighborMap {
		for _, dir := range []table.PolicyDirection{table.POLICY_DIRECTION_IN, table.POLICY_DIRECTION_EXPORT, table.POLICY_DIRECTION_EXPORT} {
			for _, y := range server.policy.GetPolicy(peer.ID(), dir) {
				if x.Name() == y.Name() {
					return true
				}
			}
		}
	}
	for _, dir := range []table.PolicyDirection{table.POLICY_DIRECTION_EXPORT, table.POLICY_DIRECTION_EXPORT} {
		for _, y := range server.policy.GetPolicy(table.GLOBAL_RIB_NAME, dir) {
			if x.Name() == y.Name() {
				return true
			}
		}
	}
	return false
}

func (server *BgpServer) handleGrpcAddPolicy(req *Request) *Response {
	policyMutex.Lock()
	defer policyMutex.Unlock()
	rsp := &AddPolicyResponse{}
	arg := req.Data.(*AddPolicyRequest)
	x, err := table.NewPolicyFromApiStruct(arg.Policy, server.policy.DefinedSetMap)
	if err != nil {
		return rsp, err
	}
	pMap := server.policy.PolicyMap
	sMap := server.policy.StatementMap
	name := x.Name()
	y, ok := pMap[name]
	if arg.ReferExistingStatements {
		err = x.FillUp(sMap)
	} else {
		for _, s := range x.Statements {
			if _, ok := sMap[s.Name]; ok {
				return rsp, fmt.Errorf("statement %s already defined", s.Name)
			}
			sMap[s.Name] = s
		}
	}
	if ok {
		err = y.Add(x)
	} else {
		pMap[name] = x
	}
	return &Response{
		Data:        &AddPolicyResponse{},
		ResponseErr: err,
	}
}

func (server *BgpServer) handleGrpcDeletePolicy(req *Request) *Response {
	policyMutex.Lock()
	defer policyMutex.Unlock()
	rsp := &DeletePolicyResponse{}
	arg := req.Data.(*DeletePolicyRequest)
	x, err := table.NewPolicyFromApiStruct(arg.Policy, server.policy.DefinedSetMap)
	if err != nil {
		return rsp, err
	}
	pMap := server.policy.PolicyMap
	sMap := server.policy.StatementMap
	name := x.Name()
	y, ok := pMap[name]
	if !ok {
		return rsp, fmt.Errorf("not found policy: %s", name)
	}
	if arg.All {
		if server.policyInUse(y) {
			return rsp, fmt.Errorf("can't delete. policy %s is in use", name)
		}
		log.WithFields(log.Fields{
			"Topic": "Policy",
			"Key":   name,
		}).Debug("delete policy")
		delete(pMap, name)
	} else {
		err = y.Remove(x)
	}
	if err == nil && !arg.PreserveStatements {
		for _, s := range y.Statements {
			if !server.policy.StatementInUse(s) {
				log.WithFields(log.Fields{
					"Topic": "Policy",
					"Key":   s.Name,
				}).Debug("delete unused statement")
				delete(sMap, s.Name)
			}
		}
	}
	return &Response{
		Data:        rsp,
		ResponseErr: err,
	}
}

func (server *BgpServer) handleGrpcReplacePolicy(req *Request) *Response {
	policyMutex.Lock()
	defer policyMutex.Unlock()
	rsp := &ReplacePolicyResponse{}
	arg := req.Data.(*ReplacePolicyRequest)
	x, err := table.NewPolicyFromApiStruct(arg.Policy, server.policy.DefinedSetMap)
	if err != nil {
		return rsp, err
	}
	pMap := server.policy.PolicyMap
	sMap := server.policy.StatementMap
	name := x.Name()
	y, ok := pMap[name]
	if !ok {
		return rsp, fmt.Errorf("not found policy: %s", name)
	}
	if arg.ReferExistingStatements {
		if err = x.FillUp(sMap); err != nil {
			return rsp, err
		}
	} else {
		for _, s := range x.Statements {
			if _, ok := sMap[s.Name]; ok {
				return rsp, fmt.Errorf("statement %s already defined", s.Name)
			}
			sMap[s.Name] = s
		}
	}

	err = y.Replace(x)
	if err == nil && !arg.PreserveStatements {
		for _, s := range y.Statements {
			if !server.policy.StatementInUse(s) {
				log.WithFields(log.Fields{
					"Topic": "Policy",
					"Key":   s.Name,
				}).Debug("delete unused statement")
				delete(sMap, s.Name)
			}
		}
	}
	return &Response{
		Data:        rsp,
		ResponseErr: err,
	}
}

func (server *BgpServer) getPolicyInfo(a *PolicyAssignment) (string, table.PolicyDirection, error) {
	switch a.Resource {
	case Resource_GLOBAL:
		switch a.Type {
		case PolicyType_IMPORT:
			return table.GLOBAL_RIB_NAME, table.POLICY_DIRECTION_IMPORT, nil
		case PolicyType_EXPORT:
			return table.GLOBAL_RIB_NAME, table.POLICY_DIRECTION_EXPORT, nil
		default:
			return "", table.POLICY_DIRECTION_NONE, fmt.Errorf("invalid policy type")
		}
	case Resource_LOCAL:
		peer, ok := server.neighborMap[a.Name]
		if !ok {
			return "", table.POLICY_DIRECTION_NONE, fmt.Errorf("not found peer %s", a.Name)
		}
		if !peer.isRouteServerClient() {
			return "", table.POLICY_DIRECTION_NONE, fmt.Errorf("non-rs-client peer %s doesn't have per peer policy", a.Name)
		}
		switch a.Type {
		case PolicyType_IN:
			return peer.ID(), table.POLICY_DIRECTION_IN, nil
		case PolicyType_IMPORT:
			return peer.ID(), table.POLICY_DIRECTION_IMPORT, nil
		case PolicyType_EXPORT:
			return peer.ID(), table.POLICY_DIRECTION_EXPORT, nil
		default:
			return "", table.POLICY_DIRECTION_NONE, fmt.Errorf("invalid policy type")
		}
	default:
		return "", table.POLICY_DIRECTION_NONE, fmt.Errorf("invalid resource type")
	}

}

func (server *BgpServer) handleGrpcGetPolicyAssignment(req *Request) *Response {
	rsp := &GetPolicyAssignmentResponse{}
	id, dir, err := server.getPolicyInfo(req.Data.(*GetPolicyAssignmentRequest).Assignment)
	if err != nil {
		return &Response{
			Data:        rsp,
			ResponseErr: err,
		}
	}
	rsp.Assignment = &PolicyAssignment{
		Default: server.policy.GetDefaultPolicy(id, dir).ToApiStruct(),
	}
	ps := server.policy.GetPolicy(id, dir)
	rsp.Assignment.Policies = make([]*Policy, 0, len(ps))
	for _, x := range ps {
		rsp.Assignment.Policies = append(rsp.Assignment.Policies, x.ToApiStruct())
	}
	return &Response{
		Data: rsp,
	}
}

func (server *BgpServer) AddPolicyAssignment(req *Request) *Response {
	var err error
	var dir table.PolicyDirection
	var id string
	rsp := &AddPolicyAssignmentResponse{}
	policyMutex.Lock()
	defer policyMutex.Unlock()
	arg := req.Data.(*AddPolicyAssignmentRequest)
	assignment := arg.Assignment
	id, dir, err = server.getPolicyInfo(assignment)
	if err != nil {
		return &Response{
			Data:        rsp,
			ResponseErr: err,
		}
	}
	ps := make([]*table.Policy, 0, len(assignment.Policies))
	seen := make(map[string]bool)
	for _, x := range assignment.Policies {
		p, ok := server.policy.PolicyMap[x.Name]
		if !ok {
			return rsp, fmt.Errorf("not found policy %s", x.Name)
		}
		if seen[x.Name] {
			return rsp, fmt.Errorf("duplicated policy %s", x.Name)
		}
		seen[x.Name] = true
		ps = append(ps, p)
	}
	cur := server.policy.GetPolicy(id, dir)
	if cur == nil {
		err = server.policy.SetPolicy(id, dir, ps)
	} else {
		seen = make(map[string]bool)
		ps = append(cur, ps...)
		for _, x := range ps {
			if seen[x.Name()] {
				return rsp, fmt.Errorf("duplicated policy %s", x.Name())
			}
			seen[x.Name()] = true
		}
		err = server.policy.SetPolicy(id, dir, ps)
	}
	if err != nil {
		return &Response{
			Data:        rsp,
			ResponseErr: err,
		}
	}

	switch assignment.Default {
	case RouteAction_ACCEPT:
		err = server.policy.SetDefaultPolicy(id, dir, table.ROUTE_TYPE_ACCEPT)
	case RouteAction_REJECT:
		err = server.policy.SetDefaultPolicy(id, dir, table.ROUTE_TYPE_REJECT)
	}
	return &Response{
		Data:        rsp,
		ResponseErr: err,
	}
}

func (server *BgpServer) handleGrpcDeletePolicyAssignment(req *Request) *Response {
	var err error
	var dir table.PolicyDirection
	var id string
	policyMutex.Lock()
	defer policyMutex.Unlock()
	rsp := &DeletePolicyAssignmentResponse{}
	arg := req.Data.(*DeletePolicyAssignmentRequest)
	assignment := arg.Assignment
	id, dir, err = server.getPolicyInfo(assignment)
	if err != nil {
		return &Response{
			Data:        rsp,
			ResponseErr: err,
		}
	}
	ps := make([]*table.Policy, 0, len(assignment.Policies))
	seen := make(map[string]bool)
	for _, x := range assignment.Policies {
		p, ok := server.policy.PolicyMap[x.Name]
		if !ok {
			return &Response{
				Data:        rsp,
				ResponseErr: fmt.Errorf("not found policy %s", x.Name),
			}
		}
		if seen[x.Name] {
			return &Response{
				Data:        rsp,
				ResponseErr: fmt.Errorf("duplicated policy %s", x.Name),
			}
		}
		seen[x.Name] = true
		ps = append(ps, p)
	}
	cur := server.policy.GetPolicy(id, dir)

	if arg.All {
		err = server.policy.SetPolicy(id, dir, nil)
		if err != nil {
			return &Response{
				Data:        rsp,
				ResponseErr: err,
			}
		}
		err = server.policy.SetDefaultPolicy(id, dir, table.ROUTE_TYPE_NONE)
	} else {
		n := make([]*table.Policy, 0, len(cur)-len(ps))
		for _, y := range cur {
			found := false
			for _, x := range ps {
				if x.Name() == y.Name() {
					found = true
					break
				}
			}
			if !found {
				n = append(n, y)
			}
		}
		err = server.policy.SetPolicy(id, dir, n)
	}
	return &Response{
		Data:        rsp,
		ResponseErr: err,
	}
}

func (server *BgpServer) ReplacePolicyAssignment(req *Request) *Response {
	var err error
	var dir table.PolicyDirection
	var id string
	policyMutex.Lock()
	defer policyMutex.Unlock()
	rsp := &ReplacePolicyAssignmentResponse{}
	arg := req.Data.(*ReplacePolicyAssignmentRequest)
	assignment := arg.Assignment
	id, dir, err = server.getPolicyInfo(assignment)
	if err != nil {
		return &Response{
			Data:        rsp,
			ResponseErr: err,
		}
	}
	ps := make([]*table.Policy, 0, len(assignment.Policies))
	seen := make(map[string]bool)
	for _, x := range assignment.Policies {
		p, ok := server.policy.PolicyMap[x.Name]
		if !ok {
			return &Response{
				Data:        rsp,
				ResponseErr: fmt.Errorf("not found policy %s", x.Name),
			}
		}
		if seen[x.Name] {
			return &Response{
				Data:        rsp,
				ResponseErr: fmt.Errorf("duplicated policy %s", x.Name),
			}
		}
		seen[x.Name] = true
		ps = append(ps, p)
	}
	server.policy.GetPolicy(id, dir)
	err = server.policy.SetPolicy(id, dir, ps)
	if err != nil {
		return &Response{
			Data:        rsp,
			ResponseErr: err,
		}
	}
	switch assignment.Default {
	case RouteAction_ACCEPT:
		err = server.policy.SetDefaultPolicy(id, dir, table.ROUTE_TYPE_ACCEPT)
	case RouteAction_REJECT:
		err = server.policy.SetDefaultPolicy(id, dir, table.ROUTE_TYPE_REJECT)
	}
	return &Response{
		Data:        rsp,
		ResponseErr: err,
	}
}
func (server *BgpServer) GetServer(req *Request) *Response {
	g := server.bgpConfig.Global
	return &Response{
		Data: &GetServerResponse{
			Global: &Global{
				As:              g.Config.As,
				RouterId:        g.Config.RouterId,
				ListenPort:      g.Config.Port,
				ListenAddresses: g.Config.LocalAddressList,
				MplsLabelMin:    g.MplsLabelRange.MinLabel,
				MplsLabelMax:    g.MplsLabelRange.MaxLabel,
			},
		},
	}
}
func (server *BgpServer) StartServer(req *Request) *Response {
	err := server.handleModConfig(Req)
	return &Response{
		ResponseErr: err,
		Data:        &StartServerResponse{},
	}
}
func (server *BgpServer) StopServer(req *Request) *Response {
	err := server.handleModConfig(req)
	return &Response{
		ResponseErr: err,
		Data:        &StopServerResponse{},
	}
}
func responseDone(req *Request, e error) *Response {
	result := &Response{
		ResponseErr: e,
	}
	return result
}
func (server *BgpServer) EnableMrtRequest(req *Request) *Response {
	arg := req.Data.(*EnableMrtRequest)
	if _, y := server.watchers[WATCHER_MRT]; y {
		return responceDone(req, fmt.Errorf("already enabled"))
	}
	if arg.Interval != 0 && arg.Interval < 30 {
		log.Info("minimum mrt dump interval is 30 seconds")
		arg.Interval = 30
	}
	w, err := newMrtWatcher(arg.DumpType, arg.Filename, arg.Interval)
	if err == nil {
		server.watchers[WATCHER_MRT] = w
	}
	return &Response{
		ResponseErr: err,
		Data:        &EnableMrtResponse{},
	}, nil
}

func (server *BgpServer) DisableMrtRequest(req *Request) *Response {
	w, y := server.watchers[WATCHER_MRT]
	if !y {
		return responceDone(req, fmt.Errorf("not enabled yet"))
	}

	delete(server.watchers, WATCHER_MRT)
	w.stop()
	return &Response{
		Data: &DisableMrtResponse{},
	}
}

func (server *BgpServer) AddBmp(req *Request) *Response {
	var c *config.BmpServerConfig
	switch arg := req.Data.(type) {
	case *AddBmpRequest:
		c = &config.BmpServerConfig{
			Address: arg.Address,
			Port:    arg.Port,
			RouteMonitoringPolicy: config.BmpRouteMonitoringPolicyType(arg.Type),
		}
	case *config.BmpServerConfig:
		c = arg
	}

	w, y := server.watchers[WATCHER_BMP]
	if !y {
		w, _ = newBmpWatcher(server.GrpcReqCh)
		server.watchers[WATCHER_BMP] = w
	}

	err := w.(*bmpWatcher).addServer(*c)
	return <-&Response{
		ResponseErr: err,
		Data:        &AddBmpResponse{},
	}
}

func (server *BgpServer) DeleteBmp(req *Request) *Response {
	var c *config.BmpServerConfig
	switch arg := req.Data.(type) {
	case *DeleteBmpRequest:
		c = &config.BmpServerConfig{
			Address: arg.Address,
			Port:    arg.Port,
		}
	case *config.BmpServerConfig:
		c = arg
	}

	if w, y := server.watchers[WATCHER_BMP]; y {
		err := w.(*bmpWatcher).deleteServer(*c)
		return &Response{
			ResponseErr: err,
			Data:        &DeleteBmpResponse{},
		}
	} else {
		return responceDone(req, fmt.Errorf("bmp not configured"))
	}
}

func (server *BgpServer) handleValidateRib(req *Request) *Response {
	arg := req.Data.(*ValidateRibRequest)
	for _, rf := range server.globalRib.GetRFlist() {
		if t, ok := server.globalRib.Tables[rf]; ok {
			dsts := t.GetDestinations()
			if arg.Prefix != "" {
				_, prefix, _ := net.ParseCIDR(arg.Prefix)
				if dst := t.GetDestination(prefix.String()); dst != nil {
					dsts = map[string]*table.Destination{prefix.String(): dst}
				}
			}
			for _, dst := range dsts {
				server.roaManager.validate(dst.GetAllKnownPathList())
			}
		}
	}
	return &Response{
		Data: &ValidateRibResponse{},
	}
}

func (server *BgpServer) handleModRpki(req *Request, data interface{}, e error) *Response {
	return &Response{
		ResponseErr: e,
		Data:        data,
	}
}
func (server *BgpServer) AddRpki(req *Request) *Response {
	return handleModRpki(req, &AddRpkiResponse{}, server.roaManager.AddServer(net.JoinHostPort(arg.Address, strconv.Itoa(int(arg.Port))), arg.Lifetime))
}
func (server *BgpServer) AddRpki(req *Request) *Response {
	return handleModRpki(req, &DeleteRpkiResponse{}, server.roaManager.DeleteServer(arg.Address))
}
func (server *BgpServer) AddRpki(req *Request) *Response {
	return handleModRpki(req, &EnableRpkiResponse{}, server.roaManager.Enable(arg.Address))
}
func (server *BgpServer) AddRpki(req *Request) *Response {
	return handleModRpki(req, &DisableRpkiResponse{}, server.roaManager.Disable(arg.Address))
}
func (server *BgpServer) AddRpki(req *Request) *Response {
	return handleModRpki(req, &ResetRpkiResponse{}, server.roaManager.Reset(arg.Address))
}
func (server *BgpServer) AddRpki(req *Request) *Response {
	return handleModRpki(req, &SoftResetRpkiResponse{}, server.roaManager.SoftReset(arg.Address))
}
func (server *BgpServer) GetRpki(req *Request) *Response {
	return server.roaManager.GetRpki(req)
}
func (server *BgpServer) GetRoa(req *Request) *Response {
	return server.roaManager.GetRpki(req)
}
