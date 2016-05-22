// Copyright (C) 2014,2015 Nippon Telegraph and Telephone Corporation.
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

package api

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/osrg/gobgp/config"
	"github.com/osrg/gobgp/packet/bgp"
	bgpserver "github.com/osrg/gobgp/server"
	"github.com/osrg/gobgp/table"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"io"
	"net"
	"strings"
)

type Server struct {
	grpcServer *grpc.Server
	bgpServer  *bgpserver
	hosts      string
}

func (s *Server) Serve() error {
	l := strings.Split(s.hosts, ",")
	for i, host := range l {
		lis, err := net.Listen("tcp", fmt.Sprintf(host))
		if err != nil {
			return fmt.Errorf("failed to listen: %v", err)
		}
		if i == len(l)-1 {
			s.grpcServer.Serve(lis)
		} else {
			go func() {
				s.grpcServer.Serve(lis)
			}()
		}
	}
	return nil
}

func (s *Server) GetNeighbor(ctx context.Context, arg *GetNeighborRequest) (*GetNeighborResponse, error) {
	var rf bgp.RouteFamily
	req := bgpserver.NewRequest("", rf, nil)
	res := bgpserver.GetNeighbor(req)
	if res.Err() != nil {
		return nil, res.Err()
	}
	return res.Data.(*GetNeighborResponse), nil
}

func handleMultipleResponses(req *bgpserver.Request, f func(*GrpcResponse) error) error {
	for res := range req.ResponseCh {
		if err := res.Err(); err != nil {
			log.Debug(err.Error())
			req.EndCh <- struct{}{}
			return err
		}
		if err := f(res); err != nil {
			req.EndCh <- struct{}{}
			return err
		}
	}
	return nil
}

func (s *Server) GetRib(ctx context.Context, arg *GetRibRequest) (*GetRibResponse, error) {
	var reqType int
	switch arg.Table.Type {
	case Resource_LOCAL:
		reqType = REQ_LOCAL_RIB
	case Resource_GLOBAL:
		reqType = REQ_GLOBAL_RIB
	case Resource_ADJ_IN:
		reqType = REQ_ADJ_RIB_IN
	case Resource_ADJ_OUT:
		reqType = REQ_ADJ_RIB_OUT
	case Resource_VRF:
		reqType = REQ_VRF
	default:
		return nil, fmt.Errorf("unsupported resource type: %v", arg.Table.Type)
	}
	d, err := s.get(reqType, arg)
	if err != nil {
		return nil, err
	}
	return d.(*GetRibResponse), nil
}

func (s *Server) MonitorBestChanged(arg *Arguments, stream GobgpApi_MonitorBestChangedServer) error {
	var reqType int
	switch arg.Resource {
	case Resource_GLOBAL:
		reqType = REQ_MONITOR_GLOBAL_BEST_CHANGED
	default:
		return fmt.Errorf("unsupported resource type: %v", arg.Resource)
	}

	req := bgpserver.NewRequest(reqType, "", bgp.RouteFamily(arg.Family), nil)
	s.bgpServerCh <- req

	return handleMultipleResponses(req, func(res *GrpcResponse) error {
		return stream.Send(res.Data.(*Destination))
	})
}

func (s *Server) MonitorRib(arg *Table, stream GobgpApi_MonitorRibServer) error {
	switch arg.Type {
	case Resource_ADJ_IN:
	default:
		return fmt.Errorf("unsupported resource type: %v", arg.Type)
	}

	req := bgpserver.NewRequest(REQ_MONITOR_INCOMING, arg.Name, bgp.RouteFamily(arg.Family), arg)
	s.bgpServerCh <- req
	return handleMultipleResponses(req, func(res *GrpcResponse) error {
		return stream.Send(res.Data.(*Destination))
	})
}

func (s *Server) MonitorPeerState(arg *Arguments, stream GobgpApi_MonitorPeerStateServer) error {
	var rf bgp.RouteFamily
	req := bgpserver.NewRequest(REQ_MONITOR_NEIGHBOR_PEER_STATE, arg.Name, rf, nil)
	s.bgpServerCh <- req

	return handleMultipleResponses(req, func(res *GrpcResponse) error {
		return stream.Send(res.Data.(*Peer))
	})
}

func (s *Server) neighbor(reqType int, address string, d interface{}) (interface{}, error) {
	req := bgpserver.NewRequest(reqType, address, bgp.RouteFamily(0), d)
	s.bgpServerCh <- req
	res := <-req.ResponseCh
	return res.Data, res.Err()
}

func (s *Server) ResetNeighbor(ctx context.Context, arg *ResetNeighborRequest) (*ResetNeighborResponse, error) {
	d, err := s.neighbor(REQ_NEIGHBOR_RESET, arg.Address, arg)
	if err != nil {
		return nil, err
	}
	return d.(*ResetNeighborResponse), err
}

func (s *Server) SoftResetNeighbor(ctx context.Context, arg *SoftResetNeighborRequest) (*SoftResetNeighborResponse, error) {
	op := REQ_NEIGHBOR_SOFT_RESET
	switch arg.Direction {
	case SoftResetNeighborRequest_IN:
		op = REQ_NEIGHBOR_SOFT_RESET_IN
	case SoftResetNeighborRequest_OUT:
		op = REQ_NEIGHBOR_SOFT_RESET_OUT
	}
	d, err := s.neighbor(op, arg.Address, arg)
	if err != nil {
		return nil, err
	}
	return d.(*SoftResetNeighborResponse), err
}

func (s *Server) ShutdownNeighbor(ctx context.Context, arg *ShutdownNeighborRequest) (*ShutdownNeighborResponse, error) {
	d, err := s.neighbor(REQ_NEIGHBOR_SHUTDOWN, arg.Address, arg)
	if err != nil {
		return nil, err
	}
	return d.(*ShutdownNeighborResponse), err
}

func (s *Server) EnableNeighbor(ctx context.Context, arg *EnableNeighborRequest) (*EnableNeighborResponse, error) {
	d, err := s.neighbor(REQ_NEIGHBOR_ENABLE, arg.Address, arg)
	if err != nil {
		return nil, err
	}
	return d.(*EnableNeighborResponse), err
}

func (s *Server) DisableNeighbor(ctx context.Context, arg *DisableNeighborRequest) (*DisableNeighborResponse, error) {
	d, err := s.neighbor(REQ_NEIGHBOR_DISABLE, arg.Address, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DisableNeighborResponse), err
}

func (s *Server) AddPath(ctx context.Context, arg *AddPathRequest) (*AddPathResponse, error) {
	d, err := s.get(REQ_ADD_PATH, arg)
	if err != nil {
		return nil, err
	}
	return d.(*AddPathResponse), err
}

func (s *Server) DeletePath(ctx context.Context, arg *DeletePathRequest) (*DeletePathResponse, error) {
	d, err := s.get(REQ_DELETE_PATH, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DeletePathResponse), err
}

func (s *Server) EnableMrt(ctx context.Context, arg *EnableMrtRequest) (*EnableMrtResponse, error) {
	d, err := s.get(REQ_ENABLE_MRT, arg)
	if err != nil {
		return nil, err
	}
	return d.(*EnableMrtResponse), err
}

func (s *Server) DisableMrt(ctx context.Context, arg *DisableMrtRequest) (*DisableMrtResponse, error) {
	d, err := s.get(REQ_DISABLE_MRT, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DisableMrtResponse), err
}

func (s *Server) InjectMrt(stream GobgpApi_InjectMrtServer) error {
	for {
		arg, err := stream.Recv()

		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}

		if arg.Resource != Resource_GLOBAL && arg.Resource != Resource_VRF {
			return fmt.Errorf("unsupported resource: %s", arg.Resource)
		}

		req := bgpserver.NewRequest(REQ_INJECT_MRT, "", bgp.RouteFamily(0), arg)
		s.bgpServerCh <- req

		res := <-req.ResponseCh
		if err := res.Err(); err != nil {
			log.Debug(err.Error())
			return err
		}
	}
	return stream.SendAndClose(&InjectMrtResponse{})
}

func (s *Server) AddBmp(ctx context.Context, arg *AddBmpRequest) (*AddBmpResponse, error) {
	d, err := s.get(REQ_ADD_BMP, arg)
	if err != nil {
		return nil, err
	}
	return d.(*AddBmpResponse), err
}

func (s *Server) DeleteBmp(ctx context.Context, arg *DeleteBmpRequest) (*DeleteBmpResponse, error) {
	d, err := s.get(REQ_DELETE_BMP, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DeleteBmpResponse), err
}

func (s *Server) ValidateRib(ctx context.Context, arg *ValidateRibRequest) (*ValidateRibResponse, error) {
	d, err := s.get(REQ_VALIDATE_RIB, arg)
	if err != nil {
		return nil, err
	}
	return d.(*ValidateRibResponse), err
}

func (s *Server) AddRpki(ctx context.Context, arg *AddRpkiRequest) (*AddRpkiResponse, error) {
	d, err := s.get(REQ_ADD_RPKI, arg)
	if err != nil {
		return nil, err
	}
	return d.(*AddRpkiResponse), err
}

func (s *Server) DeleteRpki(ctx context.Context, arg *DeleteRpkiRequest) (*DeleteRpkiResponse, error) {
	d, err := s.get(REQ_DELETE_RPKI, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DeleteRpkiResponse), err
}

func (s *Server) EnableRpki(ctx context.Context, arg *EnableRpkiRequest) (*EnableRpkiResponse, error) {
	d, err := s.get(REQ_ENABLE_RPKI, arg)
	if err != nil {
		return nil, err
	}
	return d.(*EnableRpkiResponse), err
}

func (s *Server) DisableRpki(ctx context.Context, arg *DisableRpkiRequest) (*DisableRpkiResponse, error) {
	d, err := s.get(REQ_DISABLE_RPKI, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DisableRpkiResponse), err
}

func (s *Server) ResetRpki(ctx context.Context, arg *ResetRpkiRequest) (*ResetRpkiResponse, error) {
	d, err := s.get(REQ_RESET_RPKI, arg)
	if err != nil {
		return nil, err
	}
	return d.(*ResetRpkiResponse), err
}

func (s *Server) SoftResetRpki(ctx context.Context, arg *SoftResetRpkiRequest) (*SoftResetRpkiResponse, error) {
	d, err := s.get(REQ_SOFT_RESET_RPKI, arg)
	if err != nil {
		return nil, err
	}
	return d.(*SoftResetRpkiResponse), err
}

func (s *Server) GetRpki(ctx context.Context, arg *GetRpkiRequest) (*GetRpkiResponse, error) {
	req := bgpserver.NewRequest(REQ_GET_RPKI, "", bgp.RouteFamily(arg.Family), nil)
	s.bgpServerCh <- req
	res := <-req.ResponseCh
	if res.Err() != nil {
		return nil, res.Err()
	}
	return res.Data.(*GetRpkiResponse), res.Err()
}

func (s *Server) GetRoa(ctx context.Context, arg *GetRoaRequest) (*GetRoaResponse, error) {
	req := bgpserver.NewRequest(REQ_ROA, "", bgp.RouteFamily(arg.Family), nil)
	res := bgpserver.GetRoa(req)
	if res.Err() != nil {
		return nil, res.Err()
	}
	return res.Data.(*GetRoaResponse), res.Err()
}

func (s *Server) GetVrf(ctx context.Context, arg *GetVrfRequest) (*GetVrfResponse, error) {
	req := bgpserver.NewRequest("", bgp.RouteFamily(0), nil)
	res := bgpserver.GetVrf(req)
	if res.Err() != nil {
		return nil, res.Err()
	}
	return res.Data.(*GetVrfResponse), res.Err()
}

func (s *Server) get(d interface{}) (interface{}, error) {
	req := bgpserver.NewRequest("", bgp.RouteFamily(0), d)
	s.bgpServerCh <- req
	res := <-req.ResponseCh
	return res.Data, res.Err()
}

func (s *Server) AddVrf(ctx context.Context, arg *AddVrfRequest) (*AddVrfResponse, error) {
	d, err := s.get(REQ_ADD_VRF, arg)
	if err != nil {
		return nil, err
	}
	return d.(*AddVrfResponse), err
}

func (s *Server) DeleteVrf(ctx context.Context, arg *DeleteVrfRequest) (*DeleteVrfResponse, error) {
	d, err := s.get(REQ_DELETE_VRF, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DeleteVrfResponse), err
}

func (s *Server) AddNeighbor(ctx context.Context, arg *AddNeighborRequest) (*AddNeighborResponse, error) {
	d, err := s.get(REQ_GRPC_ADD_NEIGHBOR, arg)
	if err != nil {
		return nil, err
	}
	return d.(*AddNeighborResponse), err
}

func (s *Server) DeleteNeighbor(ctx context.Context, arg *DeleteNeighborRequest) (*DeleteNeighborResponse, error) {
	d, err := s.get(REQ_GRPC_DELETE_NEIGHBOR, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DeleteNeighborResponse), err
}

func (s *Server) GetDefinedSet(ctx context.Context, arg *GetDefinedSetRequest) (*GetDefinedSetResponse, error) {
	d, err := s.get(REQ_GET_DEFINED_SET, arg)
	if err != nil {
		return nil, err
	}
	return d.(*GetDefinedSetResponse), err
}

func (s *Server) AddDefinedSet(ctx context.Context, arg *AddDefinedSetRequest) (*AddDefinedSetResponse, error) {
	d, err := s.get(REQ_ADD_DEFINED_SET, arg)
	if err != nil {
		return nil, err
	}
	return d.(*AddDefinedSetResponse), err
}

func (s *Server) DeleteDefinedSet(ctx context.Context, arg *DeleteDefinedSetRequest) (*DeleteDefinedSetResponse, error) {
	d, err := s.get(REQ_DELETE_DEFINED_SET, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DeleteDefinedSetResponse), err
}

func (s *Server) ReplaceDefinedSet(ctx context.Context, arg *ReplaceDefinedSetRequest) (*ReplaceDefinedSetResponse, error) {
	d, err := s.get(REQ_REPLACE_DEFINED_SET, arg)
	if err != nil {
		return nil, err
	}
	return d.(*ReplaceDefinedSetResponse), err
}

func (s *Server) GetStatement(ctx context.Context, arg *GetStatementRequest) (*GetStatementResponse, error) {
	d, err := s.get(REQ_GET_STATEMENT, arg)
	if err != nil {
		return nil, err
	}
	return d.(*GetStatementResponse), err
}

func (s *Server) AddStatement(ctx context.Context, arg *AddStatementRequest) (*AddStatementResponse, error) {
	d, err := s.get(REQ_ADD_STATEMENT, arg)
	if err != nil {
		return nil, err
	}
	return d.(*AddStatementResponse), err
}

func (s *Server) DeleteStatement(ctx context.Context, arg *DeleteStatementRequest) (*DeleteStatementResponse, error) {
	d, err := s.get(REQ_DELETE_STATEMENT, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DeleteStatementResponse), err
}

func (s *Server) ReplaceStatement(ctx context.Context, arg *ReplaceStatementRequest) (*ReplaceStatementResponse, error) {
	d, err := s.get(REQ_REPLACE_STATEMENT, arg)
	if err != nil {
		return nil, err
	}
	return d.(*ReplaceStatementResponse), err
}

func (s *Server) GetPolicy(ctx context.Context, arg *GetPolicyRequest) (*GetPolicyResponse, error) {
	d, err := s.get(REQ_GET_POLICY, arg)
	if err != nil {
		return nil, err
	}
	return d.(*GetPolicyResponse), err
}

func (s *Server) AddPolicy(ctx context.Context, arg *AddPolicyRequest) (*AddPolicyResponse, error) {
	d, err := s.get(REQ_ADD_POLICY, arg)
	if err != nil {
		return nil, err
	}
	return d.(*AddPolicyResponse), err
}

func (s *Server) DeletePolicy(ctx context.Context, arg *DeletePolicyRequest) (*DeletePolicyResponse, error) {
	d, err := s.get(REQ_DELETE_POLICY, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DeletePolicyResponse), err
}

func (s *Server) ReplacePolicy(ctx context.Context, arg *ReplacePolicyRequest) (*ReplacePolicyResponse, error) {
	d, err := s.get(REQ_REPLACE_POLICY, arg)
	if err != nil {
		return nil, err
	}
	return d.(*ReplacePolicyResponse), err
}

func (s *Server) GetPolicyAssignment(ctx context.Context, arg *GetPolicyAssignmentRequest) (*GetPolicyAssignmentResponse, error) {
	d, err := s.get(REQ_GET_POLICY_ASSIGNMENT, arg)
	if err != nil {
		return nil, err
	}
	return d.(*GetPolicyAssignmentResponse), err
}

func (s *Server) AddPolicyAssignment(ctx context.Context, arg *AddPolicyAssignmentRequest) (*AddPolicyAssignmentResponse, error) {
	d, err := s.get(REQ_ADD_POLICY_ASSIGNMENT, arg)
	if err != nil {
		return nil, err
	}
	return d.(*AddPolicyAssignmentResponse), err
}

func (s *Server) DeletePolicyAssignment(ctx context.Context, arg *DeletePolicyAssignmentRequest) (*DeletePolicyAssignmentResponse, error) {
	d, err := s.get(REQ_DELETE_POLICY_ASSIGNMENT, arg)
	if err != nil {
		return nil, err
	}
	return d.(*DeletePolicyAssignmentResponse), err
}

func (s *Server) ReplacePolicyAssignment(ctx context.Context, arg *ReplacePolicyAssignmentRequest) (*ReplacePolicyAssignmentResponse, error) {
	d, err := s.get(REQ_REPLACE_POLICY_ASSIGNMENT, arg)
	if err != nil {
		return nil, err
	}
	return d.(*ReplacePolicyAssignmentResponse), err
}

func (s *Server) GetServer(ctx context.Context, arg *GetServerRequest) (*GetServerResponse, error) {
	d, err := s.get(REQ_GET_SERVER, arg)
	if err != nil {
		return nil, err
	}
	return d.(*GetServerResponse), err
}

func (s *Server) StartServer(ctx context.Context, arg *StartServerRequest) (*StartServerResponse, error) {
	d, err := s.get(REQ_START_SERVER, arg)
	if err != nil {
		return nil, err
	}
	return d.(*StartServerResponse), err
}

func (s *Server) StopServer(ctx context.Context, arg *StopServerRequest) (*StopServerResponse, error) {
	d, err := s.get(REQ_STOP_SERVER, arg)
	if err != nil {
		return nil, err
	}
	return d.(*StopServerResponse), err
}

func (s *Server) ToPeerAPIStruct(peer *bgpserver.Peer) *Peer {
	f := peer.fsm
	c := f.pConf

	remoteCap := make([][]byte, 0, len(peer.fsm.capMap))
	for _, c := range peer.fsm.capMap {
		for _, m := range c {
			buf, _ := m.Serialize()
			remoteCap = append(remoteCap, buf)
		}
	}

	caps := capabilitiesFromConfig(peer.fsm.pConf)
	localCap := make([][]byte, 0, len(caps))
	for _, c := range caps {
		buf, _ := c.Serialize()
		localCap = append(localCap, buf)
	}

	prefixLimits := make([]*PrefixLimit, 0, len(peer.fsm.pConf.AfiSafis))
	for _, family := range peer.fsm.pConf.AfiSafis {
		if c := family.PrefixLimit.Config; c.MaxPrefixes > 0 {
			k, _ := bgp.GetRouteFamily(string(family.Config.AfiSafiName))
			prefixLimits = append(prefixLimits, &PrefixLimit{
				Family:               uint32(k),
				MaxPrefixes:          c.MaxPrefixes,
				ShutdownThresholdPct: uint32(c.ShutdownThresholdPct),
			})
		}
	}

	conf := &PeerConf{
		NeighborAddress:  c.Config.NeighborAddress,
		Id:               peer.fsm.peerInfo.ID.To4().String(),
		PeerAs:           c.Config.PeerAs,
		LocalAs:          c.Config.LocalAs,
		PeerType:         uint32(c.Config.PeerType.ToInt()),
		AuthPassword:     c.Config.AuthPassword,
		RemovePrivateAs:  uint32(c.Config.RemovePrivateAs.ToInt()),
		RouteFlapDamping: c.Config.RouteFlapDamping,
		SendCommunity:    uint32(c.Config.SendCommunity.ToInt()),
		Description:      c.Config.Description,
		PeerGroup:        c.Config.PeerGroup,
		RemoteCap:        remoteCap,
		LocalCap:         localCap,
		PrefixLimits:     prefixLimits,
	}

	timer := c.Timers
	s := c.State

	advertised := uint32(0)
	received := uint32(0)
	accepted := uint32(0)
	if f.state == bgp.BGP_FSM_ESTABLISHED {
		rfList := peer.configuredRFlist()
		advertised = uint32(peer.adjRibOut.Count(rfList))
		received = uint32(peer.adjRibIn.Count(rfList))
		accepted = uint32(peer.adjRibIn.Accepted(rfList))
	}

	uptime := int64(0)
	if timer.State.Uptime != 0 {
		uptime = timer.State.Uptime
	}
	downtime := int64(0)
	if timer.State.Downtime != 0 {
		downtime = timer.State.Downtime
	}

	timerconf := &TimersConfig{
		ConnectRetry:      uint64(timer.Config.ConnectRetry),
		HoldTime:          uint64(timer.Config.HoldTime),
		KeepaliveInterval: uint64(timer.Config.KeepaliveInterval),
	}

	timerstate := &TimersState{
		KeepaliveInterval:  uint64(timer.State.KeepaliveInterval),
		NegotiatedHoldTime: uint64(timer.State.NegotiatedHoldTime),
		Uptime:             uint64(uptime),
		Downtime:           uint64(downtime),
	}

	imer := &Timers{
		Config: timerconf,
		State:  timerstate,
	}
	msgrcv := &Message{
		NOTIFICATION: s.Messages.Received.Notification,
		UPDATE:       s.Messages.Received.Update,
		OPEN:         s.Messages.Received.Open,
		KEEPALIVE:    s.Messages.Received.Keepalive,
		REFRESH:      s.Messages.Received.Refresh,
		DISCARDED:    s.Messages.Received.Discarded,
		TOTAL:        s.Messages.Received.Total,
	}
	msgsnt := &Message{
		NOTIFICATION: s.Messages.Sent.Notification,
		UPDATE:       s.Messages.Sent.Update,
		OPEN:         s.Messages.Sent.Open,
		KEEPALIVE:    s.Messages.Sent.Keepalive,
		REFRESH:      s.Messages.Sent.Refresh,
		DISCARDED:    s.Messages.Sent.Discarded,
		TOTAL:        s.Messages.Sent.Total,
	}
	msg := &Messages{
		Received: msgrcv,
		Sent:     msgsnt,
	}
	info := &PeerState{
		BgpState:   f.state.String(),
		AdminState: f.adminState.String(),
		Messages:   msg,
		Received:   received,
		Accepted:   accepted,
		Advertised: advertised,
	}
	rr := &RouteReflector{
		RouteReflectorClient:    peer.fsm.pConf.RouteReflector.Config.RouteReflectorClient,
		RouteReflectorClusterId: string(peer.fsm.pConf.RouteReflector.Config.RouteReflectorClusterId),
	}
	rs := &RouteServer{
		RouteServerClient: peer.fsm.pConf.RouteServer.Config.RouteServerClient,
	}

	return &Peer{
		Conf:           conf,
		Info:           info,
		Timers:         imer,
		RouteReflector: rr,
		RouteServer:    rs,
	}
}

func NewGrpcServer(hosts string, bgpServerCh chan *GrpcRequest) *Server {
	grpc.EnableTracing = false
	grpcServer := grpc.NewServer()
	server := &Server{
		grpcServer:  grpcServer,
		bgpServerCh: bgpServerCh,
		hosts:       hosts,
	}
	RegisterGobgpApiServer(grpcServer, server)
	return server
}
func (s *Server) ToVrfApiStruct(v *table.Vrf) *Vrf {
	f := func(rts []bgp.ExtendedCommunityInterface) [][]byte {
		ret := make([][]byte, 0, len(rts))
		for _, rt := range rts {
			b, _ := rt.Serialize()
			ret = append(ret, b)
		}
		return ret
	}
	rd, _ := v.Rd.Serialize()
	return &Vrf{
		Name:     v.Name,
		Rd:       rd,
		ImportRt: f(v.ImportRt),
		ExportRt: f(v.ExportRt),
	}
}
func (s *Server) ToVrfConfigStruct(v *Vrf) (*table.Vrf, error) {
	var result *table.Vrf
	rd := bgp.GetRouteDistinguisher(arg.Vrf.Rd)
	f := func(bufs [][]byte) ([]bgp.ExtendedCommunityInterface, error) {
		ret := make([]bgp.ExtendedCommunityInterface, 0, len(bufs))
		for _, rt := range bufs {
			r, err := bgp.ParseExtended(rt)
			if err != nil {
				return nil, err
			}
			ret = append(ret, r)
		}
		return ret, nil
	}
	importRt, err := f(arg.Vrf.ImportRt)
	if err != nil {
		return nil, err
	}
	exportRt, err := f(arg.Vrf.ExportRt)
	if err != nil {
		return nil, err
	}
	pi := &table.PeerInfo{
		AS:      server.bgpConfig.Global.Config.As,
		LocalID: net.ParseIP(server.bgpConfig.Global.Config.RouterId).To4(),
	}
	return &table.Vrf{
		Name:     v.Name,
		Rd:       rd,
		ImportRt: importRt,
		ExportRt: exportRt,
	}
}
func (s *Server) ToPathApiStruct(path *table.Path, id string) *Path {
	nlri := path.GetNlri()
	n, _ := nlri.Serialize()
	family := uint32(bgp.AfiSafiToRouteFamily(nlri.AFI(), nlri.SAFI()))
	pattrs := func(arg []bgp.PathAttributeInterface) [][]byte {
		ret := make([][]byte, 0, len(arg))
		for _, a := range arg {
			aa, _ := a.Serialize()
			ret = append(ret, aa)
		}
		return ret
	}(path.GetPathAttrs())
	return &Path{
		Nlri:           n,
		Pattrs:         pattrs,
		Age:            path.OriginInfo().timestamp.Unix(),
		IsWithdraw:     path.IsWithdraw,
		Validation:     int32(path.OriginInfo().validation.ToInt()),
		Filtered:       path.Filtered(id) == POLICY_DIRECTION_IN,
		Family:         family,
		SourceAsn:      path.OriginInfo().source.AS,
		SourceId:       path.OriginInfo().source.ID.String(),
		NeighborIp:     path.OriginInfo().source.Address.String(),
		Stale:          path.IsStale(),
		IsFromExternal: path.OriginInfo().isFromExternal,
	}
}
func (s *Server) ToApiDestinationStruct(dd *table.Destination, id string) *Destination {
	prefix := dd.GetNlri().String()
	paths := func(arg []*table.Path) []*table.Path {
		ret := make([]*table.Path, 0, len(arg))
		first := true
		for _, p := range arg {
			if p.Filtered(id) == POLICY_DIRECTION_NONE {
				pp := s.ToPathApiStruct(p, id)
				if first {
					pp.Best = true
					first = false
				}
				ret = append(ret, pp)
			}
		}
		return ret
	}(dd.knownPathList)

	if len(paths) == 0 {
		return nil
	}
	return &Destination{
		Prefix: prefix,
		Paths:  paths,
	}
}
func (s *Server) PeerApiToConfig(a *Peer) (*config.Neighbor, error) {
	pconf := &config.Neighbor{}
	if a.Conf != nil {
		pconf.Config.NeighborAddress = a.Conf.NeighborAddress
		pconf.Config.PeerAs = a.Conf.PeerAs
		if a.Conf.LocalAs == 0 {
			pconf.Config.LocalAs = server.bgpConfig.Global.Config.As
		} else {
			pconf.Config.LocalAs = a.Conf.LocalAs
		}
		if pconf.Config.PeerAs != pconf.Config.LocalAs {
			pconf.Config.PeerType = config.PEER_TYPE_EXTERNAL
		} else {
			pconf.Config.PeerType = config.PEER_TYPE_INTERNAL
		}
		pconf.Config.AuthPassword = a.Conf.AuthPassword
		pconf.Config.RemovePrivateAs = config.RemovePrivateAsOption(a.Conf.RemovePrivateAs)
		pconf.Config.RouteFlapDamping = a.Conf.RouteFlapDamping
		pconf.Config.SendCommunity = config.CommunityType(a.Conf.SendCommunity)
		pconf.Config.Description = a.Conf.Description
		pconf.Config.PeerGroup = a.Conf.PeerGroup
		pconf.Config.NeighborAddress = a.Conf.NeighborAddress
	}
	if a.Timers != nil {
		if a.Timers.Config != nil {
			pconf.Timers.Config.ConnectRetry = float64(a.Timers.Config.ConnectRetry)
			pconf.Timers.Config.HoldTime = float64(a.Timers.Config.HoldTime)
			pconf.Timers.Config.KeepaliveInterval = float64(a.Timers.Config.KeepaliveInterval)
			pconf.Timers.Config.MinimumAdvertisementInterval = float64(a.Timers.Config.MinimumAdvertisementInterval)
		}
	} else {
		pconf.Timers.Config.ConnectRetry = float64(config.DEFAULT_CONNECT_RETRY)
		pconf.Timers.Config.HoldTime = float64(config.DEFAULT_HOLDTIME)
		pconf.Timers.Config.KeepaliveInterval = float64(config.DEFAULT_HOLDTIME / 3)
	}
	if a.RouteReflector != nil {
		pconf.RouteReflector.Config.RouteReflectorClusterId = config.RrClusterIdType(a.RouteReflector.RouteReflectorClusterId)
		pconf.RouteReflector.Config.RouteReflectorClient = a.RouteReflector.RouteReflectorClient
	}
	if a.RouteServer != nil {
		pconf.RouteServer.Config.RouteServerClient = a.RouteServer.RouteServerClient
	}
	if a.ApplyPolicy != nil {
		if a.ApplyPolicy.ImportPolicy != nil {
			pconf.ApplyPolicy.Config.DefaultImportPolicy = config.DefaultPolicyType(a.ApplyPolicy.ImportPolicy.Default)
			for _, p := range a.ApplyPolicy.ImportPolicy.Policies {
				pconf.ApplyPolicy.Config.ImportPolicyList = append(pconf.ApplyPolicy.Config.ImportPolicyList, p.Name)
			}
		}
		if a.ApplyPolicy.ExportPolicy != nil {
			pconf.ApplyPolicy.Config.DefaultExportPolicy = config.DefaultPolicyType(a.ApplyPolicy.ExportPolicy.Default)
			for _, p := range a.ApplyPolicy.ExportPolicy.Policies {
				pconf.ApplyPolicy.Config.ExportPolicyList = append(pconf.ApplyPolicy.Config.ExportPolicyList, p.Name)
			}
		}
		if a.ApplyPolicy.InPolicy != nil {
			pconf.ApplyPolicy.Config.DefaultInPolicy = config.DefaultPolicyType(a.ApplyPolicy.InPolicy.Default)
			for _, p := range a.ApplyPolicy.InPolicy.Policies {
				pconf.ApplyPolicy.Config.InPolicyList = append(pconf.ApplyPolicy.Config.InPolicyList, p.Name)
			}
		}
	}
	if a.Families != nil {
		for _, family := range a.Families {
			name, ok := bgp.AddressFamilyNameMap[bgp.RouteFamily(family)]
			if !ok {
				return pconf, fmt.Errorf("invalid address family: %d", family)
			}
			cAfiSafi := config.AfiSafi{
				Config: config.AfiSafiConfig{
					AfiSafiName: config.AfiSafiType(name),
				},
			}
			pconf.AfiSafis = append(pconf.AfiSafis, cAfiSafi)
		}
	} else {
		if net.ParseIP(a.Conf.NeighborAddress).To4() != nil {
			pconf.AfiSafis = []config.AfiSafi{
				config.AfiSafi{
					Config: config.AfiSafiConfig{
						AfiSafiName: "ipv4-unicast",
					},
				},
			}
		} else {
			pconf.AfiSafis = []config.AfiSafi{
				config.AfiSafi{
					Config: config.AfiSafiConfig{
						AfiSafiName: "ipv6-unicast",
					},
				},
			}
		}
	}
	if a.Transport != nil {
		pconf.Transport.Config.LocalAddress = a.Transport.LocalAddress
		pconf.Transport.Config.PassiveMode = a.Transport.PassiveMode
	}
	if a.EbgpMultihop != nil {
		pconf.EbgpMultihop.Config.Enabled = a.EbgpMultihop.Enabled
		pconf.EbgpMultihop.Config.MultihopTtl = uint8(a.EbgpMultihop.MultihopTtl)
	}
	return pconf, nil
}
